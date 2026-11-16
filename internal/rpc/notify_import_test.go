package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/core"
)

// o e-mail de teste e a importacao de banco pelo socket, como o CLI e a
// TUI chamam: a string de conexao chega inteira no daemon, a senha vai pro
// dump pelo stdin e o andamento aparece pra quem esta inscrito
func TestAvisoEImportacaoPeloSocket(t *testing.T) {
	var mails []string
	var mu sync.Mutex
	brevo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct {
			Subject string `json:"subject"`
			HTML    string `json:"htmlContent"`
		}
		json.NewDecoder(r.Body).Decode(&m)
		mu.Lock()
		mails = append(mails, m.Subject)
		mu.Unlock()
		if r.Header.Get("api-key") != "xkeysib-teste" || !strings.Contains(m.HTML, "<html") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer brevo.Close()

	seen := make(chan struct{}) // o teste viu o andamento: o dump pode acabar
	var password, restored string
	root := t.TempDir()
	d := startDaemonDeps(t, core.Deps{
		CheckDocker: okStatus,
		ConfigPath:  filepath.Join(root, "config.yaml"),
		SecretsDir:  filepath.Join(root, "secrets"),
		StackDir:    func(n string) string { return filepath.Join(root, "stacks", n) },
		BrevoURL:    brevo.URL,
		Hostname:    func() (string, error) { return "vps-teste", nil },
		ExecInput:   func(context.Context, string, []byte, ...string) (string, error) { return "", nil },
		ExecStream: func(_ context.Context, _ string, stdin io.Reader, stdout io.Writer, args ...string) error {
			if strings.Contains(strings.Join(args, " "), "pg_dump") {
				line, _ := bufio.NewReader(stdin).ReadString('\n')
				mu.Lock()
				password = strings.TrimSuffix(line, "\n")
				mu.Unlock()
				<-seen
				_, err := io.WriteString(stdout, "CREATE TABLE t (id int);\n")
				return err
			}
			b, err := io.ReadAll(stdin)
			mu.Lock()
			restored = string(b)
			mu.Unlock()
			return err
		},
	})
	c := dial(t, d.path)
	ctx := context.Background()

	cfg := config.Default()
	cfg.Email, cfg.Stacks = "a@exemplo.com.br", []string{"postgres"}
	cfg.Notify = config.Notify{From: "cais@exemplo.com.br", To: []string{"ops@exemplo.com.br"}}
	if err := c.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := c.SetSecret(ctx, "brevo_api_key", "xkeysib-teste"); err != nil {
		t.Fatal(err)
	}
	if err := c.TestNotify(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(mails) != 1 || !strings.Contains(mails[0], "[cais vps-teste]") {
		t.Errorf("e-mails: %q", mails)
	}
	mu.Unlock()

	states, unsub, _ := c.Subscribe(ctx)
	defer unsub()
	type result struct {
		info core.DatabaseInfo
		err  error
	}
	done := make(chan result, 1)
	go func() {
		info, err := c.CreateDatabase(ctx, core.DatabaseRequest{Engine: "postgres", Name: "loja",
			Source: "postgres://antigo:S3nh@/x@db.antigo.com/loja"})
		done <- result{info, err}
	}()
	st := recv(t, states, func(s core.State) bool { return s.Import != nil })
	if st.Import.Name != "loja" || st.Import.Source != "antigo@db.antigo.com:5432/loja" {
		t.Errorf("andamento: %+v", st.Import)
	}
	if b, _ := json.Marshal(st); strings.Contains(string(b), "S3nh@") {
		t.Error("a senha da origem esta no estado")
	}
	close(seen)
	r := <-done
	if r.err != nil || r.info.Imported != "antigo@db.antigo.com:5432/loja" {
		t.Fatalf("CreateDatabase: %+v, %v", r.info, r.err)
	}
	if password != "S3nh@/x" || restored != "CREATE TABLE t (id int);\n" {
		t.Errorf("senha no dump %q, restore %q", password, restored)
	}
	recv(t, states, func(s core.State) bool { return s.Import == nil })
}
