package mcpserver

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/trdSystemDev/cais/internal/docker"
)

const (
	// linhas devolvidas sem o lines: o bastante pra ver o estado do app sem
	// gastar o contexto da IA
	defaultLines = 50
	maxLines     = 1000
	// linhas lidas do docker quando ha grep: o filtro procura nelas
	grepWindow = 5000
)

const logsHelp = "por padrao as 50 linhas mais novas; lines ate 1000, since (10m, 2h ou um horario RFC 3339) " +
	"e grep (texto, sem diferenca de maiusculas, procurado nas ultimas 5000 linhas) pra achar um erro; sem as cores do terminal"

// logFilter sao os parametros de log comuns ao app_logs e ao infra_logs.
type logFilter struct {
	Lines int    `json:"lines,omitempty" jsonschema:"quantas linhas devolver, as mais novas (padrao 50, maximo 1000)"`
	Since string `json:"since,omitempty" jsonschema:"so as linhas depois de: duracao (10m, 2h) ou horario RFC 3339"`
	Grep  string `json:"grep,omitempty" jsonschema:"so as linhas com este texto, sem diferenca de maiusculas (ex: error)"`
}

// options valida o pedido e diz quanto ler do docker.
func (f logFilter) options() (docker.LogsOptions, error) {
	if f.Lines < 0 || f.Lines > maxLines {
		return docker.LogsOptions{}, fmt.Errorf("lines vai de 1 a %d", maxLines)
	}
	if f.Since != "" {
		d, err := time.ParseDuration(f.Since)
		if err != nil || d <= 0 {
			if _, err := time.Parse(time.RFC3339, f.Since); err != nil {
				return docker.LogsOptions{}, fmt.Errorf("since %q: use uma duracao (10m, 2h) ou um horario RFC 3339 (2026-01-02T15:04:05Z)", f.Since)
			}
		}
	}
	opt := docker.LogsOptions{Tail: f.lines(), Since: f.Since}
	if f.Grep != "" {
		opt.Tail = grepWindow
	}
	return opt, nil
}

func (f logFilter) lines() int {
	if f.Lines == 0 {
		return defaultLines
	}
	return f.Lines
}

var (
	// cores e movimentos de cursor do terminal (ESC [ ... letra)
	ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	// o horario do docker logs --timestamps com nanossegundos: o segundo basta
	dockerTime = regexp.MustCompile(`(?m)^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)\.\d+Z `)
)

// apply limpa a saida do docker pra IA: sem cores, horario ate o segundo,
// so as linhas do grep e no maximo lines linhas, as mais novas.
func (f logFilter) apply(out string) string {
	out = dockerTime.ReplaceAllString(ansi.ReplaceAllString(out, ""), "${1}Z ")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if out == "" {
		lines = nil
	}
	if f.Grep != "" {
		want := strings.ToLower(f.Grep)
		kept := lines[:0]
		for _, l := range lines {
			if strings.Contains(strings.ToLower(l), want) {
				kept = append(kept, l)
			}
		}
		lines = kept
	}
	if len(lines) == 0 {
		switch {
		case f.Grep != "":
			return fmt.Sprintf("nenhuma linha com %q nas ultimas %d do log.", f.Grep, grepWindow)
		case f.Since != "":
			return "nenhuma linha desde " + f.Since + "."
		}
		return "o log esta vazio."
	}
	if n := f.lines(); len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n") + "\n"
}
