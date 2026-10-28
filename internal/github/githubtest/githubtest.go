// Package githubtest e uma API do GitHub falsa pros testes de quem usa o
// GitHub App (core, TUI): repositorios, arquivos e PRs em memoria.
package githubtest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
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
	// PRs sao os pull requests abertos: dono/nome -> arquivo -> conteudo.
	PRs map[string]map[string]string
}

// New sobe a API com os repositorios dados.
func New(t *testing.T, appID int64, repos map[string]map[string]string) *Server {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{AppID: appID, pub: &k.PublicKey, repos: repos, PRs: map[string]map[string]string{},
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
		path := strings.Join(rest[1:], "/")
		if r.URL.Query().Get("ref") != "main" {
			if c, ok := s.PRs[full][path]; ok {
				json.NewEncoder(w).Encode(map[string]string{"sha": "x", "content": c})
				return
			}
			notFound()
			return
		}
		c, ok := files[path]
		if !ok {
			notFound()
			return
		}
		io.WriteString(w, c)
	case rest[0] == "git" && r.Method == http.MethodGet:
		io.WriteString(w, `{"object": {"sha": "abc"}}`)
	case rest[0] == "git" && r.Method == http.MethodPost:
		w.WriteHeader(201)
		io.WriteString(w, `{}`)
	case rest[0] == "contents" && r.Method == http.MethodPut:
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		c, _ := base64.StdEncoding.DecodeString(body["content"])
		if s.PRs[full] == nil {
			s.PRs[full] = map[string]string{}
		}
		s.PRs[full][strings.Join(rest[1:], "/")] = string(c)
		w.WriteHeader(201)
		io.WriteString(w, `{}`)
	case rest[0] == "pulls" && r.Method == http.MethodPost:
		w.WriteHeader(201)
		io.WriteString(w, `{"html_url": "https://github.com/`+full+`/pull/1"}`)
	default:
		t.Errorf("githubtest: rota inesperada %s %s", r.Method, r.URL.Path)
		notFound()
	}
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

// PR devolve o arquivo proposto no PR do repositorio ("" se nao ha).
func (s *Server) PR(repo, path string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.PRs[repo][path]
}
