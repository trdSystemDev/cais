package github

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// O GitHub limita as requisicoes por hora em cotas separadas: a do app
// (chamadas com o JWT: achar a instalacao, pedir token) e a de cada
// instalacao (chamadas com o token: commits, tarball, compare). Cada
// resposta diz quanto sobra da cota que gastou.

// QuotaApp e a cota das chamadas com o JWT do app.
const QuotaApp = "app"

// QuotaInstallation e a cota das chamadas com o token de uma instalacao.
func QuotaInstallation(id int64) string { return fmt.Sprintf("instalacao %d", id) }

// Quota e o que o GitHub informou de uma cota na ultima resposta.
type Quota struct {
	Name      string
	Limit     int
	Remaining int
	Reset     time.Time // quando a cota volta inteira
}

// RateLimitError e a cota esgotada (ou o GitHub pedindo pra esperar).
type RateLimitError struct {
	Quota string
	Until time.Time
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("limite de requisicoes da API do GitHub esgotado (cota %s) ate %s: tente depois desse horario",
		e.Quota, e.Until.Local().Format("15:04:05"))
}

// Limits guarda o estado de cada cota. O zero vale; metodos aceitam nil.
type Limits struct {
	mu     sync.Mutex
	quotas map[string]*quotaState
}

type quotaState struct {
	Quota
	blocked time.Time // nao chamar antes disso (cota esgotada ou Retry-After)
}

// wait recusa a chamada enquanto a cota esta esgotada: insistir com o
// limite estourado faz o GitHub bloquear por mais tempo.
func (l *Limits) wait(quota string, now time.Time) error {
	if l == nil || quota == "" {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if q := l.quotas[quota]; q != nil && now.Before(q.blocked) {
		return &RateLimitError{Quota: quota, Until: q.blocked}
	}
	return nil
}

// observe anota o que a resposta diz da cota e devolve *RateLimitError
// quando a resposta e a recusa por limite (403/429 com a cota zerada ou
// Retry-After, o limite secundario de rajadas).
func (l *Limits) observe(quota string, status int, h http.Header, now time.Time) error {
	limit, errL := strconv.Atoi(h.Get("X-RateLimit-Limit"))
	remaining, errR := strconv.Atoi(h.Get("X-RateLimit-Remaining"))
	reset, errS := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64)
	known := errL == nil && errR == nil && errS == nil

	var blocked time.Time
	if status == http.StatusForbidden || status == http.StatusTooManyRequests {
		if secs, err := strconv.Atoi(h.Get("Retry-After")); err == nil {
			blocked = now.Add(time.Duration(secs) * time.Second)
		} else if known && remaining == 0 {
			blocked = time.Unix(reset, 0)
		} else if status == http.StatusTooManyRequests {
			blocked = now.Add(time.Minute) // sem dizer quanto: espera 1 minuto
		}
	}
	if known && remaining == 0 && blocked.IsZero() {
		blocked = time.Unix(reset, 0) // esta foi a ultima: a proxima seria recusada
	}

	if l != nil && quota != "" && (known || !blocked.IsZero()) {
		l.mu.Lock()
		if l.quotas == nil {
			l.quotas = map[string]*quotaState{}
		}
		q := l.quotas[quota]
		if q == nil {
			q = &quotaState{Quota: Quota{Name: quota}}
			l.quotas[quota] = q
		}
		if known {
			q.Limit, q.Remaining, q.Reset = limit, remaining, time.Unix(reset, 0)
		}
		if blocked.After(q.blocked) {
			q.blocked = blocked
		}
		l.mu.Unlock()
	}
	// so a recusa e erro; a ultima requisicao da cota (remaining 0 com 200)
	// deu certo
	if status == http.StatusForbidden || status == http.StatusTooManyRequests {
		if !blocked.IsZero() {
			return &RateLimitError{Quota: quota, Until: blocked}
		}
	}
	return nil
}

// Low devolve a cota mais apertada quando alguma tem menos que a reserva
// (10% do limite, no minimo 100) ou esta bloqueada. Quem nao e urgente (o
// deploy automatico) espera, e a reserva fica pros deploys manuais.
func (l *Limits) Low(now time.Time) (Quota, bool) {
	if l == nil {
		return Quota{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var worst *quotaState
	for _, q := range l.quotas {
		low := now.Before(q.blocked)
		if q.Limit > 0 && now.Before(q.Reset) && q.Remaining < max(100, q.Limit/10) {
			low = true
		}
		if low && (worst == nil || q.Remaining < worst.Remaining) {
			worst = q
		}
	}
	if worst == nil {
		return Quota{}, false
	}
	out := worst.Quota
	if worst.blocked.After(out.Reset) {
		out.Reset = worst.blocked
	}
	return out, true
}
