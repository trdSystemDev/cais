package notify

import (
	"bytes"
	_ "embed"
	"html/template"
	"strings"
)

// Tipos de aviso: mudam a cor e a etiqueta do e-mail.
const (
	KindFailure = "falha"
	KindTest    = "teste"
)

// Fact e uma linha "rotulo: valor" do resumo (app, commit, horario).
type Fact struct{ Label, Value string }

// Message e o conteudo de um aviso, que vira o e-mail em HTML e em texto.
type Message struct {
	Kind  string
	Title string // ex: "Deploy automatico de loja falhou"
	Intro string // um paragrafo do que aconteceu
	Facts []Fact
	// Detail e o erro como veio (vai em fonte monoespacada, sem quebrar).
	Detail string
	// Items e uma lista curta (ex: os databases que ficaram de fora).
	Items []string
	// Next e o que o cais faz agora e onde ver mais.
	Next string
	// Server e o dominio da VPS, que aparece no topo e no rodape.
	Server string
}

//go:embed mail.html.tmpl
var mailHTML string

var htmlTmpl = template.Must(template.New("mail").Parse(mailHTML))

type palette struct {
	Label, Accent, Soft string
}

var palettes = map[string]palette{
	KindFailure: {"FALHA", "#c2410c", "#fff4ed"},
	KindTest:    {"TESTE", "#2563eb", "#eff4ff"},
}

// HTML e o e-mail em HTML: tabelas e estilos inline, que e o que os
// clientes de e-mail (Outlook inclusive) respeitam. Todo texto e escapado.
func (m Message) HTML() string {
	p, ok := palettes[m.Kind]
	if !ok {
		p = palettes[KindFailure]
	}
	var b bytes.Buffer
	if err := htmlTmpl.Execute(&b, struct {
		Message
		P palette
	}{m, p}); err != nil {
		panic(err) // template fixo e testado
	}
	return b.String()
}

// Text e a versao em texto puro, pra cliente que nao mostra HTML.
func (m Message) Text() string {
	var b strings.Builder
	b.WriteString(m.Title + "\n\n")
	if m.Intro != "" {
		b.WriteString(m.Intro + "\n\n")
	}
	for _, it := range m.Items {
		b.WriteString("- " + it + "\n")
	}
	if len(m.Items) > 0 {
		b.WriteString("\n")
	}
	for _, f := range m.Facts {
		b.WriteString(f.Label + ": " + f.Value + "\n")
	}
	if len(m.Facts) > 0 {
		b.WriteString("\n")
	}
	if m.Detail != "" {
		b.WriteString(m.Detail + "\n\n")
	}
	if m.Next != "" {
		b.WriteString(m.Next + "\n\n")
	}
	b.WriteString("-- \nAviso automatico do cais em " + m.Server + "\n")
	return b.String()
}
