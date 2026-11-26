package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/trdSystemDev/cais/internal/databases"
)

// UpdateUnit e a unidade do systemd que roda o install.sh da atualizacao
// (fora do cais.service: o install.sh reinicia o daemon).
const UpdateUnit = "cais-update"

// UpdateResult e o que o UpdateCais fez.
type UpdateResult struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Started: a atualizacao comecou (falso: ja estava na versao pedida).
	Started bool `json:"started"`
}

var validTag = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// UpdateProgress e o andamento da atualizacao do cais, pra quem pediu nao
// ficar no escuro: o passo do install.sh, o download e o tempo.
type UpdateProgress struct {
	From      string    `json:"from"`
	To        string    `json:"to"`
	StartedAt time.Time `json:"started_at"`
	// Step e o ultimo passo do install.sh (as linhas "==> ").
	Step string `json:"step,omitempty"`
	// Bytes e quanto do binario ja baixou, de Total (0: nao sabe); Rate e a
	// velocidade em bytes por segundo.
	Bytes int64 `json:"bytes,omitempty"`
	Total int64 `json:"total,omitempty"`
	Rate  int64 `json:"rate,omitempty"`
	// Error e o motivo, quando a atualizacao terminou sem o daemon
	// reiniciar (a versao no ar continua).
	Error string `json:"error,omitempty"`
}

// Line e o andamento da atualizacao em uma linha (TUI e cais update): o tempo, o
// download (quanto, a velocidade e quanto falta) ou o passo do install.sh.
func (u *UpdateProgress) Line(now time.Time) string {
	parts := []string{"Atualizando o cais pra " + u.To, now.Sub(u.StartedAt).Round(time.Second).String()}
	downloading := u.Bytes > 0 && (u.Step == "" || strings.HasPrefix(u.Step, "Baixando"))
	switch {
	case downloading && u.Total > 0:
		s := fmt.Sprintf("baixando %s de %s (%d%%", databases.HumanSize(u.Bytes), databases.HumanSize(u.Total), min(100, u.Bytes*100/u.Total))
		if u.Rate > 0 {
			s += ", " + databases.HumanSize(u.Rate) + "/s"
			if left := u.Total - u.Bytes; left > 0 {
				s += fmt.Sprintf(", falta ~%s", (time.Duration(left/u.Rate+1) * time.Second).String())
			}
		}
		parts = append(parts, s+")")
	case downloading:
		parts = append(parts, "baixando "+databases.HumanSize(u.Bytes))
	case u.Step != "":
		parts = append(parts, u.Step)
	default:
		parts = append(parts, "comecando")
	}
	return strings.Join(parts, " · ")
}

// Running diz se a atualizacao ainda esta andando.
func (u *UpdateProgress) Running() bool { return u != nil && u.Error == "" }

// UpdateProbe e uma leitura do andamento da unidade cais-update.
type UpdateProbe struct {
	// Active: a unidade ainda roda o install.sh.
	Active bool
	// Steps sao os passos do install.sh ate agora; Last, a ultima linha
	// (o motivo, quando falha).
	Steps []string
	Last  string
	// Bytes e o tamanho do binario baixando.
	Bytes int64
}

// updateLimit e ate quando o daemon acompanha a atualizacao (o reinicio
// espera os deploys em andamento, ate 45min).
const updateLimit = time.Hour

// UpdateCais atualiza o cais pelo install.sh do release (o mesmo comando da
// instalacao, com as opcoes que ja estao no lugar: grupo e ssh). Sem
// version, o release mais novo. Volta assim que a atualizacao comeca: o
// install.sh roda numa unidade propria e reinicia o daemon no fim, que
// espera os deploys e backups em andamento. Fora do MCP.
func (c *Core) UpdateCais(ctx context.Context, version string) (UpdateResult, error) {
	r := UpdateResult{From: c.deps.Version}
	if r.From == "" || r.From == "dev" {
		return r, errors.New("este cais nao veio de um release (versao dev): atualize pelo install.sh")
	}
	if version == "" {
		tag, err := c.deps.LatestRelease(ctx)
		if err != nil {
			return r, fmt.Errorf("nao deu pra saber o release mais novo: %w", err)
		}
		if !newerVersion(tag, r.From) {
			r.To = r.From
			return r, nil
		}
		version = tag
	}
	if !validTag.MatchString(version) {
		return r, fmt.Errorf("versao invalida %q: use a tag do release (ex: v0.5.3)", version)
	}
	r.To = version
	if version == r.From {
		return r, nil
	}
	// duas ao mesmo tempo o systemd-run tambem recusa (o nome da unidade)
	if st, _ := c.Snapshot(ctx); st.Update.Running() {
		return r, errors.New("ja tem uma atualizacao do cais andando")
	}
	dir := filepath.Join(c.deps.DataDir, "update")
	// o que sobrou de uma atualizacao anterior que nao terminou
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return r, err
	}
	started := c.deps.Now()
	if err := c.deps.SelfUpdate(ctx, version, dir); err != nil {
		return r, err
	}
	c.deps.Logger.Info("atualizacao do cais", "de", r.From, "pra", version)
	c.update(func(s *State) { s.Update = &UpdateProgress{From: r.From, To: version, StartedAt: started} })
	go c.watchUpdate(version, started, dir)
	r.Started = true
	return r, nil
}

