package core

import (
	"time"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/bootstrap"
	"github.com/trdSystemDev/cais/internal/docker"
	"github.com/trdSystemDev/cais/internal/host"
)

// State e uma foto imutavel de tudo que o cais sabe naquele momento.
// Quem recebe um State pode le-lo a vontade, de qualquer goroutine.
//
// Slices do estado (ex: Setup.Steps) sao copy-on-write: quem altera cria
// uma lista nova em vez de mexer na que ja foi publicada.
type State struct {
	// Version aumenta a cada mudanca; interfaces podem usar pra ignorar
	// fotos repetidas.
	Version     uint64       `json:"version"`
	Environment Environment  `json:"environment"`
	Setup       Setup        `json:"setup"`
	Infra       Infra        `json:"infra"`
	Apps        Apps         `json:"apps"`
	Backup      BackupStatus `json:"backup"`
	Health      Health       `json:"health"`
	// Hardening e o Preparar VPS e as correcoes da aba Seguranca.
	Hardening Hardening `json:"hardening"`
	// Import e a importacao de banco em andamento (nil: nenhuma).
	Import *DBImport `json:"import,omitempty"`
}

// DBImport e o andamento de um banco sendo importado de outro servidor.
type DBImport struct {
	Engine string `json:"engine"`
	Name   string `json:"name"`
	// Source e a origem sem a senha (usuario@host:porta/banco).
	Source    string    `json:"source"`
	StartedAt time.Time `json:"started_at"`
	// Bytes e quanto do dump ja foi copiado.
	Bytes int64 `json:"bytes"`
}

// Infra e o estado das stacks base, como o docker ve.
type Infra struct {
	Checking  bool          `json:"checking"`
	CheckedAt time.Time     `json:"checked_at,omitzero"`
	Stacks    []StackStatus `json:"stacks,omitempty"`
	// Error e problema lendo a config (as stacks ainda sao listadas).
	Error string `json:"error,omitempty"`
}

// Apps e o estado dos apps: o que o cais guarda de cada um e como o docker
// ve os containers dele.
type Apps struct {
	Checking  bool        `json:"checking"`
	CheckedAt time.Time   `json:"checked_at,omitzero"`
	List      []AppStatus `json:"list,omitempty"`
	Error     string      `json:"error,omitempty"`
}

// AppStatus e um app, os containers dele e se ha um deploy em andamento.
type AppStatus struct {
	apps.App
	Deploying bool `json:"deploying,omitempty"`
	// BuildQueued: o deploy espera o build de outro app terminar (fila
	// global de build, um por vez).
	BuildQueued bool               `json:"build_queued,omitempty"`
	Containers  []docker.Container `json:"containers,omitempty"`
	Error       string             `json:"error,omitempty"`
}

// StackStatus e uma stack base e os containers dela.
type StackStatus struct {
	Name string `json:"name"`
	Desc string `json:"desc"`
	// Enabled diz se a stack esta ativa no config.yaml.
	Enabled bool `json:"enabled"`
	// Installed diz se o compose dela esta gravado (o Setup ja a subiu).
	Installed  bool               `json:"installed"`
	Containers []docker.Container `json:"containers,omitempty"`
	Error      string             `json:"error,omitempty"`
}

// Healthy diz se a stack esta toda no ar: instalada, com containers, todos
// rodando e nenhum com healthcheck falhando.
func (s StackStatus) Healthy() bool {
	if !s.Installed || s.Error != "" || len(s.Containers) == 0 {
		return false
	}
	for _, c := range s.Containers {
		if c.State != "running" || c.Health == "unhealthy" || c.Health == "starting" {
			return false
		}
	}
	return true
}

// Setup e o andamento da ultima execucao do Setup (bootstrap da VPS).
type Setup struct {
	// Service identifica o servico alterado; vazio quando o Setup e geral.
	Service    string           `json:"service,omitempty"`
	ServiceOn  bool             `json:"service_on,omitempty"`
	Running    bool             `json:"running"`
	Steps      []bootstrap.Step `json:"steps,omitempty"`
	StartedAt  time.Time        `json:"started_at,omitzero"`
	FinishedAt time.Time        `json:"finished_at,omitzero"`
	Error      string           `json:"error,omitempty"`
}

// Environment e o estado da maquina onde o cais roda.
type Environment struct {
	Checking  bool      `json:"checking"`
	CheckedAt time.Time `json:"checked_at,omitzero"`
	Host      Host      `json:"host"`
	Docker    Docker    `json:"docker"`
	// Security e a checagem de seguranca da VPS (cais host check): SSH,
	// firewall, fail2ban, atualizacoes, portas abertas.
	Security []host.Item `json:"security,omitempty"`
	// Tailscale e a VPN da VPS (o link de login enquanto espera).
	Tailscale host.Tailscale `json:"tailscale"`
}

// Checked diz se ja houve ao menos uma checagem concluida.
func (e Environment) Checked() bool {
	return !e.CheckedAt.IsZero()
}

// Ready diz se o ambiente esta pronto pro Setup.
func (e Environment) Ready() bool {
	return e.Checked() && e.Docker.OK()
}

// Host descreve o sistema operacional onde o cais roda.
type Host struct {
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
}

// Supported diz se e o sistema alvo (a VPS Linux). Em outros sistemas a
// interface funciona, mas as acoes de infraestrutura nao fazem sentido.
func (h Host) Supported() bool {
	return h.OS == "linux"
}

// Docker e o resultado da checagem do Docker em formato serializavel.
type Docker struct {
	// Installed e falso quando o binario docker nem existe; nesse caso
	// Daemon e Compose nao chegam a ser checados.
	Installed bool      `json:"installed"`
	CLI       Component `json:"cli"`
	Daemon    Component `json:"daemon"`
	Compose   Component `json:"compose"`
}

// OK diz se CLI, daemon e compose estao todos funcionando.
func (d Docker) OK() bool {
	return d.Installed && d.CLI.OK() && d.Daemon.OK() && d.Compose.OK()
}

// Component e uma parte checada: tem versao quando funciona, ou o texto
// do erro quando nao.
type Component struct {
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

// OK diz se o componente respondeu sem erro.
func (c Component) OK() bool {
	return c.Error == "" && c.Version != ""
}
