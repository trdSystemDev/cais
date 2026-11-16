package core

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trdSystemDev/cais/internal/backup"
	"github.com/trdSystemDev/cais/internal/backup/drivetest"
	"github.com/trdSystemDev/cais/internal/config"
)

// fakeDBs finge os containers dos bancos: o dump devolve "-- dump <db>" e
// o restore guarda o que recebeu no stdin.
type fakeDBs struct {
	mu       sync.Mutex
	restored map[string]string // container/db -> dump recebido
	sql      []string          // SQL de administracao (Drop, Provision)
	failDump string
}

func (f *fakeDBs) stream(_ context.Context, container string, stdin io.Reader, stdout io.Writer, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	db := args[len(args)-1]
	if stdin == nil {
		if db == f.failDump {
			return io.ErrUnexpectedEOF
		}
		_, err := io.WriteString(stdout, "-- dump "+container+"/"+db+"\n")
		return err
	}
	data, _ := io.ReadAll(stdin)
	f.restored[container+"/"+db] = string(data)
	return nil
}

func (f *fakeDBs) execInput(_ context.Context, container string, input []byte, _ ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sql = append(f.sql, container+": "+strings.TrimSpace(string(input)))
	return "", nil
}

func newBackupCore(t *testing.T) (*Core, *drivetest.Server, *fakeDBs, string) {
	t.Helper()
	root := t.TempDir()
	s := drivetest.New()
	t.Cleanup(s.Close)
	dbs := &fakeDBs{restored: map[string]string{}}
	now := time.Date(2026, 11, 13, 3, 0, 0, 0, time.Local)
	data := filepath.Join(root, "data")
	c := New(Deps{
		CheckDocker:    okStatus,
		Now:            func() time.Time { return now },
		ConfigPath:     filepath.Join(root, "etc", "config.yaml"),
		DataDir:        data,
		SecretsDir:     filepath.Join(data, "secrets"),
		BackupsDir:     filepath.Join(data, "backups"),
		AppsDir:        filepath.Join(data, "apps"),
		StackDir:       func(n string) string { return filepath.Join(data, "stacks", n) },
		GoogleEndpoint: s.Endpoint(),
		DriveAPI:       s.API(),
		DriveUpload:    s.Upload(),
		ExecStream:     dbs.stream,
		ExecInput:      dbs.execInput,
	})
	os.MkdirAll(filepath.Join(root, "etc"), 0o755)
	cfg := config.Default()
	cfg.Domain, cfg.Email = "exemplo.com.br", "a@exemplo.com.br"
	if err := c.SaveConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	// dois databases do cais, o codigo de um app (fica de fora) e um secret
	store := c.secretStore()
	store.Put("db_postgres_loja", []byte("senhaLoja123456789"))
	store.Put("db_mysql_blog", []byte("senhaBlog123456789"))
	os.MkdirAll(filepath.Join(data, "apps", "loja", "src"), 0o755)
	os.WriteFile(filepath.Join(data, "apps", "loja", "app.yaml"), []byte("repo: o/loja\n"), 0o600)
	os.WriteFile(filepath.Join(data, "apps", "loja", "src", "main.go"), []byte("codigo"), 0o644)
	return c, s, dbs, root
}

// conecta o Drive pelo login de dispositivo e gera a chave
func setupBackup(t *testing.T, c *Core, s *drivetest.Server) string {
	t.Helper()
	ctx := context.Background()
	if err := c.SetBackupClient(ctx, "123-abc", "segredo"); err == nil {
		t.Error("aceitou client ID invalido")
	}
	if err := c.SetBackupClient(ctx, "123-abc.apps.googleusercontent.com", "segredo"); err != nil {
		t.Fatal(err)
	}
	login, err := c.StartDriveLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if login.Code != "ABCD-EFGH" || login.URL != "https://www.google.com/device" {
		t.Fatalf("login %+v", login)
	}
	if st, _ := c.Snapshot(ctx); st.Backup.Login == nil || st.Backup.Login.Code != "ABCD-EFGH" {
		t.Fatalf("codigo nao esta no estado: %+v", st.Backup)
	}
	s.Approve()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, _ := c.Snapshot(ctx)
		if st.Backup.Email == "pessoa@exemplo.com" && st.Backup.Login == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("login nao terminou: %+v", st.Backup)
		}
		time.Sleep(10 * time.Millisecond)
	}
	priv, err := c.NewBackupKey(ctx)
	if err != nil || !strings.HasPrefix(priv, "AGE-SECRET-KEY-") {
		t.Fatalf("chave %q %v", priv, err)
	}
	return priv
}

