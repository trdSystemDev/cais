package core

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
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
	if err := c.deps.SelfUpdate(ctx, version); err != nil {
		return r, err
	}
	c.deps.Logger.Info("atualizacao do cais", "de", r.From, "pra", version)
	r.Started = true
	return r, nil
}

// selfUpdate baixa o install.sh da tag e roda numa unidade transiente do
// systemd: fora do cgroup do cais.service, o restart do daemon nao mata o
// install.sh no meio. O install.sh confere o SHA-256 do binario.
func selfUpdate(ctx context.Context, version string) error {
	exec.CommandContext(ctx, "systemctl", "reset-failed", UpdateUnit+".service").Run()
	script := `f=$(mktemp) || exit 1
curl -fsSL "$1" -o "$f" && sh "$f" --version "$2"
s=$?; rm -f "$f"; exit $s`
	out, err := exec.CommandContext(ctx, "systemd-run", "--unit="+UpdateUnit, "--collect",
		"--description=cais: atualiza pra "+version,
		"--setenv=PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
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
