package apps

import (
	"context"
	"errors"
	"fmt"
)

// Estados de uma promocao (Promotion.State).
const (
	PromoteReady   = "ready"   // pode promover
	PromoteSame    = "same"    // a producao ja esta nesse codigo
	PromoteOlder   = "older"   // voltaria a producao pra um commit mais velho: pede confirmacao
	PromoteBlocked = "blocked" // nao pode (Reason diz o que fazer)
)

// ErrConfirm e a promocao que volta a producao pra um commit mais velho
// (ou fora da linha dela): so com confirmacao.
var ErrConfirm = errors.New("confirme pra promover")

// Promotion e o que promover a homologacao faria agora, com o motivo
// quando nao pode.
type Promotion struct {
	From string `json:"from"` // homologacao
	To   string `json:"to"`   // producao
	// Commit e o commit no ar na homologacao: o validado.
	Commit string `json:"commit"`
	// Deploy e o commit que sobe na producao: o validado, ou a ponta da
	// branch da producao quando ela tem o mesmo codigo (squash merge).
	Deploy string `json:"deploy,omitempty"`
	// Current e o commit no ar na producao.
	Current string `json:"current,omitempty"`
	// Branch e a branch da producao (vazia: a principal do repositorio).
	Branch string `json:"branch,omitempty"`
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	// Ahead e quantos commits a producao ganha.
	Ahead int `json:"ahead,omitempty"`
	// Unvalidated e quantos commits a branch da producao tem depois do
	// validado: um deploy direto da branch subiria codigo que nao passou
	// pela homologacao.
	Unvalidated int `json:"unvalidated,omitempty"`
}

// CheckPromotion diz se o commit no ar na homologacao pode ir pra producao.
// A regra vale pra mesma branch ou branches diferentes: o commit validado
// tem que estar no historico da branch da producao (senao um deploy futuro
// da branch desfaria a promocao), ou ter o mesmo codigo da ponta dela
// (squash merge).
func (m Manager) CheckPromotion(ctx context.Context, from string) (Promotion, error) {
	a, err := m.Get(from)
	if err != nil {
		return Promotion{}, err
	}
	if a.PromoteTo == "" {
		return Promotion{}, fmt.Errorf("%s nao e homologacao de nenhum app (a homologacao e outro app, com promote_to apontando pra producao)", from)
	}
	prod, err := m.Get(a.PromoteTo)
	if err != nil {
		return Promotion{}, fmt.Errorf("producao %s: %w", a.PromoteTo, err)
	}
	p := Promotion{From: from, To: prod.Name, Commit: a.Commit, Current: prod.Commit, Branch: prod.Branch}
	blocked := func(format string, args ...any) (Promotion, error) {
		p.State, p.Reason, p.Deploy = PromoteBlocked, fmt.Sprintf(format, args...), ""
		return p, nil
	}
	branch := prod.Branch
	if branch == "" {
		branch = "branch principal"
	}

	if a.Commit == "" {
		return blocked("a homologacao ainda nao teve deploy: faca o deploy em %s e valide", from)
	}
	if l := a.Last; l != nil && l.Error != "" && l.Commit != a.Commit {
		return blocked("o ultimo deploy de %s (%s) falhou e o que esta no ar e o anterior (%s): corrija e faca o deploy de novo na homologacao",
			from, short(l.Commit), short(a.Commit))
	}
	if err := m.healthy(ctx, a); err != nil {
		return blocked("%v: so promove o que esta no ar e de pe na homologacao", err)
	}

	head, err := m.Source.Resolve(ctx, prod.Repo, prod.Branch)
	if err != nil {
		return p, err
	}
	in, err := m.Source.Compare(ctx, prod.Repo, head, a.Commit)
	if err != nil {
		return p, err
	}
	switch {
	case in.InHistory():
		p.Deploy, p.Unvalidated = a.Commit, in.BehindBy
	default:
		same, err := m.sameTree(ctx, prod.Repo, a.Commit, head)
		if err != nil {
			return p, err
		}
		if !same {
			return blocked("o commit %s da homologacao ainda nao esta na %s da producao: faca o merge (PR) na %s e promova de novo",
				short(a.Commit), branch, branch)
		}
		p.Deploy = head // squash: o mesmo codigo, com outro commit
	}

	if prod.Commit == "" {
		p.State = PromoteReady
		return p, nil
	}
	if p.Deploy == prod.Commit {
		p.State, p.Reason = PromoteSame, fmt.Sprintf("a producao ja esta no commit %s", short(prod.Commit))
		return p, nil
	}
	rel, err := m.Source.Compare(ctx, prod.Repo, prod.Commit, p.Deploy)
	if err != nil {
		return p, err
	}
	switch rel.Status {
	case "ahead":
		p.State, p.Ahead = PromoteReady, rel.AheadBy
	case "identical":
		p.State, p.Reason = PromoteSame, fmt.Sprintf("a producao ja esta no commit %s", short(prod.Commit))
	case "behind":
		p.State, p.Reason = PromoteOlder, fmt.Sprintf("o commit %s e %d commit(s) mais velho que o da producao (%s): promover volta a producao",
			short(p.Deploy), rel.BehindBy, short(prod.Commit))
	default:
		p.State, p.Reason = PromoteOlder, fmt.Sprintf("o commit %s nao vem depois do que esta na producao (%s): a producao perderia o que so ela tem",
			short(p.Deploy), short(prod.Commit))
	}
	return p, nil
}

// Promote sobe na producao o commit validado na homologacao, pelo deploy
// normal (build com a versao no ar, volta automatica se falhar). Voltar a
// producao pra um commit mais velho so com confirm.
func (m Manager) Promote(ctx context.Context, from string, confirm bool) (DeployResult, error) {
	p, err := m.CheckPromotion(ctx, from)
	if err != nil {
		return DeployResult{}, err
	}
	switch p.State {
	case PromoteBlocked:
		return DeployResult{}, errors.New(p.Reason)
	case PromoteSame:
		return DeployResult{}, errors.New("nada a promover: " + p.Reason)
	case PromoteOlder:
		if !confirm {
			return DeployResult{}, fmt.Errorf("%s. %w", p.Reason, ErrConfirm)
		}
	}
	return m.deploy(ctx, p.To, p.Deploy, Deploy{PromotedFrom: from})
}

// sameTree diz se os dois commits tem o mesmo codigo.
func (m Manager) sameTree(ctx context.Context, repo, a, b string) (bool, error) {
	ta, err := m.Source.Tree(ctx, repo, a)
	if err != nil {
		return false, err
	}
	tb, err := m.Source.Tree(ctx, repo, b)
	if err != nil {
		return false, err
	}
	return ta == tb, nil
}

// healthy diz se o app esta no ar: algum container rodando e nenhum
// reiniciando, parado com erro ou unhealthy.
func (m Manager) healthy(ctx context.Context, a App) error {
	cs, err := m.Docker.PS(ctx, m.project(a, SrcDir))
	if err != nil {
		return err
	}
	running := false
	for _, c := range cs {
		switch {
		case c.State == "restarting" || c.Health == "unhealthy" ||
			(c.State == "exited" || c.State == "dead") && c.ExitCode != 0:
			return fmt.Errorf("o container %s da homologacao nao esta bem (%s)", c.Name, c.Status)
		case c.State == "running":
			running = true
		}
	}
	if !running {
		return fmt.Errorf("a homologacao %s esta parada", a.Name)
	}
	return nil
}
