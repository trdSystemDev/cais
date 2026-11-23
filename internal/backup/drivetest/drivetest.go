// Package drivetest e um Google Drive (e o OAuth de dispositivo do Google)
// falso pros testes: pastas, envio resumable, lista, download e exclusao,
// em memoria.
package drivetest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// Server e o Drive falso. Approve libera o login pendente (como a pessoa
// digitando o codigo no google.com/device).
type Server struct {
	*httptest.Server
	mu       sync.Mutex
	files    map[string]*file
	next     int
	approved bool
	Email    string
	Now      func() time.Time
	// Fail faz a proxima requisicao que casar com o prefixo (ex: "PUT")
	// devolver 500.
	Fail string
	// down faz toda requisicao que casar com o prefixo falhar (Drive fora
	// do ar) ate SetDown("").
	down string
}

// SetDown faz toda requisicao que casar com o prefixo (ex: "POST /upload")
// devolver 503 ate SetDown(""): o Drive fora do ar.
func (s *Server) SetDown(prefix string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.down = prefix
}

type file struct {
	ID, Name, Parent, Mime string
	Data                   []byte
	Created                time.Time
	Trashed                bool
}

// New sobe o servidor.
func New() *Server {
	s := &Server{files: map[string]*file{}, Email: "pessoa@exemplo.com", Now: time.Now}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// Endpoint e o OAuth do servidor falso.
func (s *Server) Endpoint() oauth2.Endpoint {
	return oauth2.Endpoint{AuthURL: s.URL + "/auth", TokenURL: s.URL + "/token", DeviceAuthURL: s.URL + "/device/code"}
}

// API e Upload sao os enderecos do Drive falso.
func (s *Server) API() string    { return s.URL + "/drive/v3" }
func (s *Server) Upload() string { return s.URL + "/upload/drive/v3" }

// Approve libera o login de dispositivo pendente.
func (s *Server) Approve() {
	s.mu.Lock()
	s.approved = true
	s.mu.Unlock()
}

// Names sao os nomes dos arquivos (fora pastas e lixeira), ordenados.
func (s *Server) Names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, f := range s.files {
		if f.Mime == "" && !f.Trashed {
			out = append(out, f.Name)
		}
	}
	sort.Strings(out)
	return out
}

// Data e o conteudo do arquivo de nome name.
func (s *Server) Data(name string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.files {
		if f.Name == name {
			return f.Data
		}
	}
	return nil
}

// Add poe um arquivo pronto na pasta (ex: um backup velho pra retencao).
func (s *Server) Add(parent, name string, data []byte, created time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	id := fmt.Sprintf("f%d", s.next)
	s.files[id] = &file{ID: id, Name: name, Parent: parent, Data: data, Created: created}
}

// TrashAll manda tudo pra lixeira (a pessoa apagou a pasta no Drive).
func (s *Server) TrashAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.files {
		f.Trashed = true
	}
}

var parentRe = regexp.MustCompile(`'([^']+)' in parents`)

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Fail != "" && strings.HasPrefix(r.Method+" "+r.URL.Path, s.Fail) {
		s.Fail = ""
		http.Error(w, `{"error":{"message":"falha de teste"}}`, http.StatusInternalServerError)
		return
	}
	if s.down != "" && strings.HasPrefix(r.Method+" "+r.URL.Path, s.down) {
		http.Error(w, `{"error":{"message":"fora do ar"}}`, http.StatusServiceUnavailable)
		return
	}
	js := func(v any) { w.Header().Set("Content-Type", "application/json"); json.NewEncoder(w).Encode(v) }
	meta := func(f *file) map[string]any {
		return map[string]any{"id": f.ID, "name": f.Name, "size": fmt.Sprint(len(f.Data)), "createdTime": f.Created.UTC().Format(time.RFC3339), "trashed": f.Trashed}
	}
	p := r.URL.Path
	switch {
	case p == "/device/code":
		js(map[string]any{"device_code": "dev-123", "user_code": "ABCD-EFGH", "verification_url": "https://www.google.com/device", "expires_in": 1800, "interval": 1})
	case p == "/token":
		r.ParseForm()
		if r.Form.Get("grant_type") == "refresh_token" {
			js(map[string]any{"access_token": "acesso-2", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		if !s.approved {
			w.WriteHeader(400)
			js(map[string]any{"error": "authorization_pending"})
			return
		}
		js(map[string]any{"access_token": "acesso-1", "refresh_token": "renova-1", "token_type": "Bearer", "expires_in": 3600})
	case !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer "):
		http.Error(w, `{"error":{"message":"sem token"}}`, http.StatusUnauthorized)
	case p == "/drive/v3/about":
		js(map[string]any{"user": map[string]any{"emailAddress": s.Email}})
	case p == "/drive/v3/files" && r.Method == "POST":
		var m struct{ Name, MimeType string }
		json.NewDecoder(r.Body).Decode(&m)
		s.next++
		f := &file{ID: fmt.Sprintf("f%d", s.next), Name: m.Name, Mime: m.MimeType, Created: s.Now()}
		s.files[f.ID] = f
		js(meta(f))
	case p == "/drive/v3/files" && r.Method == "GET":
		m := parentRe.FindStringSubmatch(r.URL.Query().Get("q"))
		var list []*file
		for _, f := range s.files {
			if m != nil && f.Parent == m[1] && !f.Trashed {
				list = append(list, f)
			}
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Created.Before(list[j].Created) })
		var out []map[string]any
		for _, f := range list {
			out = append(out, meta(f))
		}
		js(map[string]any{"files": out})
	case strings.HasPrefix(p, "/drive/v3/files/"):
		f, ok := s.files[strings.TrimPrefix(p, "/drive/v3/files/")]
		if !ok {
			http.Error(w, `{"error":{"message":"File not found"}}`, 404)
			return
		}
		switch {
		case r.Method == "DELETE":
			delete(s.files, f.ID)
			w.WriteHeader(204)
		case r.URL.Query().Get("alt") == "media":
			w.Write(f.Data)
		default:
			js(meta(f))
		}
	case p == "/upload/drive/v3/files" && r.Method == "POST":
		var m struct {
			Name    string
			Parents []string
		}
		json.NewDecoder(r.Body).Decode(&m)
		if len(m.Parents) != 1 || s.files[m.Parents[0]] == nil {
			http.Error(w, `{"error":{"message":"pasta invalida"}}`, 404)
			return
		}
		s.next++
		f := &file{ID: fmt.Sprintf("f%d", s.next), Name: m.Name, Parent: m.Parents[0], Created: s.Now(), Trashed: true} // so aparece com o PUT
		s.files[f.ID] = f
		w.Header().Set("Location", s.URL+"/upload/session/"+f.ID)
	case strings.HasPrefix(p, "/upload/session/") && r.Method == "PUT":
		f := s.files[strings.TrimPrefix(p, "/upload/session/")]
		data, _ := io.ReadAll(r.Body)
		if f == nil || int64(len(data)) != r.ContentLength {
			http.Error(w, "envio incompleto", 400)
			return
		}
		f.Data, f.Trashed = data, false
		js(meta(f))
	default:
		http.Error(w, "nao implementado: "+r.Method+" "+p, http.StatusNotImplemented)
	}
}
