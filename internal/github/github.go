package github

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// API e o endereco da API do GitHub.
const API = "https://api.github.com"

// maxFile e o maior arquivo lido do repositorio: a analise so precisa de
// Dockerfile, .env.example e manifestos.
const maxFile = 1 << 20

// Repo identifica um repositorio.
type Repo struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

func (r Repo) String() string { return r.Owner + "/" + r.Name }

var (
	ownerName = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	repoName  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

// ParseRepo aceita o link do repositorio como o GitHub mostra:
// https://github.com/dono/repo, com .git, o caminho de clone por SSH
// (git@github.com:dono/repo.git) ou so dono/repo.
func ParseRepo(link string) (Repo, error) {
	s := strings.TrimSpace(link)
	s = strings.TrimSuffix(strings.TrimSuffix(s, "/"), ".git")
	switch {
	case strings.HasPrefix(s, "git@github.com:"):
		s = strings.TrimPrefix(s, "git@github.com:")
	case strings.Contains(s, "://"):
		u, err := url.Parse(s)
		if err != nil || (u.Host != "github.com" && u.Host != "www.github.com") {
			return Repo{}, fmt.Errorf("link %q nao e de um repositorio do GitHub", link)
		}
		s = strings.TrimPrefix(u.Path, "/")
	}
	parts := strings.Split(s, "/")
	if len(parts) != 2 || !ownerName.MatchString(parts[0]) || !repoName.MatchString(parts[1]) || parts[1] == "." || parts[1] == ".." {
		return Repo{}, fmt.Errorf("link %q nao e de um repositorio do GitHub (ex: https://github.com/dono/repo)", link)
	}
	return Repo{Owner: parts[0], Name: parts[1]}, nil
}

// App e o cais autenticado como GitHub App.
type App struct {
	ID   int64
	Key  *rsa.PrivateKey
	HTTP *http.Client
	// BaseURL troca a API (testes); vazio e o GitHub.
	BaseURL string
	Now     func() time.Time
}

// ParseKey le a chave privada do GitHub App (o .pem baixado da pagina do
// app, PKCS#1; PKCS#8 tambem e aceito).
func ParseKey(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("chave do GitHub App invalida: nao e um arquivo PEM (.pem)")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("chave do GitHub App invalida: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("chave do GitHub App precisa ser RSA")
	}
	return rk, nil
}

func (a App) base() string {
	if a.BaseURL != "" {
		return strings.TrimSuffix(a.BaseURL, "/")
	}
	return API
}

func (a App) client() *http.Client {
	if a.HTTP != nil {
		return a.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (a App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// jwt e o token do proprio app (RS256, 9 minutos): so serve pra pedir o
// token de instalacao. O iat volta 60s pra tolerar relogio adiantado.
func (a App) jwt() (string, error) {
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	now := a.now()
	claims, _ := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": strconv.FormatInt(a.ID, 10),
	})
	signing := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.Key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// Error e uma resposta de erro da API do GitHub.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("GitHub respondeu %d: %s", e.Status, e.Message) }

// IsNotFound diz se o erro e 404 (repositorio ou arquivo que nao existe, ou
// que o app nao enxerga).
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// do faz uma chamada a API. auth e "Bearer <jwt>" ou "token <instalacao>".
func (a App) do(ctx context.Context, method, path, auth, accept string, body, out any) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base()+path, rd)
	if err != nil {
		return nil, err
	}
	if accept == "" {
		accept = "application/vnd.github+json"
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("Authorization", auth)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "cais")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("falando com o GitHub: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFile {
		return nil, fmt.Errorf("resposta do GitHub grande demais (%s)", path)
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		json.Unmarshal(data, &e)
		if e.Message == "" {
			e.Message = http.StatusText(resp.StatusCode)
		}
		return nil, &Error{Status: resp.StatusCode, Message: e.Message}
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return nil, fmt.Errorf("resposta do GitHub: %w", err)
		}
	}
	return data, nil
}

// Session e o acesso a um repositorio com um token de instalacao, que vale
// uma hora e so enxerga esse repositorio.
type Session struct {
	app   App
	token string
	Repo  Repo
	// DefaultBranch e o branch principal do repositorio (main, master...).
	DefaultBranch string
}

// Open autentica no repositorio: acha a instalacao do app nele e pede um
// token restrito a ele.
func (a App) Open(ctx context.Context, r Repo) (*Session, error) {
	if a.ID <= 0 || a.Key == nil {
		return nil, errors.New("GitHub App nao configurado (github.app_id e o secret github_app_key)")
	}
	jwt, err := a.jwt()
	if err != nil {
		return nil, err
	}
	var inst struct {
		ID int64 `json:"id"`
	}
	path := "/repos/" + url.PathEscape(r.Owner) + "/" + url.PathEscape(r.Name) + "/installation"
	if _, err := a.do(ctx, http.MethodGet, path, "Bearer "+jwt, "", nil, &inst); err != nil {
		if IsNotFound(err) {
			return nil, fmt.Errorf("o GitHub App nao esta instalado em %s (ou o repositorio nao existe): instale em Settings > GitHub Apps > Configure e libere o repositorio", r)
		}
		return nil, err
	}
	var tok struct {
		Token string `json:"token"`
	}
	body := map[string]any{"repositories": []string{r.Name}}
	if _, err := a.do(ctx, http.MethodPost, fmt.Sprintf("/app/installations/%d/access_tokens", inst.ID), "Bearer "+jwt, "", body, &tok); err != nil {
		return nil, err
	}
	s := &Session{app: a, token: tok.Token, Repo: r}
	var info struct {
		DefaultBranch string `json:"default_branch"`
	}
	if _, err := s.call(ctx, http.MethodGet, s.repoPath(""), "", nil, &info); err != nil {
		return nil, err
	}
	s.DefaultBranch = info.DefaultBranch
	return s, nil
}

