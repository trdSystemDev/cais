package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/secrets"
	"github.com/trdSystemDev/cais/internal/stacks"
)

// fakeDocker registra o que o Setup pediu ao docker.
type fakeDocker struct {
	mu            sync.Mutex
	calls         []string // "up <stack>" e "down <stack>", na ordem
	downs         []string
	ups           []string // nome da stack (pasta do compose)
	recreate      []string
	logins        []string
	failUp        string // stack cujo up falha
	execs         []string
	layoutVersion int
	loginErr      int // quantos logins falham antes de passar
}

func (f *fakeDocker) up(_ context.Context, file string, recreate bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := filepath.Base(filepath.Dir(file))
	if _, err := os.Stat(file); err != nil {
		return err // o compose tem que estar gravado antes do up
	}
	f.ups = append(f.ups, name)
	f.calls = append(f.calls, "up "+name)
	if recreate {
		f.recreate = append(f.recreate, name)
	}
	if name == f.failUp {
		return errors.New("container unhealthy")
	}
	return nil
}

func (f *fakeDocker) down(_ context.Context, file string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := os.Stat(file); err != nil {
		return err // derruba pelo compose que estava no ar
	}
	name := filepath.Base(filepath.Dir(file))
	f.downs = append(f.downs, name)
	f.calls = append(f.calls, "down "+name)
	return nil
}

// exec finge o CLI do Garage: layout na versao 0 ate o apply.
func (f *fakeDocker) exec(_ context.Context, container string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execs = append(f.execs, container+" "+strings.Join(args, " "))
	switch strings.Join(args[1:], " ") {
	case "layout show":
		return fmt.Sprintf("==== CURRENT CLUSTER LAYOUT ====\nCurrent cluster layout version: %d\n", f.layoutVersion), nil
	case "node id -q":
		return "137b3cac6a9a3725@127.0.0.1:3901\n", nil
	case "layout apply --version 1":
		if f.layoutVersion != 0 {
			return "", errors.New("Invalid new layout version")
		}
		f.layoutVersion = 1
	}
	return "", nil
}

func (f *fakeDocker) login(_ context.Context, host, user string, pw []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logins = append(f.logins, host+" "+user)
	if f.loginErr > 0 {
		f.loginErr--
		return errors.New("x509: certificate is not valid")
	}
	return nil
}

func newDeps(t *testing.T, f *fakeDocker) Deps {
	root := t.TempDir()
	return Deps{
		ConfigPath:    filepath.Join(root, "etc", "config.yaml"),
		SecretsDir:    filepath.Join(root, "data", "secrets"),
		StackDir:      func(n string) string { return filepath.Join(root, "data", "stacks", n) },
		CheckDocker:   func(context.Context) error { return nil },
		ComposeUp:     f.up,
		ComposeDown:   f.down,
		Exec:          f.exec,
		Login:         f.login,
		LoginAttempts: 3,
	}
}

func writeConfig(t *testing.T, d Deps, mutate func(*config.Config)) config.Config {
	t.Helper()
	c := config.Default()
	c.Domain = "exemplo.com.br"
	c.Email = "admin@exemplo.com.br"
	c.Woodpecker = config.Woodpecker{GitHubClient: "Ov23liAbc", Admin: "fulano"}
	c.Stacks = []string{"postgres", "mysql"}
	if mutate != nil {
		mutate(&c)
	}
	if err := config.Save(d.ConfigPath, c); err != nil {
		t.Fatal(err)
	}
	return c
}

func provideGitHub(t *testing.T, d Deps) {
	t.Helper()
	if err := stacks.SetProvided(d.store(), "github_client_secret", []byte("segredo")); err != nil {
		t.Fatal(err)
	}
}

func TestPlanSemConfig(t *testing.T) {
	d := newDeps(t, &fakeDocker{})
	p, err := MakePlan(d)
	if err != nil {
		t.Fatal(err)
	}
	if p.Configured || p.Ready() || len(p.Problems) == 0 {
		t.Fatalf("plano sem config tem que pedir o Setup: %+v", p)
	}
	if _, err := Run(context.Background(), d, nil); err == nil {
		t.Fatal("rodou sem config")
	}
}

