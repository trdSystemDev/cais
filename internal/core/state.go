package core

import "time"

// State e uma foto imutavel de tudo que o vpsctl sabe naquele momento.
// Quem recebe um State pode le-lo a vontade, de qualquer goroutine.
//
// Hoje State so tem valores (sem slices/maps/ponteiros), entao copiar ja
// basta. Quando entrar lista de apps etc., quem altera deve criar uma
// lista nova (copy-on-write) em vez de mexer na que ja foi publicada.
type State struct {
	// Version aumenta a cada mudanca; interfaces podem usar pra ignorar
	// fotos repetidas.
	Version     uint64      `json:"version"`
	Environment Environment `json:"environment"`
}

// Environment e o estado da maquina onde o vpsctl roda.
type Environment struct {
	Checking  bool      `json:"checking"`
	CheckedAt time.Time `json:"checked_at,omitzero"`
	Host      Host      `json:"host"`
	Docker    Docker    `json:"docker"`
}

// Checked diz se ja houve ao menos uma checagem concluida.
func (e Environment) Checked() bool {
	return !e.CheckedAt.IsZero()
}

// Ready diz se o ambiente esta pronto pro Setup.
func (e Environment) Ready() bool {
	return e.Checked() && e.Docker.OK()
}

// Host descreve o sistema operacional onde o vpsctl roda.
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
