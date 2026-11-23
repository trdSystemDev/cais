package ui

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/github"
)

func (p *appsPage) checkCmd() tea.Cmd {
	return func() tea.Msg {
		p.api.CheckApps(context.Background())
		return nil
	}
}

// do roda uma acao do core fora do Update e devolve o resultado.
func (p *appsPage) do(app, label, ok string, fn func(ctx context.Context) error) tea.Cmd {
	if p.busy != "" {
		return nil
	}
	p.busy = label
	p.detailErr = ""
	p.setStatus("warn", label+"...")
	return func() tea.Msg {
		return appActionMsg{app: app, ok: ok, err: fn(context.Background())}
	}
}

func (p *appsPage) deploy() tea.Cmd {
	a := p.selected()
	if a == nil || p.busy != "" {
		return nil
	}
	name, ref := a.Name, strings.TrimSpace(p.ref.Value())
	p.busy = "deploy"
	p.detailErr = ""
	p.setStatus("warn", "Deploy de "+name+": baixando, build (a versao atual continua no ar) e up...")
	return func() tea.Msg {
		res, err := p.api.DeployApp(context.Background(), name, ref)
		return appActionMsg{app: name, err: err, deployed: &res}
	}
}

// rollback volta o app pro commit do historico; o primeiro aperto so arma.
func (p *appsPage) rollback(commit string) tea.Cmd {
	a := p.selected()
	if a == nil || p.busy != "" {
		return nil
	}
	if p.rollbackArm != commit {
		p.rollbackArm = commit
		p.setStatus("warn", "Voltar "+a.Name+" pra "+shortCommit(commit)+"? Aperte de novo pra confirmar (variaveis e secrets sao os de agora).")
		p.rebuild()
		return nil
	}
	p.rollbackArm = ""
	name := a.Name
	p.busy = "deploy"
	p.detailErr = ""
	p.setStatus("warn", "Voltando "+name+" pra "+shortCommit(commit)+" (imagens guardadas, sem queda)...")
	return func() tea.Msg {
		res, err := p.api.RollbackApp(context.Background(), name, commit)
		return appActionMsg{app: name, err: err, deployed: &res, rollback: true}
	}
}

func (p *appsPage) restart() tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	name := a.Name
	return p.do(name, "Reiniciando "+name, name+" reiniciado.", func(ctx context.Context) error {
		return p.api.RestartApp(ctx, name, "")
	})
}

func (p *appsPage) stop() tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	name := a.Name
	return p.do(name, "Parando "+name, name+" parado (Reiniciar volta).", func(ctx context.Context) error {
		return p.api.StopApp(ctx, name)
	})
}

func (p *appsPage) deleteApp() tea.Cmd {
	a := p.selected()
	if a == nil || strings.TrimSpace(p.delConfirm.Value()) != a.Name {
		return nil
	}
	name, volumes := a.Name, p.delVolumes
	msg := name + " excluido (o database da infra fica)."
	if volumes {
		msg = name + " excluido, com os volumes (o database da infra fica)."
	}
	return p.do(name, "Excluindo "+name, msg, func(ctx context.Context) error {
		return p.api.DeleteApp(ctx, name, volumes)
	})
}

func (p *appsPage) saveApp() tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	repo, err := github.ParseRepo(p.repo.Value())
	if err != nil {
		p.setStatus("danger", err.Error())
		return nil
	}
	upd := a.App // mantem a producao (homologacao) e o deploy automatico
	upd.Repo, upd.Branch, upd.Compose = repo.String(), strings.TrimSpace(p.branch.Value()), strings.TrimSpace(p.compose.Value())
	return p.do(a.Name, "Salvando", a.Name+" salvo; vale no proximo deploy.", func(ctx context.Context) error {
		return p.api.UpdateApp(ctx, upd)
	})
}

