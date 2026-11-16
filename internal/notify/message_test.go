package notify

import (
	"strings"
	"testing"
)

func sample() Message {
	return Message{
		Kind:   KindFailure,
		Title:  "Deploy automatico de loja falhou",
		Intro:  "O commit novo nao subiu.",
		Facts:  []Fact{{"App", "loja"}, {"Commit", "abc1234"}},
		Detail: "build: <script>alert(1)</script> & mais",
		Items:  []string{"postgres/loja"},
		Next:   "Detalhes na aba Apps.",
		Server: "exemplo.com.br",
	}
}

func TestHTMLTemTudoEscapado(t *testing.T) {
	h := sample().HTML()
	for _, want := range []string{
		"Deploy automatico de loja falhou", "O commit novo nao subiu.", "abc1234",
		"postgres/loja", "Detalhes na aba Apps.", "exemplo.com.br", "FALHA", "#c2410c",
		"&lt;script&gt;alert(1)&lt;/script&gt; &amp; mais",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("HTML sem %q", want)
		}
	}
	if strings.Contains(h, "<script>") {
		t.Error("o erro entrou no HTML sem escape")
	}
}

func TestHTMLSoMostraOQueTem(t *testing.T) {
	h := Message{Kind: KindTest, Title: "Teste", Server: "x.com"}.HTML()
	if !strings.Contains(h, "TESTE") || strings.Contains(h, "ERRO") {
		t.Errorf("teste sem detalhe mostrou a caixa de erro ou perdeu a etiqueta")
	}
}

func TestText(t *testing.T) {
	got := sample().Text()
	for _, want := range []string{"Deploy automatico de loja falhou\n", "Commit: abc1234\n", "- postgres/loja\n", "cais em exemplo.com.br"} {
		if !strings.Contains(got, want) {
			t.Errorf("texto sem %q:\n%s", want, got)
		}
	}
}
