// Package notify envia os e-mails de aviso do cais pela API transacional
// da Brevo, em HTML (Message.HTML) com a alternativa em texto puro.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// BrevoAPI e o endpoint de envio de e-mail transacional da Brevo.
const BrevoAPI = "https://api.brevo.com/v3/smtp/email"

// client e o cliente HTTP do envio. O prazo proprio vale mesmo se quem
// chama esquecer de por um no contexto: um envio preso nao segura o
// desligamento do daemon. Variavel pros testes.
var client = &http.Client{Timeout: 30 * time.Second}

// Mail e um e-mail de aviso.
type Mail struct {
	From    string
	To      []string
	Subject string
	Text    string
	// HTML e opcional: com ele, Text vira a alternativa em texto puro.
	HTML string
}

type address struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type brevoMail struct {
	Sender      address   `json:"sender"`
	To          []address `json:"to"`
	Subject     string    `json:"subject"`
	TextContent string    `json:"textContent"`
	HTMLContent string    `json:"htmlContent,omitempty"`
}

// Send envia m pela Brevo em url (BrevoAPI fora dos testes) com a chave
// key. O erro traz a resposta da Brevo (chave invalida, remetente nao
// validado), nunca a chave.
func Send(ctx context.Context, url, key string, m Mail) error {
	if key == "" {
		return errors.New("falta a chave da API da Brevo (secret brevo_api_key)")
	}
	if len(m.To) == 0 {
		return errors.New("sem destinatarios")
	}
	body := brevoMail{
		Sender:      address{Email: m.From, Name: "cais"},
		Subject:     m.Subject,
		TextContent: m.Text,
		HTMLContent: m.HTML,
	}
	for _, to := range m.To {
		body.To = append(body.To, address{Email: to})
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("api-key", key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("brevo: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return fmt.Errorf("brevo recusou o envio (%s): %s", resp.Status, brevoError(resp.Body))
}

// brevoError tira a mensagem do corpo de erro da Brevo
// ({"code": "...", "message": "..."}), ou o comeco do corpo.
func brevoError(r io.Reader) string {
	raw, _ := io.ReadAll(io.LimitReader(r, 4096))
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Message != "" {
		if e.Code != "" {
			return e.Message + " (" + e.Code + ")"
		}
		return e.Message
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	if s == "" {
		return "sem detalhe"
	}
	return s
}