// toggleAuto liga ou desliga o deploy automatico do app (grava na hora).
func (p *appsPage) toggleAuto() tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	upd := a.App
	upd.AutoDeploy = !upd.AutoDeploy
	ok := "Deploy de " + a.Name + " manual: so pelo botao (ou CLI/MCP)."
	if upd.AutoDeploy {
		ok = "Deploy automatico em " + a.Name + ": cada commit novo na " + branchName(a.Branch) + " sobe sozinho (consulta a cada minuto)."
	}
	return p.do(a.Name, "Salvando", ok, func(ctx context.Context) error { return p.api.UpdateApp(ctx, upd) })
}

func branchName(b string) string {
	if b == "" {
		return "branch principal"
	}
	return b
}

// promote sobe na producao o commit validado na homologacao from.
func (p *appsPage) promote(from string, confirm bool) tea.Cmd {
	r, ok := p.promos[from]
	if !ok || p.busy != "" {
		return nil
	}
	to := r.p.To
	p.busy = "deploy"
	p.detailErr = ""
	p.setStatus("warn", "Promovendo "+shortCommit(r.p.Deploy)+" pra "+to+": build (a versao atual continua no ar) e up...")
	return func() tea.Msg {
		res, err := p.api.PromoteApp(context.Background(), from, confirm)
		return appActionMsg{app: to, err: err, deployed: &res}
	}
}

// startHomolog abre o cadastro de uma homologacao do app: mesmo
// repositorio, ligada a ele.
func (p *appsPage) startHomolog() tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	prod := *a
	cmd := p.startCreate()
	p.newName.SetValue(prod.Name + "-homolog")
	p.newRepo.SetValue(prod.Repo)
	p.newPromoteTo = prod.Name
	p.rebuild()
	return tea.Batch(cmd, p.setFocus(2)) // a branch
}

func (p *appsPage) saveEnv() tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	env := map[string]string{}
	for _, r := range p.envRows {
		k := strings.TrimSpace(r.name.Value())
		if k == "" {
			continue
		}
		if _, dup := env[k]; dup {
			p.setStatus("danger", "variavel "+k+" repetida")
			return nil
		}
		env[k] = r.value.Value()
	}
	name := a.Name
	p.envPending = env
	return p.do(name, "Salvando variaveis", "Variaveis de "+name+" salvas; valem no proximo deploy.", func(ctx context.Context) error {
		return p.api.SetAppEnv(ctx, name, env)
	})
}

// envNow sao as variaveis como estao nos editores (linhas sem nome ficam fora).
func (p *appsPage) envNow() map[string]string {
	env := map[string]string{}
	for _, r := range p.envRows {
		if k := strings.TrimSpace(r.name.Value()); k != "" {
			env[k] = r.value.Value()
		}
	}
	return env
}

func (p *appsPage) envDirty() bool {
	return p.envSaved != nil && !maps.Equal(p.envNow(), p.envSaved)
}

func (p *appsPage) closeImport() { p.importing, p.importText, p.importErr = false, "", "" }

// applyImport junta o .env colado nos editores: variavel que ja existe
// recebe o valor novo, as outras entram no fim. So grava no Salvar.
func (p *appsPage) applyImport() tea.Cmd {
	env, err := apps.ParseEnv([]byte(p.importText))
	if err != nil {
		p.importErr = err.Error()
		return nil
	}
	keys := slices.Sorted(maps.Keys(env))
	changed, added := 0, 0
	for _, k := range keys {
		i := slices.IndexFunc(p.envRows, func(r *envRow) bool { return strings.TrimSpace(r.name.Value()) == k })
		if i >= 0 {
			if p.envRows[i].value.Value() != env[k] {
				p.envRows[i].value.SetValue(env[k])
				p.envRows[i].value.CursorStart()
				changed++
			}
			continue
		}
		p.envRows = append(p.envRows, p.newEnvRow(k, env[k]))
		added++
	}
	p.closeImport()
	p.rebuild()
	p.setStatus("warn", fmt.Sprintf("Importadas: %d novas, %d com valor trocado. Confira e clique Salvar.", added, changed))
	return p.setFocus(slices.IndexFunc(p.fields, func(f *field) bool { return f.id == "env-save" }))
}

