package apps

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/trdSystemDev/cais/internal/compose"
	"github.com/trdSystemDev/cais/internal/detect"
)

func validSpec() Spec {
	return Spec{
		Image:   "registry.exemplo.com.br/meu-app",
		Port:    3000,
		Volumes: []string{"/app/uploads"},
		Env:     map[string]string{"NODE_ENV": "production", "JWT_SECRET_FILE": "/run/secrets/jwt_secret"},
		Secrets: []string{"jwt_secret"},
		Workers: map[string]string{"worker": "node worker.js"},
	}
}

func TestRenderPassaNaValidacaoENoDocker(t *testing.T) {
	data, err := Render(validSpec(), "abc123")
	if err != nil {
		t.Fatal(err)
	}
	// o compose gerado passa pela mesma validacao de um compose de fora
	if err := compose.Validate(data, compose.Policy{App: "meu-app", Networks: Networks}); err != nil {
		t.Fatalf("compose gerado recusado:\n%v\n%s", err, data)
	}
	ss, _ := compose.Services(data)
	if len(ss) != 2 || ss[0].Image != "registry.exemplo.com.br/meu-app:abc123" || ss[1].Name != "worker" {
		t.Fatalf("services %+v", ss)
	}

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker nao instalado")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ComposeFile), data, 0o644)
	os.Mkdir(filepath.Join(dir, "secrets"), 0o700)
	os.WriteFile(filepath.Join(dir, "secrets", "jwt_secret"), []byte("x"), 0o444)
	if out, err := exec.Command("docker", "compose", "-p", "meu-app", "-f", filepath.Join(dir, ComposeFile), "config", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("docker compose recusou: %v\n%s\n%s", err, out, data)
	}
}

func TestCheckSpecRecusa(t *testing.T) {
	cases := map[string]func(*Spec){
		"imagem com tag":       func(s *Spec) { s.Image = "registry.exemplo.com.br/app:v1" },
		"imagem maiuscula":     func(s *Spec) { s.Image = "Registry/App" },
		"volume relativo":      func(s *Spec) { s.Volumes = []string{"uploads"} },
		"volume raiz":          func(s *Spec) { s.Volumes = []string{"/"} },
		"volume com dois pts":  func(s *Spec) { s.Volumes = []string{"/a:/b"} },
		"segredo em variavel":  func(s *Spec) { s.Env = map[string]string{"JWT_SECRET": "abc"} },
		"senha em variavel":    func(s *Spec) { s.Env = map[string]string{"DB_PASSWORD": "abc"} },
		"url em variavel":      func(s *Spec) { s.Env = map[string]string{"DATABASE_URL": "postgres://u:p@h/d"} },
		"_FILE fora de secret": func(s *Spec) { s.Env = map[string]string{"API_KEY_FILE": "/etc/passwd"} },
		"secret reservado":     func(s *Spec) { s.Secrets = []string{"cais_db_password"} },
		"secret invalido":      func(s *Spec) { s.Secrets = []string{"Jwt-Secret"} },
		"worker web":           func(s *Spec) { s.Workers = map[string]string{"web": "x"} },
		"worker sem comando":   func(s *Spec) { s.Workers = map[string]string{"fila": " "} },
	}
	for name, mutate := range cases {
		s := validSpec()
		mutate(&s)
		if err := CheckSpec(s); err == nil {
			t.Errorf("%s: aceitou %+v", name, s)
		}
	}
	if _, err := Render(validSpec(), "v1;rm -rf"); err == nil {
		t.Error("aceitou tag invalida")
	}
}

func TestDeployTag(t *testing.T) {
	m, f := newManager(t)
	ctx := context.Background()
	m.Create("meu-app")

	if _, err := m.DeployTag(ctx, "meu-app", "v1"); err == nil || !strings.Contains(err.Error(), "sem spec") {
		t.Fatalf("deploy por tag sem spec: %v", err)
	}
	if err := m.SetSpec("meu-app", validSpec()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.DeployTag(ctx, "meu-app", "v1"); err == nil || !strings.Contains(err.Error(), "jwt_secret") {
		t.Fatalf("deploy sem o secret informado: %v", err)
	}
	if len(f.ups) != 0 {
		t.Fatal("subiu sem o secret")
	}

	if err := m.SetSecret(ctx, "meu-app", "jwt_secret", []byte("segredo")); err != nil {
		t.Fatal(err)
	}
	res, err := m.DeployTag(ctx, "meu-app", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Images, []string{"registry.exemplo.com.br/meu-app:v1"}) {
		t.Errorf("imagens %v", res.Images)
	}
	if _, err := m.DeployTag(ctx, "meu-app", "v2"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.ups[len(f.ups)-1], "meu-app:v2") {
		t.Error("segundo deploy nao trocou a tag")
	}

	// trocar o secret com o app no ar recria os containers
	before := len(f.ups)
	if err := m.SetSecret(ctx, "meu-app", "jwt_secret", []byte("novo")); err != nil {
		t.Fatal(err)
	}
	if len(f.ups) != before+1 {
		t.Error("secret trocado sem recriar os containers")
	}
	for _, bad := range []string{"cais_db_password", "Maiuscula"} {
		if err := m.SetSecret(ctx, "meu-app", bad, []byte("x")); err == nil {
			t.Errorf("gravou secret %q", bad)
		}
	}

	list, _ := m.Secrets("meu-app")
	if len(list) != 1 || list[0].Name != "jwt_secret" || !list[0].Set {
		t.Errorf("secrets %+v", list)
	}
}

func TestPlanFromAnalysis(t *testing.T) {
	r := detect.Analyze(detect.Files{
		"Dockerfile":   []byte("FROM node:22\nEXPOSE 3000\nVOLUME /app/uploads\n"),
		"package.json": []byte(`{"dependencies": {"pg": "8"}}`),
		".env.example": []byte("NODE_ENV=production\nPORT=3000\nDATABASE_URL=postgres://x\nJWT_SECRET=x\n"),
	})
	p := PlanFromAnalysis("meu-app", "registry.exemplo.com.br", "exemplo.com.br", r)

	if p.Spec.Image != "registry.exemplo.com.br/meu-app" || p.Spec.Port != 3000 || !slices.Equal(p.Spec.Volumes, []string{"/app/uploads"}) {
		t.Errorf("spec %+v", p.Spec)
	}
	want := map[string]string{"NODE_ENV": "production", "PORT": "3000", "JWT_SECRET_FILE": "/run/secrets/jwt_secret"}
	for k, v := range want {
		if p.Spec.Env[k] != v {
			t.Errorf("env %s = %q, esperava %q", k, p.Spec.Env[k], v)
		}
	}
	if _, ok := p.Spec.Env["DATABASE_URL"]; ok {
		t.Error("variavel de banco foi pra env")
	}
	if !slices.Equal(p.Spec.Secrets, []string{"jwt_secret"}) {
		t.Errorf("secrets %v", p.Spec.Secrets)
	}
	if len(p.Routes) != 1 || p.Routes[0].Host != "meu-app.exemplo.com.br" || p.Routes[0].Port != 3000 {
		t.Errorf("rotas %+v", p.Routes)
	}
	if p.Database == nil || p.Database.Engine != "postgres" || p.Database.Env["DATABASE_URL_FILE"] != "url_file" {
		t.Errorf("banco %+v", p.Database)
	}
	// o plano tem que ser uma spec valida, pronta pra aplicar
	if err := CheckSpec(p.Spec); err != nil {
		t.Fatalf("plano gerou spec invalida: %v", err)
	}
}
