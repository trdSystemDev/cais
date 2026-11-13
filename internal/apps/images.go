package apps

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
)

// builtImages sao as imagens que o build do compose gera: a do image: do
// service, ou <projeto>-<service> (o nome que o compose da).
func builtImages(configJSON []byte, project string) []string {
	var c composeConfig
	if json.Unmarshal(configJSON, &c) != nil {
		return nil
	}
	var out []string
	for _, n := range sortedKeys(c.Services) {
		s := c.Services[n]
		if s.Build == nil {
			continue
		}
		image := s.Image
		if image == "" {
			image = project + "-" + n
		}
		out = append(out, image)
	}
	return out
}

// commitTag e a tag guardada da imagem num commit: <repositorio>:cais-<commit>.
func commitTag(image, commit string) string {
	repo := image
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		repo = image[:i] // tira a tag (a porta do registry fica)
	}
	if len(commit) > 12 {
		commit = commit[:12]
	}
	return repo + ":cais-" + commit
}

// restoreImages poe de volta as imagens guardadas do commit no lugar das
// do build. So se todas existem: com uma faltando, o build faz tudo.
func (m Manager) restoreImages(ctx context.Context, images []string, commit string) bool {
	if len(images) == 0 {
		return false
	}
	for _, img := range images {
		if !m.Docker.HasImage(ctx, commitTag(img, commit)) {
			return false
		}
	}
	for _, img := range images {
		if m.Docker.TagImage(ctx, commitTag(img, commit), img) != nil {
			return false
		}
	}
	return true
}

// keepImages guarda as imagens do commit no ar e solta as dos deploys que
// sairam dos KeepImages ultimos. Falha aqui so custa um build no rollback.
func (m Manager) keepImages(ctx context.Context, name string, images []string, commit string) {
	for _, img := range images {
		m.Docker.TagImage(ctx, img, commitTag(img, commit))
	}
	a, err := m.Get(name)
	if err != nil {
		return
	}
	var keep []string // commits que guardam imagem
	for _, d := range a.History {
		if d.OK() && !slices.Contains(keep, d.Commit) && len(keep) < KeepImages {
			keep = append(keep, d.Commit)
		}
	}
	tags, err := m.Docker.ImageTags(ctx, name)
	if err != nil {
		return
	}
	var drop []string
	for _, t := range tags {
		_, tag, _ := strings.Cut(t[strings.LastIndex(t, "/")+1:], ":")
		c, ok := strings.CutPrefix(tag, "cais-")
		if !ok {
			continue
		}
		if !slices.ContainsFunc(keep, func(k string) bool { return strings.HasPrefix(k, c) }) {
			drop = append(drop, t)
		}
	}
	m.Docker.RemoveImages(ctx, drop)
}
