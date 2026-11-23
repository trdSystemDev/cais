// Package onedrivetest e um OneDrive (Microsoft Graph) e o login de
// dispositivo da Microsoft falsos pros testes: a pasta do app, sessoes de
// envio em pedacos, lista paginada, download por redirecionamento e
// exclusao, em memoria. Confere o que o Graph de verdade exige: pedacos e
// download sem o token, cliente publico (sem secret) e o escopo da pasta do
// app; e troca o refresh token a cada renovacao, como a Microsoft.
package onedrivetest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// Server e o OneDrive falso. Approve libera o login pendente (como a
// pessoa digitando o codigo em microsoft.com/devicelogin).
type Server struct {
	*httptest.Server
	mu       sync.Mutex
	items    map[string]*item
	sessions map[string]*session
	next     int
	approved bool
	refresh  int // refresh tokens emitidos
	Email    string
	Now      func() time.Time
	// PageSize e quantos itens cada pagina da lista traz (pra testar o
	// @odata.nextLink).
	PageSize int
	// Errors sao as violacoes do protocolo que o cliente cometeu (token
	// onde nao pode, secret num cliente publico, escopo errado).
	Errors []string
}

type item struct {
	ID, Name, Parent string
	Folder           bool
	Data             []byte
	Created          time.Time
}

type session struct {
	parent, name string
	size         int64
	data         []byte
}

// New sobe o servidor, com a pasta do app (approot) vazia.
func New() *Server {
	s := &Server{items: map[string]*item{}, sessions: map[string]*session{}, Email: "pessoa@outlook.com", Now: time.Now, PageSize: 200}
	s.items["approot"] = &item{ID: "approot", Name: "cais", Folder: true}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// Endpoint e o login do servidor falso.
func (s *Server) Endpoint() oauth2.Endpoint {
	return oauth2.Endpoint{AuthURL: s.URL + "/authorize", TokenURL: s.URL + "/token", DeviceAuthURL: s.URL + "/devicecode"}
}

// API e o endereco do Graph falso.
func (s *Server) API() string { return s.URL + "/v1.0" }

// Approve libera o login de dispositivo pendente.
func (s *Server) Approve() {
	s.mu.Lock()
	s.approved = true
	s.mu.Unlock()
}

// Names sao os nomes dos arquivos (fora pastas), ordenados.
func (s *Server) Names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, it := range s.items {
		if !it.Folder {
			out = append(out, it.Name)
		}
	}
	sort.Strings(out)
	return out
}

// Data e o conteudo do arquivo de nome name.
func (s *Server) Data(name string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range s.items {
		if it.Name == name && !it.Folder {
			return it.Data
		}
	}
	return nil
}

// Add poe um arquivo pronto na pasta (ex: um backup velho pra retencao).
func (s *Server) Add(parent, name string, data []byte, created time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	id := fmt.Sprintf("i%d", s.next)
	s.items[id] = &item{ID: id, Name: name, Parent: parent, Data: data, Created: created}
}

// DeleteAll apaga tudo da pasta do app (a pessoa apagou a pasta).
func (s *Server) DeleteAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.items {
		if id != "approot" {
			delete(s.items, id)
		}
	}
}

// Refreshes e quantos refresh tokens o servidor ja emitiu.
func (s *Server) Refreshes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refresh
}

