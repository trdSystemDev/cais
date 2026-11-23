package apps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/trdSystemDev/cais/internal/docker"
	"github.com/trdSystemDev/cais/internal/fsx"
)

// DeployResult e o que um deploy fez.
type DeployResult struct {
	App      string   `json:"app"`
	Commit   string   `json:"commit"`
	Warnings []string `json:"warnings,omitempty"`
	// Problems e por que o compose foi recusado (vazio se passou).
	Problems []string `json:"problems,omitempty"`
	// Images diz que subiu com as imagens guardadas do commit, sem build.
	Images bool `json:"images,omitempty"`
}

// Deploy baixa o commit de ref (vazio = o mais novo da branch do app),
// grava o .env, faz o build com a versao antiga ainda no ar, troca e sobe
// esperando os healthchecks. Se o up falhar e havia versao no ar, ela
// volta. Os avisos sobre o compose nao impedem o deploy.
func (m Manager) Deploy(ctx context.Context, name, ref string) (DeployResult, error) {
	return m.deploy(ctx, name, ref, Deploy{})
}

// AutoDeploy e o Deploy disparado por um commit novo na branch: fica
// registrado como automatico.
func (m Manager) AutoDeploy(ctx context.Context, name, commit string) (DeployResult, error) {
	return m.deploy(ctx, name, commit, Deploy{Auto: true})
}

// deploy faz o Deploy; tag tem o que o registro do deploy leva alem do
// resultado (origem da promocao, automatico).
func (m Manager) deploy(ctx context.Context, name, ref string, tag Deploy) (DeployResult, error) {
	// back e o ctx do que roda mesmo depois do limite do deploy: a volta da
	// versao anterior e a limpeza das imagens
	back := context.WithoutCancel(ctx)
	if m.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, m.Timeout)
		defer cancel()
	}
	dl := deadline{ctx: ctx, limit: m.Timeout}
	a, commit, err := m.deployTarget(ctx, name, ref, dl)
	if err != nil {
		return DeployResult{}, err
	}
	res := DeployResult{App: name, Commit: commit}

	// o codigo novo vai pra uma pasta propria: o app no ar nao e tocado ate
	// o build terminar
	dir := m.dir(name)
	next := NextDir
	os.RemoveAll(filepath.Join(dir, next))
	if err := m.Source.Download(ctx, a.Repo, commit, filepath.Join(dir, next)); err != nil {
		os.RemoveAll(filepath.Join(dir, next))
		return res, dl.wrap(err)
	}
	fail := func(err error) (DeployResult, error) {
		err = dl.wrap(err)
		os.RemoveAll(filepath.Join(dir, next))
		m.record(a, Deploy{At: m.Now(), Commit: commit, Error: err.Error(), Warnings: res.Warnings, Problems: res.Problems,
			PromotedFrom: tag.PromotedFrom, Auto: tag.Auto, Rollback: tag.Rollback, Transient: transient(err)})
		return res, err
	}
	cfg, built, err := m.buildNext(ctx, a, commit, tag, &res)
	if err != nil {
		return fail(err)
	}

	// troca: src vira src.prev e o novo vira src. Da marca ate o registro
	// do deploy, uma parada do daemon deixa o app entre as duas versoes; a
	// marca e o que o Recover usa pra voltar a boa.
	last := Deploy{Commit: commit, Warnings: res.Warnings, PromotedFrom: tag.PromotedFrom, Auto: tag.Auto,
		Rollback: tag.Rollback, Images: res.Images}
	if err := m.markSwap(a, last); err != nil {
		return fail(err)
	}
	defer fsx.Remove(m.swapPath(name)) // depois do registro
	hadPrev, err := m.swapIn(a)
	if err != nil {
		return fail(err)
	}

	p := m.project(a, SrcDir)
	upErr := m.upNew(ctx, a, p, cfg)
	last.At = m.Now()
	if upErr != nil {
		return res, m.failedUp(back, a, last, hadPrev, dl.wrap(upErr))
	}
	a.Commit = commit
	m.record(a, last)
	// as imagens deste commit ficam guardadas (rollback sem build) e as de
	// deploys velhos saem; o que sobrar sem tag e lixo. Falhar aqui nao
	// desfaz um deploy que deu certo.
	m.keepImages(back, name, built, commit)
	m.Docker.PruneImages(back, p.Name)
	return res, nil
}