func TestBackupDoLoginAoRestore(t *testing.T) {
	c, s, dbs, _ := newBackupCore(t)
	ctx := context.Background()
	if _, err := c.RunBackup(ctx); err == nil || !strings.Contains(err.Error(), "falta o OAuth client do Google, conectar o Drive, a chave do backup") {
		t.Fatalf("backup sem configurar: %v", err)
	}
	priv := setupBackup(t, c, s)
	if err := c.SetBackupSchedule(ctx, "3:00", 7); err == nil {
		t.Error("aceitou horario invalido")
	}
	if err := c.SetBackupSchedule(ctx, "03:00", 7); err != nil {
		t.Fatal(err)
	}
	st, _ := c.RefreshBackup(ctx)
	if !st.Ready() || st.Time != "03:00" || st.RetentionDays != 7 || !strings.HasPrefix(st.Recipient, "age1") {
		t.Fatalf("estado %+v", st)
	}
	// a privada nao fica em lugar nenhum da VPS
	if found := grepDir(t, filepath.Dir(c.deps.ConfigPath), priv) + grepDir(t, c.deps.DataDir, priv); found != "" {
		t.Fatalf("chave privada gravada em %s", found)
	}

	run, err := c.RunBackup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if run.File != "cais-exemplo.com.br-2026-11-13_0300.tar.gz.age" || run.Databases != 2 || run.Size == 0 {
		t.Errorf("backup %+v", run)
	}
	if names := s.Names(); !slices.Equal(names, []string{run.File}) {
		t.Fatalf("no Drive: %v", names)
	}
	// o arquivo local sai depois do envio
	if left, _ := filepath.Glob(filepath.Join(c.deps.BackupsDir, "cais-*")); len(left) != 0 {
		t.Errorf("sobrou na VPS: %v", left)
	}

	// o conteudo: dumps, config e secrets, sem o codigo do app
	r, err := backup.NewReader(strings.NewReader(string(s.Data(run.File))), priv)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(r)
		got[h.Name] = string(data)
	}
	if got["postgres/loja.sql"] != "-- dump postgres/loja\n" || got["mysql/blog.sql"] != "-- dump mysql/blog\n" {
		t.Errorf("dumps %v", got)
	}
	if !strings.Contains(got["state/etc/config.yaml"], "exemplo.com.br") || got["state/data/secrets/db_postgres_loja"] != "senhaLoja123456789" {
		t.Errorf("estado: %v", keys(got))
	}
	for name := range got {
		if strings.Contains(name, "/src/") || strings.HasPrefix(name, "state/data/backups") {
			t.Errorf("entrou no backup: %s", name)
		}
	}
	if got["state/data/apps/loja/app.yaml"] == "" {
		t.Errorf("app.yaml fora do backup: %v", keys(got))
	}

	// restore: lista e devolve so o pedido
	res, err := c.RestoreBackup(ctx, RestoreRequest{File: run.File, Identity: priv})
	if err != nil || !slices.Equal(res.Available, []string{"mysql/blog", "postgres/loja"}) || len(res.Restored) != 0 {
		t.Fatalf("lista %+v %v", res, err)
	}
	if len(dbs.restored) != 0 {
		t.Fatal("restaurou sem pedir")
	}
	res, err = c.RestoreBackup(ctx, RestoreRequest{File: run.File, Identity: priv, Databases: []string{"postgres/loja"}})
	if err != nil || !slices.Equal(res.Restored, []string{"postgres/loja"}) {
		t.Fatalf("restore %+v %v", res, err)
	}
	if dbs.restored["postgres/loja"] != "-- dump postgres/loja\n" || len(dbs.restored) != 1 {
		t.Errorf("restaurado %v", dbs.restored)
	}
	// o database foi apagado e recriado com a senha guardada antes do dump
	sql := strings.Join(dbs.sql, "\n")
	if !strings.Contains(sql, `DROP DATABASE IF EXISTS "loja"`) || !strings.Contains(sql, "senhaLoja123456789") {
		t.Errorf("SQL %s", sql)
	}
	if _, err := c.RestoreBackup(ctx, RestoreRequest{File: run.File, Identity: priv, Databases: []string{"postgres/nao_existe"}}); err == nil {
		t.Error("restaurou database que o cais nao conhece")
	}
	_, other, _ := backup.NewKey()
	if _, err := c.RestoreBackup(ctx, RestoreRequest{File: run.File, Identity: other}); err == nil {
		t.Error("abriu com outra chave")
	}
}

