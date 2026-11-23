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
	"sync"
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
	// Limits anota o limite de requisicoes que o GitHub informa e recusa
	// chamar com a cota esgotada; nil nao anota.
	Limits *Limits
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

// IsUnauthorized diz se o erro e 401: token que deixou de valer (app
// reinstalado, chave trocada).
func IsUnauthorized(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusUnauthorized
}

// request e uma chamada a API.
type request struct {
	method, path string
	auth         string // "Bearer <jwt>" ou "token <instalacao>"
	accept       string
	header       map[string]string // cabecalhos extras (If-None-Match)
	body         any
	limit        int    // tamanho maximo da resposta
	quota        string // cota que a chamada gasta (QuotaApp ou a da instalacao)
}

// response e a resposta crua: quem chama decide o que e erro (o Resolve
// trata o 304).
type response struct {
	status int
	header http.Header
	data   []byte
}

// check transforma status de erro em *Error.
func (r response) check() error {
	if r.status < 300 {
		return nil
	}
	var e struct {
		Message string `json:"message"`
	}
	json.Unmarshal(r.data, &e)
	if e.Message == "" {
		e.Message = http.StatusText(r.status)
	}
	return &Error{Status: r.status, Message: e.Message}
}

// send faz a chamada e anota o limite que o GitHub informou. Erro so de
// rede, de tamanho ou de limite esgotado (*RateLimitError): com a cota
// esgotada nem chama, porque insistir faz o GitHub bloquear por mais tempo.
func (a App) send(ctx context.Context, rq request) (response, error) {
	if err := a.Limits.wait(rq.quota, a.now()); err != nil {
		return response{}, err
	}
	var rd io.Reader
	if rq.body != nil {
		b, err := json.Marshal(rq.body)
		if err != nil {
			return response{}, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, rq.method, a.base()+rq.path, rd)
	if err != nil {
		return response{}, err
	}
	accept := rq.accept
	if accept == "" {
		accept = "application/vnd.github+json"
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("Authorization", rq.auth)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "cais")
	if rq.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range rq.header {
		req.Header.Set(k, v)
	}
	resp, err := a.client().Do(req)
	if err != nil {
		return response{}, fmt.Errorf("falando com o GitHub: %w", err)
	}
	defer resp.Body.Close()
	if err := a.Limits.observe(rq.quota, resp.StatusCode, resp.Header, a.now()); err != nil {
		return response{}, err
	}
	limit := rq.limit
	if limit == 0 {
		limit = maxFile
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return response{}, err
	}
	if len(data) > limit {
		return response{}, fmt.Errorf("resposta do GitHub grande demais (%s)", rq.path)
	}
	return response{status: resp.StatusCode, header: resp.Header, data: data}, nil
}

// do faz a chamada e decodifica o JSON da resposta em out (se nao for nil).
func (a App) do(ctx context.Context, rq request, out any) ([]byte, error) {
	res, err := a.send(ctx, rq)
	if err != nil {
		return nil, err
	}
	if err := res.check(); err != nil {
		return nil, err
	}
	if out != nil {
		if err := json.Unmarshal(res.data, out); err != nil {
			return nil, fmt.Errorf("resposta do GitHub: %w", err)
		}
	}
	return res.data, nil
}

// asApp e uma chamada com o JWT do app (cota do app).
func (a App) asApp(ctx context.Context, method, path string, body, out any) ([]byte, error) {
	jwt, err := a.jwt()
	if err != nil {
		return nil, err
	}
	return a.do(ctx, request{method: method, path: path, auth: "Bearer " + jwt, body: body, quota: QuotaApp}, out)
}

// Check e o resultado do teste do GitHub App: o nome dele e as contas
// (usuarios ou organizacoes) em que esta instalado.
type Check struct {
	Name          string   `json:"name"`
	Installations []string `json:"installations"`
}

// Check confere o App ID e a chave (o JWT tem que ser aceito) e lista onde
// o app esta instalado. Nao abre nenhum repositorio.
func (a App) Check(ctx context.Context) (Check, error) {
	if a.ID <= 0 || a.Key == nil {
		return Check{}, errors.New("GitHub App nao configurado: informe o App ID e a chave privada")
	}
	var app struct {
		Name string `json:"name"`
	}
	if _, err := a.asApp(ctx, http.MethodGet, "/app", nil, &app); err != nil {
		if IsUnauthorized(err) || IsNotFound(err) {
			return Check{}, fmt.Errorf("o GitHub recusou o App ID %d com esta chave: confira o App ID e se a chave privada e deste app (%w)", a.ID, err)
		}
		return Check{}, err
	}
	var insts []struct {
		Account struct {
			Login string `json:"login"`
		} `json:"account"`
	}
	if _, err := a.asApp(ctx, http.MethodGet, "/app/installations?per_page=100", nil, &insts); err != nil {
		return Check{}, err
	}
	c := Check{Name: app.Name}
	for _, in := range insts {
		c.Installations = append(c.Installations, in.Account.Login)
	}
	return c, nil
}

// Session e o acesso a um repositorio com um token de instalacao, que vale
// uma hora e so enxerga esse repositorio. Pode ser usada por varias
// goroutines (o Sessions guarda uma por repositorio).
type Session struct {
	app   App
	token string
	Repo  Repo
	// DefaultBranch e o branch principal do repositorio (main, master...).
	DefaultBranch string
	// Expires e quando o token deixa de valer.
	Expires time.Time
	quota   string // cota da instalacao

	mu    sync.Mutex
	etags map[string]etagged // ref -> ultima resposta do Resolve
}

// etagged e o commit de um ref com o ETag da resposta: perguntar de novo
// com If-None-Match e receber 304 nao gasta o limite da API.
type etagged struct {
	etag, sha string
}

// Open autentica no repositorio: acha a instalacao do app nele e pede um
// token restrito a ele (3 chamadas: use Sessions pra reaproveitar).
func (a App) Open(ctx context.Context, r Repo) (*Session, error) {
	if a.ID <= 0 || a.Key == nil {
		return nil, errors.New("GitHub App nao configurado (github.app_id e o secret github_app_key)")
	}
	var inst struct {
		ID int64 `json:"id"`
	}
	path := "/repos/" + url.PathEscape(r.Owner) + "/" + url.PathEscape(r.Name) + "/installation"
	if _, err := a.asApp(ctx, http.MethodGet, path, nil, &inst); err != nil {
		if IsNotFound(err) {
			return nil, fmt.Errorf("o GitHub App nao esta instalado em %s (ou o repositorio nao existe): instale em Settings > GitHub Apps > Configure e libere o repositorio", r)
		}
		return nil, err
	}
	var tok struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	body := map[string]any{"repositories": []string{r.Name}}
	if _, err := a.asApp(ctx, http.MethodPost, fmt.Sprintf("/app/installations/%d/access_tokens", inst.ID), body, &tok); err != nil {
		return nil, err
	}
	if tok.ExpiresAt.IsZero() {
		tok.ExpiresAt = a.now().Add(time.Hour)
	}
	s := &Session{app: a, token: tok.Token, Repo: r, Expires: tok.ExpiresAt, quota: QuotaInstallation(inst.ID)}
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

// req monta uma chamada com o token da sessao.
func (s *Session) req(method, path, accept string) request {
	return request{method: method, path: path, auth: "token " + s.token, accept: accept, quota: s.quota}
}

func (s *Session) call(ctx context.Context, method, path, accept string, body, out any) ([]byte, error) {
	rq := s.req(method, path, accept)
	rq.body = body
	return s.app.do(ctx, rq, out)
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