// deadline e o limite de um deploy: wrap diz o motivo de um erro que veio
// dele (o docker morto pelo ctx so diz "signal: killed").
type deadline struct {
	ctx   context.Context
	limit time.Duration
}

func (d deadline) wrap(err error) error {
	if err != nil && errors.Is(d.ctx.Err(), context.DeadlineExceeded) {
		return &TimeoutError{Limit: d.limit, Err: err}
	}
	return err
}

// deployTarget le o app, confere a ref (vazia: a branch do app) e devolve
// o commit que vai subir. Antes, arruma as pastas de um deploy que o daemon
// nao terminou.
func (m Manager) deployTarget(ctx context.Context, name, ref string, dl deadline) (App, string, error) {
	a, err := m.Get(name)
	if err != nil {
		return App{}, "", err
	}
	if ref == "" {
		ref = a.Branch
	}
	if ref != "" && !refRe.MatchString(ref) {
		return App{}, "", fmt.Errorf("commit ou branch invalido %q", ref)
	}
	// um deploy anterior que o daemon nao terminou deixa as pastas no meio
	// da troca: a versao boa volta pro src antes de tudo (a deste deploy
	// sobe por cima)
	if _, err := m.recover(ctx, a, false); err != nil {
		return App{}, "", err
	}
	if a, err = m.Get(name); err != nil {
		return App{}, "", err
	}
	commit, err := m.Source.Resolve(ctx, a.Repo, ref)
	if err != nil {
		return App{}, "", dl.wrap(err)
	}
	return a, commit, nil
}

// buildNext prepara o codigo baixado em NextDir: confere o compose contra o
// formato do cais (problemas e avisos vao em res) e faz o build, ou usa as
// imagens guardadas de um rollback. Devolve o compose resolvido e as
// imagens que o build gera.
func (m Manager) buildNext(ctx context.Context, a App, commit string, tag Deploy, res *DeployResult) (cfg []byte, built []string, err error) {
	name, dir, next := a.Name, m.dir(a.Name), NextDir
	if _, err := os.Stat(filepath.Join(dir, next, a.ComposeFile())); err != nil {
		return nil, nil, fmt.Errorf("o commit %s nao tem o compose %s", short(commit), a.ComposeFile())
	}
	if err := m.writeDotEnv(a, next, commit); err != nil {
		return nil, nil, err
	}
	if err := m.infraSecrets(a); err != nil {
		return nil, nil, err
	}
	p := m.project(a, next)
	cfg, err = m.Docker.Config(ctx, p)
	if err != nil {
		return nil, nil, err
	}
	res.Problems, res.Warnings = CheckCompose(cfg, Rules{App: name, Src: filepath.Join(dir, next),
		SecretsDir: m.SecretsPath(name), DotEnv: m.dotEnvPath(a, next), Networks: m.Networks, Apps: m.otherApps(name)})
	if len(res.Problems) > 0 {
		return nil, nil, &ComposeError{App: name, Problems: res.Problems}
	}
	// voltar pra um deploy anterior usa as imagens guardadas dele (sem
	// build); sem elas, ou num deploy normal, o build faz
	built = builtImages(cfg, p.Name)
	res.Images = tag.Rollback && m.restoreImages(ctx, built, commit)
	if !res.Images {
		if err := m.Docker.Build(ctx, p); err != nil {
			return nil, nil, err
		}
	}
	return cfg, built, nil
}

