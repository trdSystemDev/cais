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
	// Kind e o que mudou no SSH e espera a confirmacao: vazio e o Preparar
	// VPS; KindSSHTailnet e o Fechar o SSH publico (so pela tailnet).
	Kind string `json:"kind,omitempty"`
}

// KindSSHTailnet e o Fechar o SSH publico.
const KindSSHTailnet = "ssh-tailnet"

// kept diz se a mudanca no SSH ficou (nao voltou sozinha).
func (c *Core) kept(kind string) bool {
	if kind == KindSSHTailnet {
		return c.deps.SSHTailnetOnly()
	}
	return c.deps.SSHHardened()
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
	st, _ := c.Snapshot(context.Background())
	hardened := c.kept(st.Hardening.Kind)
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
	hardened := ok || c.kept(h.Kind)
	c.update(func(s *State) {
		s.Hardening.RollbackAt = time.Time{}
		s.Hardening.Confirmed = hardened
		s.Hardening.RolledBack = !hardened
	})
	if !hardened {
		if h.Kind == KindSSHTailnet {
			return errors.New("tarde demais: o SSH ja abriu de novo pra internet; feche de novo pela tailnet")
		}
		return errors.New("tarde demais: o SSH ja voltou ao que era; rode o Preparar VPS de novo")
	}
	return nil
}

// ConnectTailscale poe a VPS na tailnet: instala o Tailscale se faltar e
// devolve o link de login (AuthURL) pra autorizar no navegador; ja
// conectada, devolve o status. Fora do MCP.
func (c *Core) ConnectTailscale(ctx context.Context) (host.Tailscale, error) {
	if !c.infra.TryLock() {
		return host.Tailscale{}, ErrBusy
	}
	defer c.infra.Unlock()
	ctx = context.WithoutCancel(ctx)
	c.update(func(s *State) { s.Hardening.Fixing = "tailscale" })
	ts, err := c.deps.ConnectTailscale(ctx)
	c.update(func(s *State) { s.Hardening.Fixing = "" })
	c.CheckEnvironment(ctx)
	if err != nil {
		c.deps.Logger.Warn("tailscale: conectar falhou", "erro", err)
	} else {
		c.deps.Logger.Info("tailscale", "estado", ts.State, "nome", ts.Name)
	}
	return ts, err
}

// CloseSSHPublic fecha o SSH pra internet (so pela tailnet; de fora, so 80
// e 443). Como o Preparar VPS: volta sozinho em RollbackDelay sem a
// confirmacao de uma sessao aberta depois (que so pode ter vindo pela
// tailnet). Fora do MCP.
func (c *Core) CloseSSHPublic(ctx context.Context) error {
	if !c.infra.TryLock() {
		return ErrBusy
	}
	defer c.infra.Unlock()
	ctx = context.WithoutCancel(ctx)
	res, err := c.deps.CloseSSH(ctx)
	now := c.deps.Now()
	if err == nil && res.PendingConfirm {
		s := c.update(func(s *State) {
			s.Hardening = Hardening{Kind: KindSSHTailnet, StartedAt: now, FinishedAt: now,
				ReloadedAt: now, RollbackAt: now.Add(c.deps.RollbackDelay)}
		})
		at := s.Hardening.RollbackAt
		go c.watchRollback(at, at.Sub(now)+rollbackGrace)
		c.deps.Logger.Info("ssh fechado pra internet, esperando a confirmacao pela tailnet")
	}
	c.CheckEnvironment(ctx)
	return err
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
