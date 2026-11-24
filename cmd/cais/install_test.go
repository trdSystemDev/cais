package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
)

// o install.sh rodando como no "curl | sh" (sem clone), contra um GitHub
// falso: baixa o binario da arquitetura pelos links diretos do release,
// confere o SHA-256 e segue a instalacao (em --dry-run: nada muda)
func TestInstallPeloRelease(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("o install.sh e pra Linux")
	}
	for _, tool := range []string{"sh", "curl", "sha256sum", "docker", "systemctl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("sem %s", tool)
		}
	}
	if exec.Command("docker", "info").Run() != nil {
		t.Skip("docker nao responde")
	}
	arch := map[string]string{"amd64": "amd64", "arm64": "arm64"}[runtime.GOARCH]
	// o "binario" e um script: so precisa responder ao version
	bin := []byte("#!/bin/sh\necho 'cais v9.9.9'\n")
	sum := sha256.Sum256(bin)
	sums := hex.EncodeToString(sum[:]) + "  cais-linux-" + arch + "\n"

	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		// como o GitHub: o link do release redireciona pro arquivo
		for _, base := range []string{"/latest/download/", "/download/v1.0.0/"} {
			if name, ok := strings.CutPrefix(r.URL.Path, base); ok {
				http.Redirect(w, r, "/files/"+name, http.StatusFound)
				return
			}
		}
		switch r.URL.Path {
		case "/files/cais-linux-amd64", "/files/cais-linux-arm64":
			w.Write(bin)
		case "/files/SHA256SUMS":
			mu.Lock()
			s := sums
			mu.Unlock()
			w.Write([]byte(s))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	script, err := filepath.Abs("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	// run roda o install.sh; argumentos NOME=valor vao pro ambiente
	run := func(url string, args ...string) (string, error) {
		f, err := os.Open(script)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		env := append(os.Environ(), "CAIS_RELEASE_URL="+url)
		flags := []string{"-s", "--", "--dry-run"}
		for _, a := range args {
			if k, _, ok := strings.Cut(a, "="); ok && strings.ToUpper(k) == k && !strings.HasPrefix(a, "-") {
				env = append(env, a)
			} else {
				flags = append(flags, a)
			}
		}
		// pelo stdin, numa pasta qualquer: o $0 e o sh, sem clone
		sh, _ := exec.LookPath("sh")
		cmd := exec.Command(sh, flags...)
		cmd.Stdin, cmd.Dir = f, t.TempDir()
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	out, err := run(srv.URL)
	if err != nil || !strings.Contains(out, "Baixando o cais mais novo ("+arch+")") ||
		!strings.Contains(out, "cais v9.9.9") || !strings.Contains(out, "+ grava /etc/systemd/system/cais.service") {
		t.Fatalf("instalacao pelo release: %v\n%s", err, out)
	}
	mu.Lock()
	if !strings.Contains(strings.Join(paths, " "), "/latest/download/cais-linux-"+arch) {
		t.Errorf("nao baixou o binario de %s: %v", arch, paths)
	}
	paths = nil
	mu.Unlock()

	if out, err := run(srv.URL, "--version", "v1.0.0"); err != nil || !strings.Contains(out, "Baixando o cais v1.0.0") {
		t.Fatalf("--version: %v\n%s", err, out)
	}
	mu.Lock()
	if !strings.Contains(strings.Join(paths, " "), "/download/v1.0.0/cais-linux-"+arch) {
		t.Errorf("--version nao pediu a tag: %v", paths)
	}
	mu.Unlock()

	if out, err := run(srv.URL, "--version", "v0.0.1"); err == nil || !strings.Contains(out, "confira se ha release publicado com a tag v0.0.1") {
		t.Fatalf("tag sem release: %v\n%s", err, out)
	}

	// VPS nova sem Docker: no Ubuntu, instala pelo repositorio oficial
	noDocker := pathWithout(t, "docker")
	osRelease := filepath.Join(t.TempDir(), "os-release")
	os.WriteFile(osRelease, []byte("ID=ubuntu\nVERSION_CODENAME=noble\n"), 0o644)
	out, err = run(srv.URL, "PATH="+noDocker, "CAIS_OS_RELEASE="+osRelease)
	if err != nil || !strings.Contains(out, "Instalando o Docker (repositorio oficial, ubuntu noble)") ||
		!strings.Contains(out, "https://download.docker.com/linux/ubuntu noble stable") ||
		!strings.Contains(out, "docker-compose-plugin") || !strings.Contains(out, "+ grava /etc/systemd/system/cais.service") {
		t.Fatalf("sem docker no ubuntu: %v\n%s", err, out)
	}
	if out, err := run(srv.URL, "--harden", "deploy"); err != nil || !strings.Contains(out, "+ /usr/local/bin/cais host harden -user deploy") {
		t.Fatalf("--harden: %v\n%s", err, out)
	}
	if out, err := run(srv.URL, "--harden"); err == nil || !strings.Contains(out, "--harden precisa do nome") {
		t.Fatalf("--harden sem nome: %v\n%s", err, out)
	}
	os.WriteFile(osRelease, []byte("ID=arch\n"), 0o644)
	if out, err := run(srv.URL, "PATH="+noDocker, "CAIS_OS_RELEASE="+osRelease); err == nil || !strings.Contains(out, "instale o Docker antes") {
		t.Fatalf("sem docker fora do ubuntu/debian: %v\n%s", err, out)
	}

	mu.Lock()
	sums = strings.Repeat("0", 64) + "  cais-linux-" + arch + "\n"
	mu.Unlock()
	if out, err := run(srv.URL); err == nil || !strings.Contains(out, "SHA-256") || strings.Contains(out, "Instalando") {
		t.Fatalf("checksum errado: %v\n%s", err, out)
	}
}

// pathWithout e um PATH com tudo do PATH atual menos os comandos de skip
// (links numa pasta temporaria).
func pathWithout(t *testing.T, skip ...string) string {
	dir := t.TempDir()
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		entries, _ := os.ReadDir(d)
		for _, e := range entries {
			if slices.Contains(skip, e.Name()) {
				continue
			}
			os.Symlink(filepath.Join(d, e.Name()), filepath.Join(dir, e.Name())) // o primeiro do PATH ganha
		}
	}
	return dir
}
