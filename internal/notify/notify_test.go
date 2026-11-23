package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSendBrevo(t *testing.T) {
	var got brevoMail
	var key string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("api-key")
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"messageId":"<x@smtp-relay>"}`))
	}))
	defer srv.Close()

	err := Send(context.Background(), srv.URL, "xkeysib-teste", Mail{
		From: "cais@exemplo.com.br", To: []string{"a@exemplo.com.br", "b@exemplo.com.br"},
		Subject: "assunto", Text: "corpo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if key != "xkeysib-teste" {
		t.Errorf("api-key = %q", key)
	}
	if got.Sender.Email != "cais@exemplo.com.br" || len(got.To) != 2 || got.To[1].Email != "b@exemplo.com.br" ||
		got.Subject != "assunto" || got.TextContent != "corpo" {
		t.Errorf("corpo enviado = %+v", got)
	}
}

func TestSendErroDaBrevo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"code":"unauthorized","message":"Key not found"}`))
	}))
	defer srv.Close()

	err := Send(context.Background(), srv.URL, "xkeysib-segredo", Mail{From: "c@x.com", To: []string{"a@x.com"}})
	if err == nil || !strings.Contains(err.Error(), "Key not found") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "segredo") {
		t.Error("a chave vazou no erro")
	}
}

func TestSendSemChave(t *testing.T) {
	if err := Send(context.Background(), "http://nada", "", Mail{To: []string{"a@x.com"}}); err == nil {
		t.Fatal("sem chave deveria falhar")
	}
}

// A Brevo que nunca responde nao prende o envio, mesmo com um contexto sem
// prazo: o cliente tem o prazo dele.
func TestSendTemPrazoProprio(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer srv.Close()
	defer close(block)

	old := client
	client = &http.Client{Timeout: 200 * time.Millisecond}
	defer func() { client = old }()

	start := time.Now()
	err := Send(context.Background(), srv.URL, "xkeysib-teste", Mail{From: "c@x.com", To: []string{"a@x.com"}})
	if err == nil {
		t.Fatal("envio sem resposta devolveu sucesso")
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("Send voltou em %v; o prazo do cliente e 200ms", took)
	}
	if !strings.Contains(err.Error(), "brevo") {
		t.Fatalf("erro sem o contexto do envio: %v", err)
	}
}
