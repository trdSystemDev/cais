// Package githubtest e uma API do GitHub falsa pros testes de quem usa o
// GitHub App (core, TUI): repositorios em memoria, com o commit da branch
// principal e o tarball dele.
package githubtest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Server e a API falsa. Repos tem os repositorios em que o app esta
// instalado: dono/nome -> caminho -> conteudo.
type Server struct {
	URL   string
	Key   []byte // chave privada do app (PEM), pra gravar no secret
	AppID int64

	mu    sync.Mutex
	pub   *rsa.PublicKey
	repos map[string]map[string]string
}

// New sobe a API com os repositorios dados.
func New(t *testing.T, appID int64, repos map[string]map[string]string) *Server {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{AppID: appID, pub: &k.PublicKey, repos: repos,
		Key: pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.serve(t, w, r) }))
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

func (s *Server) jwtOK(r *http.Request) bool {
	parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
	if len(parts) != 3 {
		return false
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	return rsa.VerifyPKCS1v15(s.pub, crypto.SHA256, sum[:], sig) == nil
}

func (s *Server) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	notFound := func() { w.WriteHeader(404); io.WriteString(w, `{"message":"Not Found"}`) }
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")

	// /app/installations/<id>/access_tokens: um id (numerico, como no
	// GitHub) por repositorio, e o token so vale pra ele
	if len(parts) == 4 && parts[0] == "app" && parts[3] == "access_tokens" {
		if !s.jwtOK(r) {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]string{"token": "tok:" + parts[2]})
		return
	}
	if len(parts) < 3 || parts[0] != "repos" {
		notFound()
		return
	}
	full := parts[1] + "/" + parts[2]
	files, installed := s.repos[full]
	rest := parts[3:]
	if len(rest) == 1 && rest[0] == "installation" {
		if !s.jwtOK(r) || !installed {
			notFound()
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": s.installID(full)})
		return
	}
	// daqui pra frente, so com o token desse repositorio
	if r.Header.Get("Authorization") != "token tok:"+strconv.Itoa(s.installID(full)) {
		w.WriteHeader(401)
		io.WriteString(w, `{"message":"token de outro repositorio"}`)
		return
	}
	switch {
	case len(rest) == 0:
		io.WriteString(w, `{"default_branch": "main"}`)
	case rest[0] == "contents" && r.Method == http.MethodGet:
		c, ok := files[strings.Join(rest[1:], "/")]
		if !ok {
			notFound()
			return
		}
		io.WriteString(w, c)
	case rest[0] == "commits" && len(rest) == 2:
		commit := commitOf(files)
		if rest[1] != "main" && rest[1] != commit {
			notFound()
			return
		}
		io.WriteString(w, commit)
	case rest[0] == "tarball" && len(rest) == 2:
		if rest[1] != commitOf(files) {
			notFound()
			return
		}
		w.Write(tarball(t, parts[2]+"-"+rest[1][:7], files))
	default:
		t.Errorf("githubtest: rota inesperada %s %s", r.Method, r.URL.Path)
		notFound()
	}
}

// Commit e o commit atual da branch principal do repositorio: muda quando
// os arquivos mudam.
func (s *Server) Commit(repo string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return commitOf(s.repos[repo])
}

// SetFile troca um arquivo do repositorio (um commit novo).
func (s *Server) SetFile(repo, path, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.repos[repo][path] = content
}

func commitOf(files map[string]string) string {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	h := sha1.New()
	for _, n := range names {
		fmt.Fprintf(h, "%s\x00%s\x00", n, files[n])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func tarball(t *testing.T, top string, files map[string]string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for n, c := range files {
		if err := tw.WriteHeader(&tar.Header{Name: top + "/" + n, Mode: 0o644, Size: int64(len(c)), Typeflag: tar.TypeReg}); err != nil {
			t.Error(err)
		}
		io.WriteString(tw, c)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// installID e o id da instalacao num repositorio (estavel por repositorio).
func (s *Server) installID(repo string) int {
	names := make([]string, 0, len(s.repos))
	for n := range s.repos {
		names = append(names, n)
	}
	sort.Strings(names)
	return 1000 + sort.SearchStrings(names, repo)
}