// swapIn poe o codigo novo no ar: src vira src.prev (se havia deploy) e
// NextDir vira src. Numa falha, o que estava no ar volta pro lugar.
func (m Manager) swapIn(a App) (hadPrev bool, err error) {
	dir := m.dir(a.Name)
	hadPrev = m.deployed(a)
	os.RemoveAll(filepath.Join(dir, PrevDir))
	if hadPrev {
		if err := fsx.Rename(filepath.Join(dir, SrcDir), filepath.Join(dir, PrevDir)); err != nil {
			return hadPrev, err
		}
	}
	if err := fsx.Rename(filepath.Join(dir, NextDir), filepath.Join(dir, SrcDir)); err != nil {
		if hadPrev {
			fsx.Rename(filepath.Join(dir, PrevDir), filepath.Join(dir, SrcDir)) // o no ar volta pro lugar
		}
		return hadPrev, err
	}
	return hadPrev, nil
}

// upNew sobe o codigo novo: services atras do Traefik com healthcheck
// trocam sem queda; o up depois sobe o resto (e nao recria os que ja
// trocaram); e observa os containers.
func (m Manager) upNew(ctx context.Context, a App, p docker.Project, cfg []byte) error {
	for _, s := range rollingServices(cfg, p.Name) {
		if err := m.Docker.Rollout(ctx, p, s.name, s.image, s.drain); err != nil {
			return err
		}
	}
	if err := m.Docker.Up(ctx, p); err != nil {
		return err
	}
	return m.settle(ctx, a)
}

// failedUp registra o deploy cujo up falhou e, se havia versao anterior,
// volta pra ela (mesmo com o limite do deploy estourado: back nao tem
// prazo). Devolve o erro com o resultado da volta.
func (m Manager) failedUp(back context.Context, a App, last Deploy, hadPrev bool, upErr error) error {
	last.Error, last.Transient = upErr.Error(), transient(upErr)
	if hadPrev {
		// a volta roda mesmo com o limite do deploy estourado
		rctx, cancel := context.WithTimeout(back, RollbackTimeout)
		rerr := m.rollback(rctx, a)
		cancel()
		if rerr != nil {
			upErr = fmt.Errorf("%w\n\ne a volta pra versao anterior tambem falhou: %v", upErr, rerr)
		} else {
			last.RolledBack = true
			upErr = fmt.Errorf("%w\n\na versao anterior (%s) foi restaurada e continua no ar", upErr, short(a.Commit))
		}
	}
	m.record(a, last)
	return upErr
}

type rollingService struct {
	name, image string
	drain       bool // healthcheck segue a convencao do docker.DrainFile
}

// rollingServices sao os services que o deploy troca sem queda: atras do
// Traefik (traefik.enable) e com healthcheck no compose, que e o que faz o
// Traefik so mandar trafego pro container novo quando ele esta pronto. Os
// outros (banco, worker, sem healthcheck) sao recriados pelo up normal.
func rollingServices(configJSON []byte, project string) []rollingService {
	var c composeConfig
	if json.Unmarshal(configJSON, &c) != nil {
		return nil
	}
	var out []rollingService
	for _, n := range sortedKeys(c.Services) {
		s := c.Services[n]
		if s.Labels["traefik.enable"] != "true" || !s.Healthcheck.active() {
			continue
		}
		image := s.Image
		if image == "" {
			image = project + "-" + n // nome que o compose da a imagem do build
		}
		out = append(out, rollingService{n, image, s.Healthcheck.drains()})
	}
	return out
}

// otherApps sao os nomes dos outros apps (pra regra dos nomes do Traefik).
func (m Manager) otherApps(name string) []string {
	all, _ := m.List()
	var out []string
	for _, a := range all {
		if a.Name != name {
			out = append(out, a.Name)
		}
	}
	return out
}

