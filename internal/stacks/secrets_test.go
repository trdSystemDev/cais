package stacks

import (
	"bytes"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/trdSystemDev/cais/internal/secrets"
)

// todo secret que um template usa precisa saber de onde vem
func TestTodoSecretTemOrigem(t *testing.T) {
	for _, st := range All {
		r, err := Render(st.Name, validConfig(), "/")
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range r.Secrets {
			_, gen := generators[name]
			_, prov := provided[name]
			if !gen && !prov && name != secrets.RegistryHtpasswd {
				t.Errorf("stack %s usa o secret %q, que nao tem gerador nem e informado", st.Name, name)
			}
		}
	}
}

func TestEnsureSecretsGeraOQueFaltaEPedeOResto(t *testing.T) {
	s := secrets.FileStore{Dir: filepath.Join(t.TempDir(), "secrets")}
	c := validConfig()

	res, err := EnsureSecrets(s, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Missing) != 1 || res.Missing[0].Name != "github_client_secret" {
		t.Fatalf("Missing = %+v, esperava so o github_client_secret", res.Missing)
	}
	if !res.RegistryChanged {
		t.Error("primeira vez tem que criar o htpasswd")
	}
	for _, want := range []string{"database_uri", "db_bunkerweb_password", "postgres_password",
		"agent_secret", "registry_htpasswd", "registry_ci_password", "registry_vps_password"} {
		if !slices.Contains(res.Created, want) {
			t.Errorf("Created nao tem %s: %v", want, res.Created)
		}
	}

	// a URI usa a mesma senha que o banco do BunkerWeb
	pw, _ := s.Get("db_bunkerweb_password")
	uri, _ := s.Get("database_uri")
	if string(uri) != "mariadb+pymysql://bunkerweb:"+string(pw)+"@bw-db:3306/bunkerweb" {
		t.Errorf("database_uri = %q", uri)
	}

	// rodar de novo nao troca nada
	snapshot := map[string][]byte{}
	names, _ := s.List()
	for _, n := range names {
		snapshot[n], _ = s.Get(n)
	}
	res, err = EnsureSecrets(s, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Created) != 0 || res.RegistryChanged {
		t.Errorf("segunda rodada mexeu: %+v", res)
	}
	for n, v := range snapshot {
		got, _ := s.Get(n)
		if !bytes.Equal(got, v) {
			t.Errorf("%s mudou na segunda rodada", n)
		}
	}

	if err := SetProvided(s, "github_client_secret", []byte("abc")); err != nil {
		t.Fatal(err)
	}
	res, _ = EnsureSecrets(s, c)
	if len(res.Missing) != 0 {
		t.Errorf("ainda pede %+v", res.Missing)
	}
}

func TestEnsureSecretsSoDasStacksAtivas(t *testing.T) {
	s := secrets.FileStore{Dir: filepath.Join(t.TempDir(), "secrets")}
	c := validConfig()
	c.Stacks = []string{"proxy", "infra"}

	res, err := EnsureSecrets(s, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Missing) != 0 || res.RegistryChanged {
		t.Errorf("sem woodpecker/registry nao pede github nem cria htpasswd: %+v", res)
	}
	for _, n := range res.Created {
		if strings.HasPrefix(n, "registry_") || n == "agent_secret" || strings.HasPrefix(n, "dbgate_") {
			t.Errorf("criou secret de stack inativa: %s", n)
		}
	}
}

func TestSetProvidedRecusa(t *testing.T) {
	s := secrets.FileStore{Dir: filepath.Join(t.TempDir(), "secrets")}
	if err := SetProvided(s, "postgres_password", []byte("x")); err == nil {
		t.Error("deixou sobrescrever secret gerado")
	}
	if err := SetProvided(s, "github_client_secret", nil); err == nil {
		t.Error("aceitou valor vazio")
	}
}
