package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// o install.sh rodando como no "curl | sh" (sem clone), contra um GitHub
// falso: acha o binario da arquitetura no release, baixa com o token,
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
	var auth []string
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = append(auth, r.Header.Get("Authorization"))
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tok123" {
			w.WriteHeader(http.StatusNotFound) // o GitHub responde 404 pra repo privado sem token
			return
		}
		base := "http://" + r.Host
		switch r.URL.Path {
		case "/releases/latest", "/releases/tags/v1.0.0":
			// como a API devolve: formatado, e o uploader (com outra "url")
			// depois do name de cada asset
			asset := func(id int, name string) map[string]any {
				return map[string]any{"url": fmt.Sprintf("%s/releases/assets/%d", base, id), "id": id, "node_id": "RA_x",
					"name": name, "label": "", "uploader": map[string]any{"login": "x", "url": base + "/users/x"},
					"browser_download_url": base + "/download/" + name}
			}
			b, _ := json.MarshalIndent(map[string]any{"url": base + "/releases/1", "tag_name": "v9.9.9",
				"assets": []any{asset(11, "cais-linux-arm64"), asset(12, "cais-linux-amd64"), asset(13, "SHA256SUMS")}}, "", "  ")
			w.Write(bytes.ReplaceAll(b, []byte(`":`), []byte(`": `)))
		case "/releases/assets/11", "/releases/assets/12":
			w.Write(bin)
		case "/releases/assets/13":
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
	run := func(token string, args ...string) (string, error) {
		f, err := os.Open(script)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		// pelo stdin, numa pasta qualquer: o $0 e o sh, sem clone
		cmd := exec.Command("sh", append([]string{"-s", "--", "--dry-run"}, args...)...)
		cmd.Stdin, cmd.Dir = f, t.TempDir()
		cmd.Env = append(os.Environ(), "CAIS_RELEASE_API="+srv.URL, "GH_TOKEN="+token)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	out, err := run("tok123")
	if err != nil || !strings.Contains(out, "Baixando o cais mais novo ("+arch+")") ||
		!strings.Contains(out, "cais v9.9.9") || !strings.Contains(out, "+ grava /etc/systemd/system/cais.service") {
		t.Fatalf("instalacao pelo release: %v\n%s", err, out)
	}
	mu.Lock()
	for _, a := range auth {
		if a != "Bearer tok123" {
			t.Errorf("pedido sem o token: %q", a)
		}
	}
	if !strings.Contains(strings.Join(paths, " "), "/releases/assets/1"+map[string]string{"arm64": "1", "amd64": "2"}[arch]) {
		t.Errorf("nao baixou o binario de %s: %v", arch, paths)
	}
	paths = nil
	mu.Unlock()

	if out, err := run("tok123", "--version", "v1.0.0"); err != nil || !strings.Contains(out, "Baixando o cais v1.0.0") {
		t.Fatalf("--version: %v\n%s", err, out)
	}
	mu.Lock()
	if !strings.Contains(strings.Join(paths, " "), "/releases/tags/v1.0.0") {
		t.Errorf("--version nao pediu a tag: %v", paths)
	}
	mu.Unlock()

	if out, err := run(""); err == nil || !strings.Contains(out, "GH_TOKEN") {
		t.Fatalf("sem token: %v\n%s", err, out)
	}

	mu.Lock()
	sums = strings.Repeat("0", 64) + "  cais-linux-" + arch + "\n"
	mu.Unlock()
	if out, err := run("tok123"); err == nil || !strings.Contains(out, "SHA-256") || strings.Contains(out, "Instalando") {
		t.Fatalf("checksum errado: %v\n%s", err, out)
	}
}
