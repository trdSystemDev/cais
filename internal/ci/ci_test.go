package ci

import (
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
	"golang.org/x/crypto/ssh"

	"github.com/trdSystemDev/cais/internal/secrets"
)

func TestPipelineEValidoENaoTemDominio(t *testing.T) {
	var doc struct {
		Steps []struct {
			Name     string
			Image    string
			Settings map[string]any
			Commands []string
		}
	}
	if err := yaml.Unmarshal(Pipeline(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Steps) != 2 || doc.Steps[0].Name != "build" || doc.Steps[1].Name != "deploy" {
		t.Fatalf("steps %+v", doc.Steps)
	}
	text := string(Pipeline())
	for _, bad := range []string{"trds", "localhost", "skip_tls_verify", "latest"} {
		if strings.Contains(text, bad) {
			t.Errorf("pipeline contem %q", bad)
		}
	}
	last := doc.Steps[1].Commands[len(doc.Steps[1].Commands)-1]
	if !strings.Contains(last, `cais-deploy@"$HOST_IP" "$CI_REPO_NAME $CI_COMMIT_SHA"`) {
		t.Errorf("deploy nao pede app e tag: %s", last)
	}
}

func TestChaveDeDeploy(t *testing.T) {
	s := secrets.FileStore{Dir: filepath.Join(t.TempDir(), "secrets")}
	pub, err := EnsureDeployKey(s)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := EnsureDeployKey(s); again != pub {
		t.Fatal("gerou outra chave na segunda vez")
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pub)); err != nil {
		t.Fatalf("chave publica invalida: %v", err)
	}
	priv, _ := s.Get(KeySecret)
	signer, err := ssh.ParsePrivateKey(priv)
	if err != nil {
		t.Fatalf("chave privada invalida: %v", err)
	}
	if !strings.HasPrefix(pub, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))) {
		t.Fatal("publica e privada nao sao do mesmo par")
	}

	line := AuthorizedKeysLine("/usr/local/bin/cais", pub)
	if !strings.HasPrefix(line, `command="/usr/local/bin/cais deploy",restrict ssh-ed25519 `) {
		t.Errorf("linha do authorized_keys: %s", line)
	}
	if _, _, opts, _, err := ssh.ParseAuthorizedKey([]byte(line)); err != nil || len(opts) != 2 {
		t.Errorf("sshd nao leria a linha: %v %v", opts, err)
	}
}

func TestPipelineComBuildDoApp(t *testing.T) {
	var doc struct {
		Steps []struct {
			Settings map[string]any
		}
	}
	out := PipelineFor(Build{Context: "app", Dockerfile: "app/Dockerfile",
		Args: map[string]string{"NEXT_PUBLIC_MAPBOX_ACCESS_TOKEN": "pk.abc", "NEXT_PUBLIC_X": "a$b"}})
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	set := doc.Steps[0].Settings
	if set["context"] != "app" || set["dockerfile"] != "app/Dockerfile" {
		t.Errorf("settings %v", set)
	}
	args, _ := set["build-args"].([]any)
	if len(args) != 2 || args[0] != "NEXT_PUBLIC_MAPBOX_ACCESS_TOKEN=pk.abc" || args[1] != "NEXT_PUBLIC_X=a$$b" {
		t.Errorf("build-args %v", args)
	}
	// sem build proprio, o pipeline e o padrao (sem context/dockerfile/args)
	if def := string(Pipeline()); strings.Contains(def, "context:") || strings.Contains(def, "build-args") || strings.Contains(def, "{{") {
		t.Errorf("pipeline padrao:\n%s", def)
	}
}