func (p *appsPage) saveSecret() tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	secret, value := strings.TrimSpace(p.secretName.Value()), p.secretValue.Value()
	if secret == "" || value == "" {
		p.setStatus("danger", "Informe o nome e o valor do secret.")
		return nil
	}
	name := a.Name
	return p.do(name, "Gravando "+secret, secret+" gravado em ${CAIS_SECRETS}/"+secret+" (vale no proximo deploy).", func(ctx context.Context) error {
		return p.api.SetAppSecret(ctx, name, secret, value)
	})
}

func (p *appsPage) linkDB() tea.Cmd {
	a := p.selected()
	list := p.linkable()
	if a == nil || len(list) == 0 {
		return nil
	}
	d := list[min(p.dbPick, len(list)-1)]
	name := a.Name
	return p.do(name, "Ligando "+d.Engine+"/"+d.Name, name+" ligado a "+d.Engine+"/"+d.Name+": os secrets valem no proximo deploy.", func(ctx context.Context) error {
		return p.api.LinkAppDatabase(ctx, name, d.Engine, d.Name)
	})
}

func (p *appsPage) linkBucket() tea.Cmd {
	a := p.selected()
	list := p.linkableBuckets()
	if a == nil || len(list) == 0 {
		return nil
	}
	bk := list[min(p.bucketPick, len(list)-1)]
	name := a.Name
	return p.do(name, "Ligando o bucket "+bk.Name, name+" ligado ao bucket "+bk.Name+": os secrets valem no proximo deploy.", func(ctx context.Context) error {
		return p.api.LinkAppBucket(ctx, name, bk.Name)
	})
}

func (p *appsPage) unlinkBucket(l apps.BucketLink) tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	name := a.Name
	return p.do(name, "Desligando o bucket "+l.Name, name+" desligado do bucket "+l.Name+" (o bucket e os arquivos ficam).", func(ctx context.Context) error {
		return p.api.UnlinkAppBucket(ctx, name, l.Name)
	})
}

func (p *appsPage) unlinkDB(l apps.DBLink) tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	name := a.Name
	return p.do(name, "Desligando "+l.ID(), name+" desligado de "+l.ID()+" (o database e os dados ficam).", func(ctx context.Context) error {
		return p.api.UnlinkAppDatabase(ctx, name, l.Engine, l.Name)
	})
}

func (p *appsPage) deleteSecret(secret string) tea.Cmd {
	a := p.selected()
	if a == nil {
		return nil
	}
	name := a.Name
	return p.do(name, "Apagando "+secret, secret+" apagado.", func(ctx context.Context) error {
		return p.api.DeleteAppSecret(ctx, name, secret)
	})
}

func (p *appsPage) startCreate() tea.Cmd {
	p.creating, p.inForm, p.focus = true, true, 0
	p.detailErr = ""
	for _, ti := range []*textinput.Model{p.newName, p.newRepo, p.newBranch, p.newCompose} {
		ti.SetValue("")
	}
	p.newPromoteTo, p.newAuto = "", false
	p.setStatus("", "")
	p.rebuild()
	return p.setFocus(0)
}

func (p *appsPage) cancelCreate() tea.Cmd {
	p.creating, p.inForm = false, false
	p.detailErr = ""
	p.setStatus("", "")
	return tea.Batch(p.loadSelected(true), p.setFocus(0))
}

// create cadastra o app. O nome vazio vira o nome do repositorio.
func (p *appsPage) create() tea.Cmd {
	repo, err := github.ParseRepo(p.newRepo.Value())
	if err != nil {
		p.setStatus("danger", err.Error())
		return nil
	}
	a := apps.App{Name: strings.TrimSpace(p.newName.Value()), Repo: repo.String(),
		Branch: strings.TrimSpace(p.newBranch.Value()), Compose: strings.TrimSpace(p.newCompose.Value()),
		PromoteTo: p.newPromoteTo, AutoDeploy: p.newAuto}
	if a.Name == "" {
		a.Name = strings.ToLower(repo.Name)
	}
	p.busy = "Criando"
	p.detailErr = ""
	p.setStatus("warn", "Criando "+a.Name+"...")
	return func() tea.Msg {
		out, err := p.api.CreateApp(context.Background(), a)
		return appCreatedMsg{out, err}
	}
}