func TestPlanPedeSecretInformadoENaoGrava(t *testing.T) {
	f := &fakeDocker{}
	d := newDeps(t, f)
	writeConfig(t, d, nil)

	p, err := MakePlan(d)
	if err != nil {
		t.Fatal(err)
	}
	if p.Ready() || len(p.Missing) != 1 || p.Missing[0].Name != "github_client_secret" {
		t.Fatalf("esperava faltar so o github_client_secret: %+v", p)
	}
	if len(p.Stacks) != 5 || p.Stacks[0].Change != ChangeNew {
		t.Fatalf("stacks do plano: %+v", p.Stacks)
	}
	if _, err := os.Stat(filepath.Dir(d.SecretsDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("o plano gravou algo no disco")
	}

	_, err = Run(context.Background(), d, nil)
	if err == nil || !strings.Contains(err.Error(), "github_client_secret") {
		t.Fatalf("Run sem o secret informado: %v", err)
	}
	if len(f.ups) != 0 {
		t.Fatal("subiu stack sem estar pronto")
	}
}

func TestPlanDizComoChegarNosBancos(t *testing.T) {
	d := newDeps(t, &fakeDocker{})
	writeConfig(t, d, func(c *config.Config) { c.Stacks = []string{"proxy", "mysql"} })
	p, err := MakePlan(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Databases) != 1 || p.Databases[0].Name != "MySQL" || p.Databases[0].Port != 3306 || p.Databases[0].User != "root" {
		t.Fatalf("Databases = %+v", p.Databases)
	}
	if p.Databases[0].PasswordFile != filepath.Join(d.SecretsDir, "mysql_root_password") {
		t.Errorf("PasswordFile = %s", p.Databases[0].PasswordFile)
	}
	if p.Tunnel != "ssh -N -L 3306:127.0.0.1:3306 <usuario>@<vps>" {
		t.Errorf("Tunnel = %q", p.Tunnel)
	}
}

func TestRunSobeTudoEmOrdem(t *testing.T) {
	f := &fakeDocker{loginErr: 1} // certificado ainda saindo no primeiro login
	d := newDeps(t, f)
	writeConfig(t, d, nil)
	provideGitHub(t, d)

	var reports [][]Step
	steps, err := Run(context.Background(), d, func(s []Step) { reports = append(reports, s) })
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.ups, []string{"proxy", "postgres", "mysql", "registry", "woodpecker"}) {
		t.Errorf("ordem de subida %v", f.ups)
	}
	if !slices.Equal(f.recreate, []string{"registry"}) {
		t.Errorf("htpasswd novo tem que recriar so o registry: %v", f.recreate)
	}
	if len(f.logins) != 2 || f.logins[1] != "registry.exemplo.com.br vps" {
		t.Errorf("logins %v", f.logins)
	}
	for _, s := range steps {
		if s.Status != Done {
			t.Errorf("passo %s ficou %s", s.ID, s.Status)
		}
	}
	// cada passo passa por running antes de done
	if len(reports) != 2*len(steps) {
		t.Errorf("%d relatorios pra %d passos", len(reports), len(steps))
	}

	compose, err := os.ReadFile(d.ComposeFile("postgres"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(compose), d.SecretsDir+"/postgres_password") {
		t.Error("compose gravado nao aponta pra pasta de secrets")
	}
}

func TestRunDeNovoNaoMudaNada(t *testing.T) {
	f := &fakeDocker{}
	d := newDeps(t, f)
	writeConfig(t, d, nil)
	provideGitHub(t, d)
	if _, err := Run(context.Background(), d, nil); err != nil {
		t.Fatal(err)
	}
	pw, _ := d.store().Get("postgres_password")

	p, err := MakePlan(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, sp := range p.Stacks {
		if sp.Change != ChangeUnchanged {
			t.Errorf("stack %s %s na segunda vez", sp.Name, sp.Change)
		}
	}
	if len(p.CreateSecrets) != 0 {
		t.Errorf("segunda vez criaria %v", p.CreateSecrets)
	}

	f.recreate = nil
	steps, err := Run(context.Background(), d, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.recreate) != 0 {
		t.Errorf("recriou %v sem nada mudar", f.recreate)
	}
	if again, _ := d.store().Get("postgres_password"); string(again) != string(pw) {
		t.Error("senha do postgres mudou")
	}
	if steps[1].Detail != "nenhum novo" || steps[2].Detail != "nenhum mudou" {
		t.Errorf("detalhes: %q %q", steps[1].Detail, steps[2].Detail)
	}

	// mudar a config so regrava a stack afetada
	writeConfig(t, d, func(c *config.Config) { c.Hosts.CI = "build.exemplo.com.br" })
	steps, _ = Run(context.Background(), d, nil)
	if steps[2].Detail != "woodpecker" {
		t.Errorf("regravou %q, esperava so woodpecker", steps[2].Detail)
	}
}

func TestRunParaNoPrimeiroErro(t *testing.T) {
	f := &fakeDocker{failUp: "registry"}
	d := newDeps(t, f)
	writeConfig(t, d, nil)
	provideGitHub(t, d)

	steps, err := Run(context.Background(), d, nil)
	if err == nil || !strings.Contains(err.Error(), "unhealthy") {
		t.Fatalf("erro %v", err)
	}
	if !slices.Equal(f.ups, []string{"proxy", "postgres", "mysql", "registry"}) {
		t.Errorf("continuou depois do erro: %v", f.ups)
	}
	status := map[string]Status{}
	for _, s := range steps {
		status[s.ID] = s.Status
	}
	if status["stack:registry"] != Failed || status["stack:woodpecker"] != Pending || status[stepRegistryLogin] != Pending {
		t.Errorf("status %v", status)
	}
}

func TestRunLoginDesisteComDica(t *testing.T) {
	f := &fakeDocker{loginErr: 10}
	d := newDeps(t, f)
	writeConfig(t, d, nil)
	provideGitHub(t, d)

	_, err := Run(context.Background(), d, nil)
	if err == nil || !strings.Contains(err.Error(), "DNS de registry.exemplo.com.br") {
		t.Fatalf("erro %v", err)
	}
	if len(f.logins) != d.LoginAttempts {
		t.Errorf("%d tentativas, esperava %d", len(f.logins), d.LoginAttempts)
	}
}

// sem nenhum servico opcional, as fixas sobem do mesmo jeito
func TestFixasSobemSempre(t *testing.T) {
	f := &fakeDocker{}
	d := newDeps(t, f)
	writeConfig(t, d, func(c *config.Config) { c.Stacks = nil })
	provideGitHub(t, d)

	if _, err := Run(context.Background(), d, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.ups, []string{"proxy", "registry", "woodpecker"}) {
		t.Errorf("subiu %v", f.ups)
	}
	if len(f.logins) != 1 {
		t.Errorf("login da VPS no registry: %v", f.logins)
	}
	if ok, _ := secrets.Exists(d.store(), "postgres_password"); ok {
		t.Error("gerou senha de banco desligado")
	}
}

func TestGarageLayoutSoNaPrimeiraVez(t *testing.T) {
	f := &fakeDocker{}
	d := newDeps(t, f)
	writeConfig(t, d, func(c *config.Config) { c.Stacks = []string{"garage"} })
	provideGitHub(t, d)

	steps, err := Run(context.Background(), d, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.layoutVersion != 1 {
		t.Fatal("layout do Garage nao foi aplicado")
	}
	if !slices.Contains(f.execs, "garage /garage layout assign -z dc1 -c 100G 137b3cac6a9a3725") {
		t.Errorf("assign: %v", f.execs)
	}
	i := slices.IndexFunc(steps, func(s Step) bool { return s.ID == stepGarageLayout })
	if i < 0 || steps[i].Detail != "no unico configurado" {
		t.Fatalf("passo do layout: %+v", steps)
	}
	// o Garage sobe antes do layout
	if slices.Index(f.ups, "garage") < 0 {
		t.Errorf("garage nao subiu: %v", f.ups)
	}

	// de novo: reaplicar daria erro no Garage, entao nao reaplica
	f.execs = nil
	steps, err = Run(context.Background(), d, nil)
	if err != nil {
		t.Fatal(err)
	}
	if steps[i].Detail != "ja preparado" || slices.ContainsFunc(f.execs, func(e string) bool { return strings.Contains(e, "apply") }) {
		t.Errorf("segunda rodada: %q, %v", steps[i].Detail, f.execs)
	}
}

func TestStackDesativadaEDerrubadaEReativada(t *testing.T) {
	f := &fakeDocker{}
	d := newDeps(t, f)
	writeConfig(t, d, nil)
	provideGitHub(t, d)
	if _, err := Run(context.Background(), d, nil); err != nil {
		t.Fatal(err)
	}

	// desliga o mysql na config
	writeConfig(t, d, func(c *config.Config) {
		c.Stacks = []string{"proxy", "postgres", "registry", "woodpecker"}
	})
	p, err := MakePlan(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Disable) != 1 || p.Disable[0].Name != "mysql" || p.Disable[0].Change != ChangeDisabled {
		t.Fatalf("Disable = %+v", p.Disable)
	}
	if !p.Ready() {
		t.Fatal("stack desativada nao pode impedir o Setup")
	}

	f.calls = nil
	if _, err := Run(context.Background(), d, nil); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) == 0 || f.calls[0] != "down mysql" {
		t.Errorf("tem que derrubar antes de subir as outras: %v", f.calls)
	}
	if _, err := os.Stat(d.ComposeFile("mysql")); !errors.Is(err, os.ErrNotExist) {
		t.Error("compose da stack derrubada continua ativo")
	}
	if _, err := os.Stat(d.ComposeFile("mysql") + disabledSuffix); err != nil {
		t.Error("compose da stack derrubada nao ficou como registro")
	}

	// na proxima vez ja nao aparece
	if p, _ := MakePlan(d); len(p.Disable) != 0 {
		t.Errorf("stack ja derrubada no plano de novo: %+v", p.Disable)
	}

	// religa: compose volta, registro sai
	writeConfig(t, d, nil)
	p, _ = MakePlan(d)
	if i := slices.IndexFunc(p.Stacks, func(s StackPlan) bool { return s.Name == "mysql" }); i < 0 || p.Stacks[i].Change != ChangeNew {
		t.Fatalf("mysql religado: %+v", p.Stacks)
	}
	f.calls = nil
	if _, err := Run(context.Background(), d, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.calls, "up mysql") || slices.Contains(f.calls, "down mysql") {
		t.Errorf("chamadas ao religar: %v", f.calls)
	}
	if _, err := os.Stat(d.ComposeFile("mysql") + disabledSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Error("registro de desativada sobrou depois de religar")
	}
}

// stacks de versoes antigas do cais (ex: infra, dbgate) tambem sao derrubadas
func TestStackLegadaEDerrubada(t *testing.T) {
	f := &fakeDocker{}
	d := newDeps(t, f)
	writeConfig(t, d, nil)
	provideGitHub(t, d)
	old := d.ComposeFile("infra")
	os.MkdirAll(filepath.Dir(old), 0o700)
	os.WriteFile(old, []byte("name: infra\n"), 0o644)

	p, err := MakePlan(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Disable) != 1 || p.Disable[0].Name != "infra" || !strings.Contains(p.Disable[0].Desc, "nao existe mais") {
		t.Fatalf("Disable = %+v", p.Disable)
	}
	if _, err := Run(context.Background(), d, nil); err != nil {
		t.Fatal(err)
	}
	if f.calls[0] != "down infra" {
		t.Errorf("chamadas %v", f.calls)
	}
}