var (
	itemPath    = regexp.MustCompile(`^/v1\.0/me/drive/items/([^/:]+)$`)
	childrenRe  = regexp.MustCompile(`^/v1\.0/me/drive/items/([^/:]+)/children$`)
	contentRe   = regexp.MustCompile(`^/v1\.0/me/drive/items/([^/:]+)/content$`)
	uploadRe    = regexp.MustCompile(`^/v1\.0/me/drive/items/([^/:]+):/([^/]+):/createUploadSession$`)
	approotName = regexp.MustCompile(`^/v1\.0/me/drive/special/approot:/([^/]+)$`)
	rangeRe     = regexp.MustCompile(`^bytes (\d+)-(\d+)/(\d+)$`)
)

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	js := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	fail := func(code int, msg string) {
		js(code, map[string]any{"error": map[string]any{"code": "x", "message": msg}})
	}
	meta := func(it *item) map[string]any {
		m := map[string]any{"id": it.ID, "name": it.Name, "size": len(it.Data), "createdDateTime": it.Created.UTC().Format(time.RFC3339)}
		if it.Folder {
			m["folder"] = map[string]any{"childCount": 0}
		} else {
			m["file"] = map[string]any{}
		}
		return m
	}
	p := r.URL.Path
	authed := strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")

	switch {
	case p == "/devicecode":
		r.ParseForm()
		if r.Form.Get("client_secret") != "" {
			s.Errors = append(s.Errors, "client_secret num cliente publico")
		}
		if !strings.Contains(r.Form.Get("scope"), "Files.ReadWrite.AppFolder") || !strings.Contains(r.Form.Get("scope"), "offline_access") {
			s.Errors = append(s.Errors, "escopo sem a pasta do app ou sem offline_access: "+r.Form.Get("scope"))
		}
		js(200, map[string]any{"device_code": "dev-ms", "user_code": "MSFT-1234", "verification_uri": "https://www.microsoft.com/link",
			"expires_in": 900, "interval": 1})
		return
	case p == "/token":
		r.ParseForm()
		if r.Form.Get("client_secret") != "" || strings.HasPrefix(r.Header.Get("Authorization"), "Basic ") {
			s.Errors = append(s.Errors, "secret no token de um cliente publico")
		}
		if r.Form.Get("client_id") == "" {
			s.Errors = append(s.Errors, "token sem client_id")
		}
		if r.Form.Get("grant_type") != "refresh_token" && !s.approved {
			js(400, map[string]any{"error": "authorization_pending"})
			return
		}
		s.refresh++
		js(200, map[string]any{"access_token": "acesso-" + strconv.Itoa(s.refresh), "refresh_token": "renova-" + strconv.Itoa(s.refresh),
			"token_type": "Bearer", "expires_in": 3600})
		return
	case strings.HasPrefix(p, "/upload/"):
		s.chunk(w, r, js, fail, meta, authed)
		return
	case strings.HasPrefix(p, "/download/"):
		if authed {
			s.Errors = append(s.Errors, "download com o token")
		}
		it := s.items[strings.TrimPrefix(p, "/download/")]
		if it == nil {
			fail(404, "itemNotFound")
			return
		}
		w.Write(it.Data)
		return
	case !authed:
		fail(401, "sem token")
		return
	}

	switch {
	case p == "/v1.0/me":
		js(200, map[string]any{"mail": nil, "userPrincipalName": s.Email})
	case approotName.MatchString(p) && r.Method == "GET":
		name := approotName.FindStringSubmatch(p)[1]
		for _, it := range s.items {
			if it.Parent == "approot" && it.Name == name {
				js(200, meta(it))
				return
			}
		}
		fail(404, "itemNotFound")
	case p == "/v1.0/me/drive/special/approot/children" && r.Method == "POST":
		var m struct {
			Name   string         `json:"name"`
			Folder map[string]any `json:"folder"`
		}
		json.NewDecoder(r.Body).Decode(&m)
		if m.Folder == nil {
			fail(400, "nao e pasta")
			return
		}
		s.next++
		it := &item{ID: fmt.Sprintf("i%d", s.next), Name: m.Name, Parent: "approot", Folder: true, Created: s.Now()}
		s.items[it.ID] = it
		js(201, meta(it))
	case uploadRe.MatchString(p) && r.Method == "POST":
		m := uploadRe.FindStringSubmatch(p)
		parent := s.items[m[1]]
		if parent == nil || !parent.Folder {
			fail(404, "itemNotFound")
			return
		}
		s.next++
		id := fmt.Sprintf("u%d", s.next)
		s.sessions[id] = &session{parent: m[1], name: m[2], size: -1}
		js(200, map[string]any{"uploadUrl": s.URL + "/upload/" + id})
	case childrenRe.MatchString(p) && r.Method == "GET":
		parent := childrenRe.FindStringSubmatch(p)[1]
		if s.items[parent] == nil {
			fail(404, "itemNotFound")
			return
		}
		var list []*item
		for _, it := range s.items {
			if it.Parent == parent {
				list = append(list, it)
			}
		}
		// de proposito fora de ordem: o cliente ordena
		sort.Slice(list, func(i, j int) bool { return list[i].Name > list[j].Name })
		skip, _ := strconv.Atoi(r.URL.Query().Get("$skiptoken"))
		end := min(len(list), skip+s.PageSize)
		var out []map[string]any
		for _, it := range list[skip:end] {
			out = append(out, meta(it))
		}
		res := map[string]any{"value": out}
		if end < len(list) {
			q := r.URL.Query()
			q.Set("$skiptoken", strconv.Itoa(end))
			res["@odata.nextLink"] = s.URL + p + "?" + q.Encode()
		}
		js(200, res)
	case contentRe.MatchString(p) && r.Method == "GET":
		id := contentRe.FindStringSubmatch(p)[1]
		if s.items[id] == nil {
			fail(404, "itemNotFound")
			return
		}
		http.Redirect(w, r, s.URL+"/download/"+id, http.StatusFound)
	case itemPath.MatchString(p):
		id := itemPath.FindStringSubmatch(p)[1]
		it := s.items[id]
		if it == nil {
			fail(404, "itemNotFound")
			return
		}
		if r.Method == "DELETE" {
			delete(s.items, id)
			w.WriteHeader(204)
			return
		}
		js(200, meta(it))
	default:
		fail(501, "nao implementado: "+r.Method+" "+p)
	}
}