// settle observa os containers por m.Settle: reiniciando, parado com erro ou
// unhealthy e falha do deploy (e a versao anterior volta). Parado com codigo
// 0 e servico de uma vez so (init, migracao separada) e esta certo.
func (m Manager) settle(ctx context.Context, a App) error {
	if m.Settle <= 0 {
		return nil
	}
	tick := min(2*time.Second, m.Settle)
	deadline := time.Now().Add(m.Settle)
	for {
		cs, err := m.Docker.PS(ctx, m.project(a, SrcDir))
		if err != nil {
			return err
		}
		for _, c := range cs {
			switch {
			case c.State == "restarting":
				return fmt.Errorf("o container %s reinicia sem parar (%s); veja o motivo em cais app logs %s %s", c.Name, c.Status, a.Name, c.Name)
			case (c.State == "exited" || c.State == "dead") && c.ExitCode != 0:
				return fmt.Errorf("o container %s parou com erro (codigo %d); veja o motivo em cais app logs %s %s", c.Name, c.ExitCode, a.Name, c.Name)
			case c.Health == "unhealthy":
				return fmt.Errorf("o container %s ficou unhealthy; veja cais app logs %s %s", c.Name, a.Name, c.Name)
			}
		}
		if !time.Now().Before(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(tick):
		}
	}
}

// rollback volta src.prev pro lugar e sobe de novo.
func (m Manager) rollback(ctx context.Context, a App) error {
	dir := m.dir(a.Name)
	failed := filepath.Join(dir, FailedDir)
	os.RemoveAll(failed)
	if err := fsx.Rename(filepath.Join(dir, SrcDir), failed); err != nil {
		return err
	}
	if err := fsx.Rename(filepath.Join(dir, PrevDir), filepath.Join(dir, SrcDir)); err != nil {
		return err
	}
	os.RemoveAll(failed)
	return m.upCurrent(ctx, a)
}

// upCurrent sobe o codigo de src, que e o do commit a.Commit. O build da
// versao e refeito so sem as imagens guardadas dela (as imagens do compose
// sao trocadas a cada build), e com o cache das camadas.
func (m Manager) upCurrent(ctx context.Context, a App) error {
	p := m.project(a, SrcDir)
	// as imagens guardadas da versao no ar voltam sem build; sem elas, o
	// build refaz (com o cache das camadas)
	cfg, err := m.Docker.Config(ctx, p)
	if err != nil || !m.restoreImages(ctx, builtImages(cfg, p.Name), a.Commit) {
		if err := m.Docker.Build(ctx, p); err != nil {
			return err
		}
	}
	return m.Docker.Up(ctx, p)
}

func (m Manager) swapPath(name string) string { return filepath.Join(m.dir(name), SwapFile) }

// markSwap grava a marca da troca com o registro do deploy em andamento.
func (m Manager) markSwap(a App, d Deploy) error {
	data, err := yaml.Marshal(d)
	if err != nil {
		return err
	}
	return fsx.WriteAtomic(m.swapPath(a.Name), data, 0o644)
}

// Interrupted diz se o app tem um deploy que o daemon nao terminou (a
// marca da troca ficou): o Recover termina.
func (m Manager) Interrupted(name string) bool {
	_, err := os.Stat(m.swapPath(name))
	return err == nil
}

// srcCommit e o commit do codigo numa pasta do app (o CAIS_COMMIT do .env
// que o deploy grava); vazio se a pasta nao existe.
func (m Manager) srcCommit(a App, src string) string {
	data, err := os.ReadFile(m.dotEnvPath(a, src))
	if err != nil {
		return ""
	}
	env, err := ParseEnv(data)
	if err != nil {
		return ""
	}
	return env["CAIS_COMMIT"]
}

// Recover termina o deploy que o daemon nao terminou (parou no meio da
// troca: queda, kill, reboot): volta a versao que estava no ar, como o
// deploy que falha faz, e registra o deploy como interrompido. Devolve se
// havia o que recuperar.
func (m Manager) Recover(ctx context.Context, name string) (bool, error) {
	a, err := m.Get(name)
	if err != nil {
		return false, err
	}
	return m.recover(ctx, a, true)
}