func TestBackupRetencaoEFalhas(t *testing.T) {
	c, s, dbs, _ := newBackupCore(t)
	ctx := context.Background()
	setupBackup(t, c, s)
	uploadRetryDelay = 0
	defer func() { uploadRetryDelay = 5 * time.Second }()

	// primeiro backup cria a pasta; poe backups velhos nela
	if _, err := c.RunBackup(ctx); err != nil {
		t.Fatal(err)
	}
	folder := c.loadBackupDisk().Folder
	now := c.deps.Now()
	s.Add(folder, "cais-exemplo.com.br-2026-09-01_0300.tar.gz.age", []byte("x"), now.AddDate(0, 0, -73))
	s.Add(folder, "cais-exemplo.com.br-2026-11-01_0300.tar.gz.age", []byte("x"), now.AddDate(0, 0, -12))
	s.Add(folder, "anotacoes.txt", []byte("x"), now.AddDate(0, 0, -90))

	// um envio que falha uma vez e tentado de novo; um banco que falha nao
	// impede os outros
	s.Fail = "PUT"
	dbs.failDump = "blog"
	c.deps.Now = func() time.Time { return now.Add(time.Minute) }
	run, err := c.RunBackup(ctx)
	if err == nil || !strings.Contains(err.Error(), "sem 1 databases") || len(run.Errors) != 1 {
		t.Fatalf("backup com banco quebrado: %+v %v", run, err)
	}
	if run.Removed != 1 {
		t.Errorf("retencao apagou %d", run.Removed)
	}
	names := s.Names()
	if !slices.Contains(names, "cais-exemplo.com.br-2026-11-01_0300.tar.gz.age") || !slices.Contains(names, "anotacoes.txt") ||
		slices.Contains(names, "cais-exemplo.com.br-2026-09-01_0300.tar.gz.age") || !slices.Contains(names, run.File) {
		t.Errorf("no Drive: %v", names)
	}
	st, _ := c.RefreshBackup(ctx)
	if st.Last == nil || st.Last.OK() || st.Last.File != run.File {
		t.Errorf("ultimo backup no estado %+v", st.Last)
	}

	// pasta apagada no Drive: o proximo backup cria outra
	s.TrashAll()
	dbs.failDump = ""
	c.deps.Now = func() time.Time { return now.Add(2 * time.Minute) }
	if _, err := c.RunBackup(ctx); err != nil {
		t.Fatal(err)
	}
	if c.loadBackupDisk().Folder == folder {
		t.Error("reaproveitou a pasta da lixeira")
	}
}

func TestProximoBackup(t *testing.T) {
	b := config.Backup{Time: "03:00"}
	at := func(h, m int) time.Time { return time.Date(2026, 11, 13, h, m, 0, 0, time.UTC) }
	cases := []struct {
		now  time.Time
		last *BackupRun
		want time.Time
	}{
		{at(1, 0), nil, at(3, 0)}, // hoje mais tarde
		{at(3, 0), nil, at(3, 0)}, // na hora
		{at(10, 0), &BackupRun{At: at(3, 0).AddDate(0, 0, -1)}, at(10, 0)}, // VPS fora do ar as 03:00: agora
		{at(10, 0), &BackupRun{At: at(3, 1)}, at(3, 0).AddDate(0, 0, 1)},   // ja fez hoje: amanha
	}
	for i, tc := range cases {
		if got, retry := nextBackup(b, tc.last, tc.now); !got.Equal(tc.want) || retry {
			t.Errorf("%d: %v (retry %v), esperava %v", i, got, retry, tc.want)
		}
	}
	if next, _ := nextBackup(config.Backup{}, nil, at(1, 0)); !next.IsZero() {
		t.Error("sem horario agendou")
	}
}

