package core

import (
	"context"
	"errors"
	"time"

	"github.com/trdSystemDev/cais/internal/host"
)

// HardenRequest e o Preparar VPS da TUI (o cais host harden, pelo daemon).
type HardenRequest struct {
	// User e o admin (sudo, so chave) que substitui o root no SSH.
	User string `json:"user"`
	// Password e a senha do sudo do admin; vazia mantem a atual.
	Password string `json:"password,omitempty"`
}

// Hardening e o andamento do Preparar VPS e da correcao de um item.
type Hardening struct {
	Running    bool        `json:"running"`
	User       string      `json:"user,omitempty"`
	Steps      []host.Step `json:"steps,omitempty"`
	StartedAt  time.Time   `json:"started_at,omitzero"`
	FinishedAt time.Time   `json:"finished_at,omitzero"`
	Error      string      `json:"error,omitempty"`
	// ReloadedAt e quando o SSH mudou; RollbackAt, quando ele volta sozinho
	// sem a confirmacao de um login novo (zero: nada pendente).
	ReloadedAt time.Time `json:"reloaded_at,omitzero"`
	RollbackAt time.Time `json:"rollback_at,omitzero"`
	Confirmed  bool      `json:"confirmed,omitempty"`
	// RolledBack: ninguem confirmou e o SSH voltou ao que era.
	RolledBack bool `json:"rolled_back,omitempty"`
	// Fixing e o item da checagem sendo corrigido sozinho.
	Fixing string `json:"fixing,omitempty"`
}

// Pending diz se o SSH espera a confirmacao de um login novo.
func (h Hardening) Pending() bool { return !h.RollbackAt.IsZero() }

// rollbackGrace e a folga depois da hora da volta antes de conferir o que
// aconteceu (o timer do systemd roda o desfazer).
var rollbackGrace = 15 * time.Second

// HardenHost prepara a VPS (host.Harden): admin com chave, firewall,
// fail2ban, atualizacoes e o SSH sem root e sem senha. O progresso fica em
// State.Hardening. Continua mesmo se o cliente cair (apt no meio).
func (c *Core) HardenHost(ctx context.Context, req HardenRequest) error {
	if !c.infra.TryLock() {
		return ErrBusy
	}
	defer c.infra.Unlock()
	ctx = context.WithoutCancel(ctx)

	start := c.deps.Now()
	c.update(func(s *State) {
		s.Hardening = Hardening{Running: true, User: req.User, StartedAt: start}
	})
	res, err := c.deps.HardenHost(ctx, host.Options{User: req.User, Password: req.Password}, func(st host.Step) {
		c.update(func(s *State) {
			steps := s.Hardening.Steps
			for i := range steps {
				if steps[i].ID == st.ID {
					steps[i] = st
					return
				}
			}
			s.Hardening.Steps = append(steps, st)
		})
	})
	now := c.deps.Now()
	s := c.update(func(s *State) {
		h := &s.Hardening
		h.Running, h.FinishedAt = false, now
		if err != nil {
			h.Error = err.Error()
		}
		if res.PendingConfirm {
			h.ReloadedAt, h.RollbackAt = now, now.Add(c.deps.RollbackDelay)
		}
	})
	if s.Hardening.Pending() {
		at := s.Hardening.RollbackAt
		go c.watchRollback(at, at.Sub(now)+rollbackGrace)
	}
	c.CheckEnvironment(ctx)
	if err != nil {
		c.deps.Logger.Warn("preparar a VPS falhou", "erro", err)
	} else {
		c.deps.Logger.Info("VPS preparada", "admin", req.User, "ssh_pendente", res.PendingConfirm)
	}
	return err
}

// watchRollback confere, passada a hora da volta, se o SSH ficou (alguem
// confirmou, talvez pelo CLI) ou voltou ao que era.
func (c *Core) watchRollback(at time.Time, wait time.Duration) {
	time.Sleep(wait)
	hardened := c.deps.SSHHardened()
	changed := false
	c.update(func(s *State) {
		h := &s.Hardening
		if !h.RollbackAt.Equal(at) {
			return // confirmado (ou outro harden) nesse meio tempo
		}
		changed = true
		h.RollbackAt = time.Time{}
		if hardened {
			h.Confirmed = true
		} else {
			h.RolledBack = true
		}
	})
	if changed {
		c.CheckEnvironment(context.Background())
	}
}

// ConfirmHost mantem o SSH do Preparar VPS. since e quando a sessao que
// confirma comecou: so uma aberta depois da mudanca prova que o login por
// chave funciona.
func (c *Core) ConfirmHost(ctx context.Context, since time.Time) error {
	st, _ := c.Snapshot(ctx)
	h := st.Hardening
	if !h.Pending() {
		return errors.New("nada pendente: o SSH nao esta esperando confirmacao")
	}
	if !since.After(h.ReloadedAt) {
		return errors.New("esta janela foi aberta antes da mudanca no SSH e nao prova que o login novo funciona: " +
			"abra outra (ssh cais@<vps> ou ssh " + h.User + "@<vps>) e confirme por ela")
	}
	ok, err := c.deps.ConfirmSSH(ctx)
	if err != nil {
		return err
	}
	hardened := ok || c.deps.SSHHardened()
	c.update(func(s *State) {
		s.Hardening.RollbackAt = time.Time{}
		s.Hardening.Confirmed = hardened
		s.Hardening.RolledBack = !hardened
	})
	if !hardened {
		return errors.New("tarde demais: o SSH ja voltou ao que era; rode o Preparar VPS de novo")
	}
	return nil
}

// FixHostItem corrige um item da checagem de seguranca sozinho (firewall,
// fail2ban, atualizacoes, logs do Docker, chave do cais, reinicio) e confere
// de novo. Fora do MCP: a IA nao reinicia a VPS. Devolve o
// detalhe do que fez.
func (c *Core) FixHostItem(ctx context.Context, id string) (string, error) {
	if !c.infra.TryLock() {
		return "", ErrBusy
	}
	defer c.infra.Unlock()
	ctx = context.WithoutCancel(ctx)
	c.update(func(s *State) { s.Hardening.Fixing = id })
	detail, err := c.deps.FixHost(ctx, id)
	c.update(func(s *State) { s.Hardening.Fixing = "" })
	c.CheckEnvironment(ctx)
	return detail, err
}