// recover faz o Recover. A versao boa e a do commit no ar (a.Commit, so
// muda no registro de um deploy que deu certo): esta em src (a troca nem
// tinha comecado, ou ja tinha voltado) ou em src.prev. Sem up (no comeco de
// um deploy novo) so arruma as pastas: o deploy novo sobe por cima.
func (m Manager) recover(ctx context.Context, a App, up bool) (bool, error) {
	data, err := os.ReadFile(m.swapPath(a.Name))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var d Deploy
	yaml.Unmarshal(data, &d) // marca ilegivel: ainda assim recupera

	dir := m.dir(a.Name)
	os.RemoveAll(filepath.Join(dir, NextDir))
	os.RemoveAll(filepath.Join(dir, FailedDir))
	good := false
	switch {
	case a.Commit == "": // primeiro deploy: nao tem versao anterior
	case m.srcCommit(a, SrcDir) == a.Commit:
		good = true
	case m.srcCommit(a, PrevDir) == a.Commit:
		if err := os.RemoveAll(filepath.Join(dir, SrcDir)); err != nil {
			return true, err
		}
		if err := fsx.Rename(filepath.Join(dir, PrevDir), filepath.Join(dir, SrcDir)); err != nil {
			return true, err
		}
		good = true
	}

	d.At, d.Interrupted, d.RolledBack = m.Now(), true, false
	d.Error = fmt.Sprintf("o deploy de %s foi interrompido no meio (o daemon parou)", short(d.Commit))
	var upErr error
	switch {
	case !good:
		d.Error += "; nao ha versao anterior pra voltar: faca o deploy de novo"
	case !up:
		d.Error += fmt.Sprintf("; o codigo de %s voltou pro lugar antes do deploy seguinte", short(a.Commit))
	default:
		if upErr = m.upCurrent(ctx, a); upErr != nil {
			d.Error += fmt.Sprintf("; voltar pra versao anterior (%s) falhou: %v", short(a.Commit), upErr)
		} else {
			d.RolledBack = true
			d.Error += fmt.Sprintf("; a versao anterior (%s) voltou e continua no ar", short(a.Commit))
		}
	}
	m.record(a, d)
	fsx.Remove(m.swapPath(a.Name))
	return true, upErr
}

func (m Manager) record(a App, d Deploy) {
	cur, err := m.Get(a.Name)
	if err != nil {
		return
	}
	if d.Error == "" {
		cur.Commit = d.Commit
		if !d.Rollback {
			cur.AutoHold = "" // deploy normal: o automatico segue
		}
	}
	cur.Last = &d
	cur.History = append([]Deploy{d}, cur.History...)
	if len(cur.History) > HistorySize {
		cur.History = cur.History[:HistorySize]
	}
	m.save(cur)
}

// Rollback volta o app pra um deploy anterior que deu certo (commit
// vazio: o anterior ao que esta no ar), com as imagens guardadas dele
// quando existem, pela troca normal (sem queda, e volta sozinho se falhar).
// As variaveis e os secrets sao os de agora. O deploy automatico fica
// parado ate a branch ganhar um commit novo: senao subiria de novo o que
// acabou de sair.
func (m Manager) Rollback(ctx context.Context, name, commit string) (DeployResult, error) {
	a, err := m.Get(name)
	if err != nil {
		return DeployResult{}, err
	}
	target := ""
	for _, d := range a.History {
		if !d.OK() {
			continue
		}
		if commit == "" && d.Commit != a.Commit {
			target = d.Commit
			break
		}
		if commit != "" && (d.Commit == commit || len(commit) >= 7 && strings.HasPrefix(d.Commit, commit)) {
			target = d.Commit
			break
		}
	}
	switch {
	case target == "" && commit == "":
		return DeployResult{}, fmt.Errorf("%s nao tem um deploy anterior que deu certo pra voltar", name)
	case target == "":
		return DeployResult{}, fmt.Errorf("o commit %s nao esta no historico de deploys que deram certo de %s (pra qualquer commit: cais app deploy %s -ref %s)", commit, name, name, commit)
	case target == a.Commit:
		return DeployResult{}, fmt.Errorf("%s ja esta no ar", short(target))
	}
	head, err := m.Source.Resolve(ctx, a.Repo, a.Branch)
	if err != nil {
		head = a.Commit
	}
	res, err := m.deploy(ctx, name, target, Deploy{Rollback: true})
	if err != nil {
		return res, err
	}
	if cur, err := m.Get(name); err == nil {
		cur.AutoHold = head
		m.save(cur)
	}
	return res, nil
}

func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}