func TestBackupQueNaoChegouNoDriveTentaDeNovo(t *testing.T) {
	b := config.Backup{Time: "03:00"}
	at := func(h, m int) time.Time { return time.Date(2026, 11, 13, h, m, 0, 0, time.UTC) }
	failed := &BackupRun{At: at(3, 0), Error: "enviando pro Drive: 503"}
	cases := []struct {
		name  string
		now   time.Time
		last  *BackupRun
		want  time.Time
		retry bool
	}{
		{"uma hora depois da falha", at(3, 5), failed, at(4, 0), true},
		{"a hora ja passou (daemon fora do ar): agora", at(6, 30), failed, at(6, 30), true},
		{"falhou as 23:30: tenta 00:30", at(23, 40), &BackupRun{At: at(23, 30), Error: "x"}, at(0, 30).AddDate(0, 0, 1), true},
		{"falhou as 02:30: o das 03:00 vem antes", at(2, 40), &BackupRun{At: at(2, 30), Error: "x"}, at(3, 0), false},
		{"subiu sem um database: so amanha", at(3, 5), &BackupRun{At: at(3, 0), Errors: []string{"postgres/x: erro"}}, at(3, 0).AddDate(0, 0, 1), false},
		{"falha manual antes do horario: tenta antes das 03:00", at(1, 10), &BackupRun{At: at(1, 0), Error: "x"}, at(2, 0), true},
	}
	for _, tc := range cases {
		got, retry := nextBackup(b, tc.last, tc.now)
		if !got.Equal(tc.want) || retry != tc.retry {
			t.Errorf("%s: %v (retry %v), esperava %v (retry %v)", tc.name, got, retry, tc.want, tc.retry)
		}
	}
}

func grepDir(t *testing.T, dir, needle string) string {
	t.Helper()
	found := ""
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			if data, _ := os.ReadFile(p); strings.Contains(string(data), needle) {
				found = p
			}
		}
		return nil
	})
	return found
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// o laco do daemon: o backup das 03:00 nao chega no Drive (fora do ar), a
// nova tentativa sai uma hora depois sem esperar o dia seguinte, e com o
// Drive de volta o proximo e o das 03:00 de amanha
func TestBackupAutomaticoTentaDeNovoDepoisDeUmaHora(t *testing.T) {
	c, s, _, _ := newBackupCore(t)
	setupBackup(t, c, s)
	uploadRetryDelay = 0
	defer func() { uploadRetryDelay = 5 * time.Second }()
	if err := c.SetBackupSchedule(context.Background(), "03:00", 30); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	now := time.Date(2026, 11, 13, 3, 0, 0, 0, time.Local)
	clock := func(d time.Time) { mu.Lock(); now = d; mu.Unlock() }
	c.deps.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	s.SetDown("PUT /upload")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.RunBackups(ctx, 5*time.Millisecond)

	last := func(ok func(*BackupRun) bool) BackupStatus {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			st, _ := c.Snapshot(context.Background())
			if b := st.Backup; b.Last != nil && !b.Running && ok(b.Last) {
				return b
			}
			if time.Now().After(deadline) {
				t.Fatalf("backup esperado nao aconteceu: %+v", st.Backup)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	at3 := now
	st := last(func(r *BackupRun) bool { return r.Error != "" })
	if !st.Retry || !st.Next.Equal(at3.Add(time.Hour)) {
		t.Fatalf("depois da falha: next %v retry %v", st.Next, st.Retry)
	}

	// antes da hora nao tenta (cada tentativa e um arquivo inteiro)
	clock(at3.Add(30 * time.Minute))
	time.Sleep(50 * time.Millisecond)
	if st, _ := c.Snapshot(context.Background()); !st.Backup.Last.At.Equal(at3) {
		t.Fatalf("tentou antes da hora: %+v", st.Backup.Last)
	}

	s.SetDown("")
	clock(at3.Add(time.Hour))
	st = last(func(r *BackupRun) bool { return r.OK() })
	if !st.Last.At.Equal(at3.Add(time.Hour)) || st.Retry || !st.Next.Equal(at3.AddDate(0, 0, 1)) {
		t.Fatalf("depois da nova tentativa: last %v next %v retry %v", st.Last.At, st.Next, st.Retry)
	}
}
