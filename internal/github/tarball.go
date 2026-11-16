package github

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Limites do codigo baixado: um repositorio de app tem bem menos.
const (
	maxTarball = 2 << 30 // 2 GiB comprimido
	maxEntries = 200_000
)

// Resolve devolve o commit (sha completo) de ref: branch, tag ou sha.
// ref vazio e o branch principal do repositorio.
func (s *Session) Resolve(ctx context.Context, ref string) (string, error) {
	if ref == "" {
		ref = s.DefaultBranch
	}
	// com o ETag da ultima resposta, ref sem commit novo volta 304, que nao
	// gasta o limite da API: e o que deixa o deploy automatico consultar
	// todos os apps a cada minuto
	rq := s.req(http.MethodGet, s.repoPath("/commits/"+url.PathEscape(ref)), "application/vnd.github.sha")
	s.mu.Lock()
	prev := s.etags[ref]
	s.mu.Unlock()
	if prev.etag != "" {
		rq.header = map[string]string{"If-None-Match": prev.etag}
	}
	res, err := s.app.send(ctx, rq)
	if err != nil {
		return "", err
	}
	if res.status == http.StatusNotModified && prev.sha != "" {
		return prev.sha, nil
	}
	err = res.check()
	if IsNotFound(err) || isUnprocessable(err) {
		return "", fmt.Errorf("%s nao existe em %s", ref, s.Repo)
	}
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(string(res.data))
	if len(sha) != 40 {
		return "", fmt.Errorf("resposta inesperada do GitHub pro commit de %s", ref)
	}
	if etag := res.header.Get("ETag"); etag != "" {
		s.mu.Lock()
		if s.etags == nil {
			s.etags = map[string]etagged{}
		}
		s.etags[ref] = etagged{etag: etag, sha: sha}
		s.mu.Unlock()
	}
	return sha, nil
}

// isUnprocessable e o 422 que o GitHub devolve pra sha que nao existe.
func isUnprocessable(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusUnprocessableEntity
}

// Download baixa o codigo do commit e extrai em dir (que nao pode existir).
// O conteudo e dado de fora: nada e gravado fora de dir, nem por caminho
// com .. nem por symlink.
func (s *Session) Download(ctx context.Context, commit, dir string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.app.base()+s.repoPath("/tarball/"+url.PathEscape(commit)), nil)
	if err != nil {
		return err
	}
	if err := s.app.Limits.wait(s.quota, s.app.now()); err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+s.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "cais")
	// o GitHub redireciona pro codeload com um token proprio na URL; o
	// cliente do Go nao repassa o Authorization pra outro host
	client := s.app.client()
	if client.Timeout < 30*60*1e9 {
		c := *client
		c.Timeout = 0 // o download pode ser grande; quem limita e o ctx
		client = &c
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("baixando o codigo: %w", err)
	}
	defer resp.Body.Close()
	if err := s.app.Limits.observe(s.quota, resp.StatusCode, resp.Header, s.app.now()); err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return &Error{Status: resp.StatusCode, Message: "baixando o codigo de " + s.Repo.String()}
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	if err := extract(io.LimitReader(resp.Body, maxTarball), dir); err != nil {
		os.RemoveAll(dir)
		return err
	}
	return nil
}

// extract descompacta o tar.gz do GitHub em root, tirando a pasta de cima
// (dono-repo-sha/).
func extract(r io.Reader, root string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("codigo baixado nao e tar.gz: %w", err)
	}
	defer gz.Close()
	root, err = filepath.Abs(root)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for n := 0; ; n++ {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lendo o codigo baixado: %w", err)
		}
		if n > maxEntries {
			return errors.New("codigo baixado com arquivos demais")
		}
		// tira o primeiro componente (pasta do commit)
		_, rel, ok := strings.Cut(h.Name, "/")
		if !ok || rel == "" {
			continue
		}
		target, err := safeJoin(root, rel)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := insideReal(root, filepath.Dir(target)); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if h.Mode&0o111 != 0 {
				mode = 0o755
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_EXCL, mode)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			f.Close()
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			// so symlink relativo que fica dentro do codigo
			if filepath.IsAbs(h.Linkname) {
				return fmt.Errorf("codigo baixado tem symlink absoluto: %s -> %s", rel, h.Linkname)
			}
			if _, err := safeJoin(root, filepath.Join(filepath.Dir(rel), h.Linkname)); err != nil {
				return fmt.Errorf("codigo baixado tem symlink pra fora: %s -> %s", rel, h.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(h.Linkname, target); err != nil {
				return err
			}
		default:
			// links fisicos, dispositivos, fifos: nao fazem parte de codigo
		}
	}
}

// safeJoin junta rel em root recusando o que sai de root.
func safeJoin(root, rel string) (string, error) {
	p := filepath.Join(root, rel)
	if p != root && !strings.HasPrefix(p, root+string(filepath.Separator)) {
		return "", fmt.Errorf("codigo baixado tem caminho pra fora da pasta: %s", rel)
	}
	return p, nil
}

// insideReal confere que dir, seguindo symlinks, continua dentro de root:
// um symlink do proprio codigo nao pode desviar a gravacao pra fora.
func insideReal(root, dir string) error {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	if real != realRoot && !strings.HasPrefix(real, realRoot+string(filepath.Separator)) {
		return fmt.Errorf("codigo baixado tenta gravar fora da pasta por symlink: %s", dir)
	}
	return nil
}

// maxCompare e o limite da resposta do compare: ela traz a lista de
// arquivos com os patches, que o cais nao usa mas nao da pra desligar.
const maxCompare = 64 << 20

// Comparison e a relacao entre dois commits (o compare do GitHub).
type Comparison struct {
	// Status: "ahead" (head tem commits que base nao tem), "behind" (head
	// ja esta no historico de base), "identical" ou "diverged".
	Status   string `json:"status"`
	AheadBy  int    `json:"ahead_by"`
	BehindBy int    `json:"behind_by"`
}

// Compare compara head com base (branch, tag ou sha).
func (s *Session) Compare(ctx context.Context, base, head string) (Comparison, error) {
	var c Comparison
	path := s.repoPath("/compare/" + url.PathEscape(base) + "..." + url.PathEscape(head) + "?per_page=1")
	rq := s.req(http.MethodGet, path, "")
	rq.limit = maxCompare
	_, err := s.app.do(ctx, rq, &c)
	if IsNotFound(err) {
		return c, fmt.Errorf("nao achei %s ou %s em %s", base, head, s.Repo)
	}
	if err == nil && c.Status == "" {
		err = fmt.Errorf("resposta inesperada do GitHub no compare de %s...%s", base, head)
	}
	return c, err
}

// Tree devolve o sha da arvore de arquivos do commit: commits com a mesma
// arvore tem exatamente o mesmo codigo.
func (s *Session) Tree(ctx context.Context, commit string) (string, error) {
	var out struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if _, err := s.call(ctx, http.MethodGet, s.repoPath("/git/commits/"+url.PathEscape(commit)), "", nil, &out); err != nil {
		return "", err
	}
	if out.Tree.SHA == "" {
		return "", fmt.Errorf("resposta inesperada do GitHub pra arvore de %s", commit)
	}
	return out.Tree.SHA, nil
}