func (s *Session) repoPath(rest string) string {
	return "/repos/" + url.PathEscape(s.Repo.Owner) + "/" + url.PathEscape(s.Repo.Name) + rest
}

func (s *Session) call(ctx context.Context, method, path, accept string, body, out any) ([]byte, error) {
	return s.app.do(ctx, method, path, "token "+s.token, accept, body, out)
}

func contentPath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return "/contents/" + strings.Join(parts, "/")
}

// ReadFile le um arquivo do branch principal. Arquivo que nao existe
// devolve um erro com IsNotFound.
func (s *Session) ReadFile(ctx context.Context, path string) ([]byte, error) {
	return s.call(ctx, http.MethodGet, s.repoPath(contentPath(path))+"?ref="+url.QueryEscape(s.DefaultBranch),
		"application/vnd.github.raw+json", nil, nil)
}

// ReadFiles le os caminhos pedidos, pulando os que nao existem.
func (s *Session) ReadFiles(ctx context.Context, paths []string) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, p := range paths {
		data, err := s.ReadFile(ctx, p)
		if IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		out[p] = data
	}
	return out, nil
}

// ProposeFile abre um pull request que cria (ou troca) um arquivo, num
// branch proprio a partir do principal. Devolve o link do PR. Se ja existe
// um PR aberto desse branch, so atualiza o arquivo nele.
func (s *Session) ProposeFile(ctx context.Context, branch, path string, content []byte, title, body string) (string, error) {
	var head struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if _, err := s.call(ctx, http.MethodGet, s.repoPath("/git/ref/heads/"+url.PathEscape(s.DefaultBranch)), "", nil, &head); err != nil {
		return "", err
	}
	_, err := s.call(ctx, http.MethodPost, s.repoPath("/git/refs"), "", map[string]string{
		"ref": "refs/heads/" + branch, "sha": head.Object.SHA}, nil)
	var gerr *Error
	if err != nil && !(errors.As(err, &gerr) && gerr.Status == http.StatusUnprocessableEntity) {
		return "", fmt.Errorf("criando o branch %s: %w", branch, err)
	}

	// arquivo que ja existe no branch precisa do sha pra ser trocado
	put := map[string]string{
		"message": title,
		"content": base64.StdEncoding.EncodeToString(content),
		"branch":  branch,
	}
	var existing struct {
		SHA string `json:"sha"`
	}
	if _, err := s.call(ctx, http.MethodGet, s.repoPath(contentPath(path))+"?ref="+url.QueryEscape(branch), "", nil, &existing); err == nil {
		put["sha"] = existing.SHA
	} else if !IsNotFound(err) {
		return "", err
	}
	if _, err := s.call(ctx, http.MethodPut, s.repoPath(contentPath(path)), "", put, nil); err != nil {
		return "", fmt.Errorf("gravando %s: %w", path, err)
	}

	var pr struct {
		HTMLURL string `json:"html_url"`
	}
	_, err = s.call(ctx, http.MethodPost, s.repoPath("/pulls"), "", map[string]string{
		"title": title, "head": branch, "base": s.DefaultBranch, "body": body}, &pr)
	if err != nil && errors.As(err, &gerr) && gerr.Status == http.StatusUnprocessableEntity {
		// PR desse branch ja aberto: o arquivo foi atualizado nele
		var open []struct {
			HTMLURL string `json:"html_url"`
		}
		q := "?state=open&head=" + url.QueryEscape(s.Repo.Owner+":"+branch)
		if _, err := s.call(ctx, http.MethodGet, s.repoPath("/pulls")+q, "", nil, &open); err == nil && len(open) > 0 {
			return open[0].HTMLURL, nil
		}
	}
	if err != nil {
		return "", fmt.Errorf("abrindo o pull request: %w", err)
	}
	return pr.HTMLURL, nil
}

// NormalizeKey refaz o PEM de uma chave colada num campo de uma linha (as
// quebras de linha viram espaco) e confere que e uma chave valida do app.
func NormalizeKey(text string) ([]byte, error) {
	s := strings.TrimSpace(text)
	const dashes = "-----"
	begin := strings.Index(s, dashes+"BEGIN ")
	if begin < 0 {
		return nil, errors.New("chave do GitHub App invalida: cole o conteudo do .pem inteiro (de -----BEGIN ate -----END) ou o caminho do arquivo")
	}
	s = s[begin:]
	headEnd := strings.Index(s[len(dashes):], dashes)
	if headEnd < 0 {
		return nil, errors.New("chave do GitHub App invalida: cabecalho do PEM incompleto")
	}
	header := s[:len(dashes)+headEnd+len(dashes)]
	label := strings.TrimSuffix(strings.TrimPrefix(header, dashes+"BEGIN "), dashes)
	footer := dashes + "END " + label + dashes
	end := strings.Index(s, footer)
	if end < 0 {
		return nil, fmt.Errorf("chave do GitHub App invalida: falta o fim (%s)", footer)
	}
	body := strings.Join(strings.Fields(s[len(header):end]), "")
	var b strings.Builder
	b.WriteString(header + "\n")
	for len(body) > 64 {
		b.WriteString(body[:64] + "\n")
		body = body[64:]
	}
	if body != "" {
		b.WriteString(body + "\n")
	}
	b.WriteString(footer + "\n")
	pem := []byte(b.String())
	if _, err := ParseKey(pem); err != nil {
		return nil, err
	}
	return pem, nil
}
