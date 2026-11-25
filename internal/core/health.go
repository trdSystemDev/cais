package core

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/trdSystemDev/cais/internal/config"
)

// ReleasesURL e a pagina dos releases do cais: o /latest redireciona pra
// tag do mais novo.
const ReleasesURL = "https://github.com/trdSystemDev/cais/releases"

// intervalos minimos entre consultas: o dashboard pede a cada abertura
const (
	releaseEvery = 6 * time.Hour
	certsEvery   = 10 * time.Minute
)

// Health e o que o dashboard avisa alem do estado dos apps e servicos: a
// validade dos certificados do proxy e se ha versao nova do cais.
type Health struct {
	Checking  bool      `json:"checking,omitempty"`
	CheckedAt time.Time `json:"checked_at,omitzero"`
	// Version e a versao do cais rodando ("dev" fora de um release).
	Version string `json:"version,omitempty"`
	// Latest e o release mais novo no GitHub (vazio: nao deu pra saber).
	Latest string `json:"latest,omitempty"`
	// Certs sao os certificados do Let's Encrypt no proxy, do que vence
	// primeiro pro ultimo.
	Certs      []Cert `json:"certs,omitempty"`
	CertsError string `json:"certs_error,omitempty"`
}

// Cert e um certificado emitido pro dominio.
type Cert struct {
	Domain   string    `json:"domain"`
	NotAfter time.Time `json:"not_after"`
}

// UpdateAvailable diz se o release mais novo e mais novo que o rodando.
func (h Health) UpdateAvailable() bool {
	return h.Latest != "" && h.Version != "" && h.Version != "dev" && newerVersion(h.Latest, h.Version)
}

type healthRuntime struct {
	mu        sync.Mutex
	releaseAt time.Time
	certsAt   time.Time
}

// CheckHealth confere os certificados do proxy e o release mais novo do
// cais (cada um no maximo de tempos em tempos) e publica em State.Health.
// Falhas viram parte do resultado, nao erro da chamada.
func (c *Core) CheckHealth(ctx context.Context) (Health, error) {
	r := &c.health
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, _ := c.Snapshot(ctx)
	h := cur.Health
	h.Version = c.deps.Version
	now := c.deps.Now()
	release := r.releaseAt.IsZero() || now.Sub(r.releaseAt) >= releaseEvery
	certs := r.certsAt.IsZero() || now.Sub(r.certsAt) >= certsEvery
	if !release && !certs {
		return h, nil
	}
	c.update(func(s *State) { s.Health.Checking = true })

	if release && c.deps.Version != "dev" && c.deps.Version != "" {
		if tag, err := c.deps.LatestRelease(ctx); err == nil {
			h.Latest, r.releaseAt = tag, now
		}
	} else if release {
		r.releaseAt = now
	}
	if certs {
		list, err := c.readCerts(ctx)
		h.Certs, h.CertsError, r.certsAt = list, "", now
		if err != nil {
			h.CertsError = firstLineErr(err)
		}
	}
	h.Checking, h.CheckedAt = false, now
	s := c.update(func(s *State) { s.Health = h })
	return s.Health, nil
}

// CheckUpdate e o CheckHealth sem esperar o intervalo do release: pergunta
// agora ao GitHub qual e o mais novo (o Verificar do dashboard). Abrir a TUI
// continua respeitando o intervalo.
func (c *Core) CheckUpdate(ctx context.Context) (Health, error) {
	c.health.mu.Lock()
	c.health.releaseAt = time.Time{}
	c.health.mu.Unlock()
	return c.CheckHealth(ctx)
}

// readCerts le o acme.json do Traefik (dentro do container) quando o proxy
// esta ligado com o Let's Encrypt; sem isso nao ha certificado pra vigiar.
func (c *Core) readCerts(ctx context.Context) ([]Cert, error) {
	cfg, err := config.Load(c.deps.ConfigPath)
	if err != nil || !cfg.LetsEncrypt() || !cfg.HasStack("proxy") {
		return nil, nil
	}
	out, err := c.deps.Exec(ctx, "proxy-traefik", "cat", "/letsencrypt/acme.json")
	if err != nil {
		return nil, fmt.Errorf("lendo os certificados do proxy: %w", err)
	}
	return parseACME([]byte(out))
}

// parseACME tira de cada certificado do acme.json o dominio e a validade.
func parseACME(b []byte) ([]Cert, error) {
	if strings.TrimSpace(string(b)) == "" {
		return nil, nil // nenhum certificado emitido ainda
	}
	var resolvers map[string]struct {
		Certificates []struct {
			Domain struct {
				Main string `json:"main"`
			} `json:"domain"`
			Certificate string `json:"certificate"`
		} `json:"Certificates"`
	}
	if err := json.Unmarshal(b, &resolvers); err != nil {
		return nil, fmt.Errorf("acme.json: %w", err)
	}
	var out []Cert
	for _, r := range resolvers {
		for _, ct := range r.Certificates {
			raw, err := base64.StdEncoding.DecodeString(ct.Certificate)
			if err != nil {
				continue
			}
			block, _ := pem.Decode(raw)
			if block == nil {
				continue
			}
			x, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				continue
			}
			out = append(out, Cert{Domain: ct.Domain.Main, NotAfter: x.NotAfter})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NotAfter.Before(out[j].NotAfter) })
	return out, nil
}

// latestRelease pergunta ao GitHub a tag do release mais novo, pelo
// redirecionamento do /latest (sem API, sem limite de consultas).
func latestRelease(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, ReleasesURL+"/latest", nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "/releases/tag/") {
		return "", errors.New("sem release publicado")
	}
	return path.Base(loc), nil
}

// newerVersion diz se a e mais nova que b (vX.Y.Z; o resto e ignorado).
func newerVersion(a, b string) bool {
	pa, pb := versionParts(a), versionParts(b)
	for i := range 3 {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func versionParts(v string) [3]int {
	var out [3]int
	v = strings.TrimPrefix(v, "v")
	v, _, _ = strings.Cut(v, "-")
	for i, p := range strings.SplitN(v, ".", 3) {
		out[i], _ = strconv.Atoi(p)
	}
	return out
}

func firstLineErr(err error) string {
	s, _, _ := strings.Cut(strings.TrimSpace(err.Error()), "\n")
	return s
}