// chunk recebe um pedaco de uma sessao de envio (ou o cancelamento dela).
func (s *Server) chunk(w http.ResponseWriter, r *http.Request, js func(int, any), fail func(int, string), meta func(*item) map[string]any, authed bool) {
	if authed {
		s.Errors = append(s.Errors, "pedaco do envio com o token")
	}
	id := strings.TrimPrefix(r.URL.Path, "/upload/")
	ses := s.sessions[id]
	if ses == nil {
		fail(404, "sessao nao existe")
		return
	}
	if r.Method == "DELETE" {
		delete(s.sessions, id)
		w.WriteHeader(204)
		return
	}
	m := rangeRe.FindStringSubmatch(r.Header.Get("Content-Range"))
	if m == nil {
		fail(400, "sem Content-Range")
		return
	}
	start, _ := strconv.ParseInt(m[1], 10, 64)
	end, _ := strconv.ParseInt(m[2], 10, 64)
	size, _ := strconv.ParseInt(m[3], 10, 64)
	data, _ := io.ReadAll(r.Body)
	if start != int64(len(ses.data)) || int64(len(data)) != end-start+1 || (ses.size >= 0 && ses.size != size) {
		fail(416, "pedaco fora de ordem")
		return
	}
	// o Graph exige pedacos multiplos de 320 KiB, menos o ultimo
	if end+1 < size && len(data)%(320*1024) != 0 {
		s.Errors = append(s.Errors, fmt.Sprintf("pedaco de %d bytes (nao e multiplo de 320 KiB)", len(data)))
	}
	ses.size = size
	ses.data = append(ses.data, data...)
	if int64(len(ses.data)) < size {
		js(202, map[string]any{"nextExpectedRanges": []string{fmt.Sprintf("%d-", len(ses.data))}})
		return
	}
	delete(s.sessions, id)
	// conflictBehavior replace: o mesmo nome troca o conteudo
	for _, it := range s.items {
		if it.Parent == ses.parent && it.Name == ses.name {
			it.Data = ses.data
			js(200, meta(it))
			return
		}
	}
	s.next++
	it := &item{ID: fmt.Sprintf("i%d", s.next), Name: ses.name, Parent: ses.parent, Data: ses.data, Created: s.Now()}
	s.items[it.ID] = it
	js(201, meta(it))
}