// watchUpdate poe no estado o andamento da atualizacao ate a unidade
// terminar. Se der certo, o install.sh reinicia o daemon e isto morre junto;
// se a unidade terminar e o daemon seguir aqui, falhou: fica o motivo.
func (c *Core) watchUpdate(version string, started time.Time, dir string) {
	ctx, cancel := context.WithTimeout(context.Background(), updateLimit)
	defer cancel()
	total := c.deps.ReleaseSize(ctx, version)
	var lastBytes int64
	lastAt := c.deps.Now()
	t := time.NewTicker(c.deps.UpdateTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			c.update(func(s *State) {
				if s.Update.Running() {
					u := *s.Update
					u.Error = "a atualizacao nao terminou em " + updateLimit.String() + " (journalctl -u " + UpdateUnit + ")"
					s.Update = &u
				}
			})
			return
		case <-t.C:
		}
		p := c.deps.ProbeUpdate(ctx, started, dir)
		now := c.deps.Now()
		rate := int64(0)
		if dt := now.Sub(lastAt).Seconds(); dt > 0 && p.Bytes > lastBytes {
			rate = int64(float64(p.Bytes-lastBytes) / dt)
		}
		lastBytes, lastAt = p.Bytes, now
		// uma copia nova: as fotos ja entregues (Snapshot, Subscribe) dividem
		// o ponteiro antigo e sao lidas sem o lock
		c.update(func(s *State) {
			if s.Update == nil || s.Update.To != version {
				return
			}
			u := *s.Update
			if n := len(p.Steps); n > 0 {
				u.Step = p.Steps[n-1]
			}
			u.Bytes, u.Total, u.Rate = p.Bytes, total, rate
			if !p.Active {
				u.Error = updateFailure(p.Last)
			}
			s.Update = &u
		})
		if !p.Active {
			c.deps.Logger.Error("atualizacao do cais falhou", "pra", version, "erro", updateFailure(p.Last))
			return
		}
	}
}

// updateFailure e o motivo pra mostrar: o die do install.sh vem com o
// prefixo "install.sh: ".
func updateFailure(last string) string {
	last = strings.TrimSpace(strings.TrimPrefix(last, "install.sh: "))
	if last == "" {
		return "o install.sh parou sem dizer o motivo (journalctl -u " + UpdateUnit + ")"
	}
	return last
}

// probeUpdate le a unidade cais-update: se ainda roda (systemctl), o que o
// install.sh escreveu (journal) e o binario baixando (a pasta temporaria do
// install.sh dentro de dir).
func probeUpdate(ctx context.Context, since time.Time, dir string) UpdateProbe {
	var p UpdateProbe
	out, _ := exec.CommandContext(ctx, "systemctl", "is-active", UpdateUnit+".service").Output()
	switch strings.TrimSpace(string(out)) {
	case "active", "activating", "reloading":
		p.Active = true
	}
	// _SYSTEMD_UNIT e so o que o install.sh escreveu: o -u traria tambem as
	// linhas do systemd ("Failed with result 'exit-code'")
	out, _ = exec.CommandContext(ctx, "journalctl", "_SYSTEMD_UNIT="+UpdateUnit+".service", "--since", "@"+strconv.FormatInt(since.Unix(), 10),
		"-o", "cat", "--no-pager", "-q").Output()
	p.Steps, p.Last = parseUpdateLog(string(out))
	// o install.sh baixa em dir/tmp.XXXX/cais-linux-<arch> e depois renomeia
	// pra cais: o maior arquivo da pasta e o binario
	matches, _ := filepath.Glob(filepath.Join(dir, "*", "cais*"))
	for _, m := range matches {
		if st, err := os.Stat(m); err == nil && st.Size() > p.Bytes {
			p.Bytes = st.Size()
		}
	}
	return p
}

// parseUpdateLog separa os passos do install.sh (as linhas "==> ") e a linha
// que explica o fim: o die (prefixo "install.sh: ") se houver, se nao a
// ultima.
func parseUpdateLog(out string) (steps []string, last string) {
	var died string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		last = line
		if strings.HasPrefix(line, "install.sh: ") {
			died = line
		}
		if step, ok := strings.CutPrefix(line, "==> "); ok {
			steps = append(steps, step)
		}
	}
	if died != "" {
		last = died
	}
	return steps, last
}

// releaseSize pergunta ao GitHub o tamanho do binario desta arquitetura no
// release (HEAD seguindo o redirecionamento pro arquivo).
func releaseSize(ctx context.Context, version string) int64 {
	url := ReleasesURL + "/download/" + version + "/cais-linux-" + runtime.GOARCH
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return 0
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength < 0 {
		return 0
	}
	return resp.ContentLength
}

// selfUpdate baixa o install.sh da tag e roda numa unidade transiente do
// systemd: fora do cgroup do cais.service, o restart do daemon nao mata o
// install.sh no meio. O install.sh confere o SHA-256 do binario.
func selfUpdate(ctx context.Context, version, downloadDir string) error {
	exec.CommandContext(ctx, "systemctl", "reset-failed", UpdateUnit+".service").Run()
	script := `f=$(mktemp) || exit 1
curl -fsSL "$1" -o "$f" && sh "$f" --version "$2"
s=$?; rm -f "$f"; exit $s`
	out, err := exec.CommandContext(ctx, "systemd-run", "--unit="+UpdateUnit, "--collect",
		"--description=cais: atualiza pra "+version,
		"--setenv=PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		// o install.sh baixa aqui: o daemon mede o arquivo crescendo
		"--setenv=CAIS_DOWNLOAD_DIR="+downloadDir,
		"/bin/sh", "-c", script, "sh", ReleasesURL+"/download/"+version+"/install.sh", version).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "already") || strings.Contains(msg, "exists") {
			return errors.New("ja tem uma atualizacao rodando (journalctl -u " + UpdateUnit + ")")
		}
		return fmt.Errorf("nao deu pra comecar a atualizacao: %s", firstLineOr(msg, err))
	}
	return nil
}

func firstLineOr(s string, err error) string {
	if s == "" {
		return err.Error()
	}
	line, _, _ := strings.Cut(s, "\n")
	return line
}
