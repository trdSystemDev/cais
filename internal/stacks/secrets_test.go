package stacks

import (
	"bytes"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/secrets"
)

// todo secret que um template usa precisa saber de onde vem
func TestTodoSecretTemOrigem(t *testing.T) {
	local := validConfig()
	local.TestDomain, local.TLS = "trd.localhost", config.TLSSelfSigned
	special := []string{secrets.TLSCert, secrets.TLSKey}
	for _, c := range []config.Config{validConfig(), local} {
		for _, st := range All {
			r, err := Render(st.Name, c, "/")
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range r.Secrets {
				_, gen := generators[name]
				_, prov := provided[name]
				if !gen && !prov && !slices.Contains(special, name) {
					t.Errorf("stack %s usa o secret %q, que nao tem gerador nem e informado", st.Name, name)
				}
			}
		}
	}
}

func TestEnsureSecretsGeraCertificadoLocal(t *testing.T) {
	s := secrets.FileStore{Dir: filepath.Join(t.TempDir(), "secrets")}
	c := validConfig()
	c.TestDomain, c.TLS = "trd.localhost", config.TLSSelfSigned
	c.Stacks = []string{"postgres"}
	c.Stacks = []string{"proxy", "postgres", "garage"}
	c.Hosts.S3 = "arquivos.outro.localhost"

	create, _, err := PlanSecrets(s, c)
	if err != nil {
		t.Fatal(err)
	}
	res, err := EnsureSecrets(s, c)
	if err != nil {
		t.Fatal(err)
	}
	// o plano anuncia exatamente o que o Setup cria
	slices.Sort(create)
	created := slices.Clone(res.Created)
	slices.Sort(created)
	if !slices.Equal(create, created) {
		t.Errorf("plano dizia %v, criou %v", create, created)
	}
	for _, n := range []string{secrets.TLSCACert, secrets.TLSCAKey, secrets.TLSCert, secrets.TLSKey} {
		if !slices.Contains(res.Created, n) {
			t.Errorf("nao criou %s: %v", n, res.Created)
		}
	}
	if !slices.Contains(res.Recreate, "proxy") {
		t.Errorf("certificado novo tem que recriar o proxy: %v", res.Recreate)
	}
	if got := TLSDomains(c); !slices.Equal(got, []string{"trd.localhost", "arquivos.outro.localhost"}) {
		t.Errorf("dominios da CA %v", got)
	}

	res, _ = EnsureSecrets(s, c)
	if slices.Contains(res.Recreate, "proxy") {
		t.Error("segunda rodada recriaria o proxy sem motivo")
	}
}

func TestEnsureSecretsGeraOQueFaltaEPedeOResto(t *testing.T) {
	s := secrets.FileStore{Dir: filepath.Join(t.TempDir(), "secrets")}
	c := validConfig()

	res, err := EnsureSecrets(s, c)
	if err != nil {
		t.Fatal(err)
	}
	// nenhuma stack pede secret de fora (a chave do GitHub App e do deploy)
	if len(res.Missing) != 0 {
		t.Fatalf("Missing = %+v", res.Missing)
	}
	for _, want := range []string{"postgres_password", "mysql_root_password"} {
		if !slices.Contains(res.Created, want) {
			t.Errorf("Created nao tem %s: %v", want, res.Created)
		}
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
	if len(res.Created) != 0 || len(res.Recreate) != 0 {
		t.Errorf("segunda rodada mexeu: %+v", res)
	}
	for n, v := range snapshot {
		got, _ := s.Get(n)
		if !bytes.Equal(got, v) {
			t.Errorf("%s mudou na segunda rodada", n)
		}
	}

	if err := SetProvided(s, "github_app_key", []byte("pem")); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureSecretsSoDasStacksAtivas(t *testing.T) {
	s := secrets.FileStore{Dir: filepath.Join(t.TempDir(), "secrets")}
	c := validConfig()
	c.Stacks = nil // so as fixas

	res, err := EnsureSecrets(s, c)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range res.Created {
		if strings.HasPrefix(n, "postgres") || strings.HasPrefix(n, "mysql") || strings.HasPrefix(n, "garage") {
			t.Errorf("criou secret de servico desligado: %s", n)
		}
	}
	if len(res.Created) != 0 {
		t.Errorf("o proxy com Let's Encrypt nao precisa de secret: %v", res.Created)
	}

	c.Stacks, c.Hosts.S3 = []string{"proxy", "garage"}, "s3.exemplo.com.br"
	res, _ = EnsureSecrets(s, c)
	rpc, _ := s.Get("garage_rpc_secret")
	if !slices.Contains(res.Created, "garage_rpc_secret") || len(rpc) != 64 {
		t.Errorf("rpc_secret do Garage tem que ser 32 bytes em hex: %q", rpc)
	}
}

func TestPlanSecretsNaoGrava(t *testing.T) {
	s := secrets.FileStore{Dir: filepath.Join(t.TempDir(), "secrets")}
	c := validConfig()
	create, missing, err := PlanSecrets(s, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %+v", missing)
	}
	if !slices.Contains(create, "postgres_password") || !slices.Contains(create, "mysql_root_password") {
		t.Errorf("create = %v", create)
	}
	if names, _ := s.List(); len(names) != 0 {
		t.Fatalf("plano gravou secrets: %v", names)
	}

	EnsureSecrets(s, c)
	create, _, _ = PlanSecrets(s, c)
	if len(create) != 0 {
		t.Errorf("depois de gerar, o plano ainda cria %v", create)
	}
}

func TestSetProvidedRecusa(t *testing.T) {
	s := secrets.FileStore{Dir: filepath.Join(t.TempDir(), "secrets")}
	if err := SetProvided(s, "postgres_password", []byte("x")); err == nil {
		t.Error("deixou sobrescrever secret gerado")
	}
	if err := SetProvided(s, "github_app_key", nil); err == nil {
		t.Error("aceitou valor vazio")
	}
}
