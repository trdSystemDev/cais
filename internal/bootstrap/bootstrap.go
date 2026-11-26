package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/fsx"
	"github.com/trdSystemDev/cais/internal/secrets"
	"github.com/trdSystemDev/cais/internal/stacks"
)

// Status e a situacao de um passo do Setup.
type Status string

const (
	Pending Status = "pending"
	Running Status = "running"
	Done    Status = "done"
	Failed  Status = "failed"
)

// Step e um passo do Setup, na ordem em que roda.
type Step struct {
	ID     string `json:"id"`
	Desc   string `json:"desc"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Deps sao os caminhos e comandos que o Setup usa. Os testes trocam os
// comandos do docker por fakes e os caminhos por pastas temporarias.
type Deps struct {
	ConfigPath string
	// SecretsDir e a pasta dos secrets centrais; vai como caminho absoluto
	// pros compose.
	SecretsDir string
	StackDir   func(name string) string

	CheckDocker func(ctx context.Context) error
	ComposeUp   func(ctx context.Context, file string, recreate bool) error
	ComposeDown func(ctx context.Context, file string) error
	Exec        func(ctx context.Context, container string, args ...string) (string, error)
}

func (d Deps) store() secrets.FileStore { return secrets.FileStore{Dir: d.SecretsDir} }

// ComposeFile e o caminho do compose gerado de uma stack.
func (d Deps) ComposeFile(stack string) string {
	return filepath.Join(d.StackDir(stack), "docker-compose.yml")
}

// DisabledSuffix marca o compose de uma stack desativada: o arquivo fica
// como registro do que existia (volumes inclusive), mas nao e mais
// detectado como stack no ar.
const DisabledSuffix = ".desativada"

// stacksOnDisk lista as stacks que tem compose gravado (ativo).
func (d Deps) stacksOnDisk() ([]string, error) {
	root := filepath.Dir(d.StackDir("x"))
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(d.ComposeFile(e.Name())); err == nil {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// Mudancas no compose de uma stack em relacao ao que esta no disco.
const (
	ChangeNew       = "nova"
	ChangeModified  = "alterada"
	ChangeUnchanged = "igual"
	ChangeDisabled  = "desativada"
)

// StackPlan e o que o Setup vai fazer com uma stack.
type StackPlan struct {
	Name   string `json:"name"`
	Desc   string `json:"desc"`
	File   string `json:"file"`
	Change string `json:"change"`
}

// Plan e o que o Setup faria agora. Montar o plano nao grava nada.
type Plan struct {
	Configured bool          `json:"configured"`
	Config     config.Config `json:"config"`
	// Problems impedem o Setup de rodar (config ausente ou invalida).
	Problems []string `json:"problems,omitempty"`
	// Missing sao secrets que o usuario precisa informar antes.
	Missing       []stacks.Provided `json:"missing,omitempty"`
	CreateSecrets []string          `json:"create_secrets,omitempty"`
	Stacks        []StackPlan       `json:"stacks,omitempty"`
	// Disable sao stacks com compose no disco que nao estao mais ativas
	// (desligadas na config ou de versoes antigas do cais): o Setup as
	// derruba, sem apagar os volumes.
	Disable []StackPlan `json:"disable,omitempty"`
	Steps   []Step      `json:"steps,omitempty"`
	// CAFile e o certificado da CA local (tls: selfsigned), que quem testa
	// instala como confiavel. Vazio com Let's Encrypt.
	CAFile string `json:"ca_file,omitempty"`
	// Databases e como chegar em cada banco ativo (so pelo loopback da
	// VPS), e Tunnel o comando de tunel SSH que abre todos de uma vez.
	Databases []DBAccess `json:"databases,omitempty"`
	Tunnel    string     `json:"tunnel,omitempty"`
}

// DBAccess e como administrar um banco: por tunel SSH ate o loopback da
// VPS, com o superusuario. A senha nunca vai no plano, so onde le-la.
type DBAccess struct {
	Name         string `json:"name"`
	Port         int    `json:"port"`
	User         string `json:"user"`
	PasswordFile string `json:"password_file"`
}

// databases sao os bancos que o cais sobe, na ordem das stacks.
var databases = []struct {
	stack, name string
	port        int
	user        string
	secret      string
}{
	{"postgres", "Postgres", 5432, "postgres", "postgres_password"},
	{"mysql", "MySQL", 3306, "root", "mysql_root_password"},
}

// Ready diz se o Setup pode rodar.
func (p Plan) Ready() bool {
	return p.Configured && len(p.Problems) == 0 && len(p.Missing) == 0
}

// Blockers descreve, em texto, o que impede o Setup de rodar.
func (p Plan) Blockers() []string {
	out := slices.Clone(p.Problems)
	for _, m := range p.Missing {
		out = append(out, fmt.Sprintf("falta informar o secret %s: %s", m.Name, m.Desc))
	}
	return out
}

// MakePlan monta o plano a partir do config.yaml e do que ja existe no
// disco. Config ausente ou invalida nao e erro: vira Problems. Erro so
// quando nao da pra ler o disco.
func MakePlan(d Deps) (Plan, error) {
	var p Plan
	cfg, err := config.Load(d.ConfigPath)
	if errors.Is(err, config.ErrNotFound) {
		p.Config = config.Default()
		p.Problems = append(p.Problems, err.Error())
		return p, nil
	}
	if err != nil {
		p.Problems = append(p.Problems, err.Error())
		return p, nil
	}
	p.Configured = true
	p.Config = cfg
	tunnel := "ssh -N"
	for _, db := range databases {
		if !cfg.HasStack(db.stack) {
			continue
		}
		p.Databases = append(p.Databases, DBAccess{
			Name: db.name, Port: db.port, User: db.user,
			PasswordFile: filepath.Join(d.SecretsDir, db.secret),
		})
		tunnel += fmt.Sprintf(" -L %d:127.0.0.1:%d", db.port, db.port)
	}
	if len(p.Databases) > 0 {
		p.Tunnel = tunnel + " <usuario>@<vps>"
	}
	if !cfg.LetsEncrypt() {
		p.CAFile = filepath.Join(d.SecretsDir, secrets.TLSCACert)
	}

	enabled, err := stacks.Enabled(cfg)
	if err != nil {
		p.Problems = append(p.Problems, err.Error())
		return p, nil
	}
	if p.CreateSecrets, p.Missing, err = stacks.PlanSecrets(d.store(), cfg); err != nil {
		return p, err
	}
	for _, st := range enabled {
		r, err := stacks.Render(st.Name, cfg, d.SecretsDir)
		if err != nil {
			return p, err
		}
		file := d.ComposeFile(st.Name)
		change, err := compare(file, r.Compose)
		if err != nil {
			return p, err
		}
		p.Stacks = append(p.Stacks, StackPlan{Name: st.Name, Desc: st.Desc, File: file, Change: change})
	}
	onDisk, err := d.stacksOnDisk()
	if err != nil {
		return p, err
	}
	for _, name := range onDisk {
		if cfg.HasStack(name) {
			continue
		}
		desc := "(stack que nao existe mais no cais)"
		if st, ok := stacks.Get(name); ok {
			desc = st.Desc
		}
		p.Disable = append(p.Disable, StackPlan{Name: name, Desc: desc, File: d.ComposeFile(name), Change: ChangeDisabled})
	}
	p.Steps = steps(enabled, p.Disable, cfg)
	return p, nil
}

func compare(file string, want []byte) (string, error) {
	have, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return ChangeNew, nil
	}
	if err != nil {
		return "", err
	}
	if bytes.Equal(have, want) {
		return ChangeUnchanged, nil
	}
	return ChangeModified, nil
}

const (
	stepDocker       = "docker"
	stepSecrets      = "secrets"
	stepFiles        = "files"
	stepGarageLayout = "garage-layout"
	stackStepPrefix  = "stack:"
	stopStepPrefix   = "stop:"
)

func steps(enabled []stacks.Stack, disable []StackPlan, cfg config.Config) []Step {
	out := []Step{
		{ID: stepDocker, Desc: "Docker e Compose funcionando"},
		{ID: stepSecrets, Desc: "Gerar os secrets que faltam"},
		{ID: stepFiles, Desc: "Gravar o compose das stacks"},
	}
	// derruba antes de subir: libera portas que uma stack nova pode usar
	for _, sp := range disable {
		out = append(out, Step{ID: stopStepPrefix + sp.Name, Desc: "Derrubar " + sp.Name + " (desativada; volumes mantidos)"})
	}
	for _, st := range enabled {
		out = append(out, Step{ID: stackStepPrefix + st.Name, Desc: "Subir " + st.Name + " (" + st.Desc + ")"})
	}
	if cfg.HasStack("garage") {
		out = append(out, Step{ID: stepGarageLayout, Desc: "Preparar o Garage (layout de no unico)"})
	}
	for i := range out {
		out[i].Status = Pending
	}
	return out
}

// Run aplica a config nos servicos ligados: gera secrets, grava os compose e sobe as stacks em
// ordem, esperando cada uma ficar saudavel antes da proxima. report recebe
// uma copia dos passos a cada mudanca. Pode rodar de novo a qualquer
// momento: nada que ja existe e regenerado e stacks iguais nao sao
// recriadas. Para no primeiro passo que falhar.
func Run(ctx context.Context, d Deps, report func([]Step)) ([]Step, error) {
	plan, err := MakePlan(d)
	if err != nil {
		return nil, err
	}
	if !plan.Ready() {
		return nil, fmt.Errorf("setup nao pode rodar:\n- %s", strings.Join(plan.Blockers(), "\n- "))
	}

	r := &runner{steps: plan.Steps, report: report}
	cfg := plan.Config

	if err := r.do(stepDocker, func() (string, error) {
		return "", d.CheckDocker(ctx)
	}); err != nil {
		return r.steps, err
	}

	var created stacks.SecretsResult
	if err := r.do(stepSecrets, func() (string, error) {
		res, err := stacks.EnsureSecrets(d.store(), cfg)
		if err != nil {
			return "", err
		}
		if len(res.Missing) > 0 {
			return "", fmt.Errorf("falta informar o secret %s", res.Missing[0].Name)
		}
		created = res
		if len(res.Created) == 0 {
			return "nenhum novo", nil
		}
		return fmt.Sprintf("%d gerados", len(res.Created)), nil
	}); err != nil {
		return r.steps, err
	}

	if err := r.do(stepFiles, func() (string, error) {
		var changed []string
		for _, sp := range plan.Stacks {
			if sp.Change == ChangeUnchanged {
				continue
			}
			rendered, err := stacks.Render(sp.Name, cfg, d.SecretsDir)
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(filepath.Dir(sp.File), 0o700); err != nil {
				return "", err
			}
			if err := fsx.WriteAtomic(sp.File, rendered.Compose, 0o644); err != nil {
				return "", err
			}
			// stack reativada: o registro de quando ela foi desligada sai
			if err := os.Remove(sp.File + DisabledSuffix); err != nil && !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
			changed = append(changed, sp.Name)
		}
		if len(changed) == 0 {
			return "nenhum mudou", nil
		}
		return strings.Join(changed, ", "), nil
	}); err != nil {
		return r.steps, err
	}

	for _, sp := range plan.Disable {
		if err := r.do(stopStepPrefix+sp.Name, func() (string, error) {
			if err := d.ComposeDown(ctx, sp.File); err != nil {
				return "", err
			}
			return "", fsx.Rename(sp.File, sp.File+DisabledSuffix)
		}); err != nil {
			return r.steps, err
		}
	}

	for _, sp := range plan.Stacks {
		// secret montado que mudou (htpasswd, certificado) so e lido de novo
		// com o container recriado
		recreate := slices.Contains(created.Recreate, sp.Name)
		if err := r.do(stackStepPrefix+sp.Name, func() (string, error) {
			return "", d.ComposeUp(ctx, sp.File, recreate)
		}); err != nil {
			return r.steps, err
		}
	}

	if cfg.HasStack("garage") {
		if err := r.do(stepGarageLayout, func() (string, error) {
			return garageLayout(ctx, d.Exec)
		}); err != nil {
			return r.steps, err
		}
	}

	return r.steps, nil
}

type runner struct {
	steps  []Step
	report func([]Step)
}

// do roda um passo, publicando running e depois done ou failed.
func (r *runner) do(id string, fn func() (string, error)) error {
	i := slices.IndexFunc(r.steps, func(s Step) bool { return s.ID == id })
	r.set(i, Running, "")
	detail, err := fn()
	if err != nil {
		r.set(i, Failed, err.Error())
		return fmt.Errorf("%s: %w", r.steps[i].Desc, err)
	}
	r.set(i, Done, detail)
	return nil
}

// set troca o passo numa lista nova: a anterior pode estar publicada.
func (r *runner) set(i int, st Status, detail string) {
	steps := slices.Clone(r.steps)
	steps[i].Status = st
	steps[i].Detail = detail
	r.steps = steps
	if r.report != nil {
		r.report(steps)
	}
}

// garageLayout da ao no unico do Garage o papel de guardar tudo. So na
// primeira vez (layout na versao 0): reaplicar a versao 1 e erro no Garage.
// A capacidade so pesa na distribuicao entre nos; com um no nao limita nada.
func garageLayout(ctx context.Context, exec func(context.Context, string, ...string) (string, error)) (string, error) {
	g := func(args ...string) (string, error) {
		return exec(ctx, "garage", append([]string{"/garage"}, args...)...)
	}
	show, err := g("layout", "show")
	if err != nil {
		return "", err
	}
	if !strings.Contains(show, "Current cluster layout version: 0") {
		return "ja preparado", nil
	}
	id, err := g("node", "id", "-q")
	if err != nil {
		return "", err
	}
	id, _, _ = strings.Cut(strings.TrimSpace(id), "@")
	if id == "" {
		return "", errors.New("garage nao informou o id do no")
	}
	if _, err := g("layout", "assign", "-z", "dc1", "-c", "100G", id); err != nil {
		return "", err
	}
	if _, err := g("layout", "apply", "--version", "1"); err != nil {
		return "", err
	}
	return "no unico configurado", nil
}
