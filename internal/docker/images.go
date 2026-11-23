package docker

import (
	"context"
	"fmt"
	"strings"
)

// TagImage poe a tag dst na imagem src (as duas continuam valendo).
func (CLI) TagImage(ctx context.Context, src, dst string) error {
	if _, err := execRunner(ctx, "docker", "image", "tag", src, dst); err != nil {
		return fmt.Errorf("docker image tag %s %s: %w", src, dst, tail(err, 3))
	}
	return nil
}

// HasImage diz se a imagem existe na maquina.
func (CLI) HasImage(ctx context.Context, ref string) bool {
	_, err := execRunner(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", ref)
	return err == nil
}

// ImageTags lista as tags (repositorio:tag) das imagens do projeto.
func (CLI) ImageTags(ctx context.Context, project string) ([]string, error) {
	out, err := execRunner(ctx, "docker", "image", "ls", "--filter", "label=com.docker.compose.project="+project,
		"--format", "{{.Repository}}:{{.Tag}}")
	if err != nil {
		return nil, fmt.Errorf("docker image ls (%s): %w", project, tail(err, 3))
	}
	var tags []string
	for _, t := range strings.Fields(out) {
		if !strings.Contains(t, "<none>") {
			tags = append(tags, t)
		}
	}
	return tags, nil
}

// RemoveImages tira as tags; a imagem so sai quando nao tem mais tag nem
// container usando.
func (CLI) RemoveImages(ctx context.Context, refs []string) error {
	if len(refs) == 0 {
		return nil
	}
	if _, err := execRunner(ctx, "docker", append([]string{"image", "rm"}, refs...)...); err != nil {
		return fmt.Errorf("docker image rm: %w", tail(err, 3))
	}
	return nil
}
