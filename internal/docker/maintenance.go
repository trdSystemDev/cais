package docker

import (
	"context"
	"fmt"
)

// PruneImages remove as imagens sem uso do projeto: no storage classico
// (overlay2) cada build deixa a versao anterior da imagem sem tag, e elas
// se acumulam deploy apos deploy. So as do projeto (label do compose): o
// que for de fora do cais nao e tocado.
func (CLI) PruneImages(ctx context.Context, project string) error {
	if _, err := execRunner(ctx, "docker", "image", "prune", "-f", "--filter", "label=com.docker.compose.project="+project); err != nil {
		return fmt.Errorf("docker image prune (%s): %w", project, tail(err, 5))
	}
	return nil
}

// BuildCacheMaxAge e BuildCacheMaxSize limitam o cache de build: e ele que
// mais cresce com deploy (cada build --pull guarda camadas novas) e nada o
// apaga sozinho.
const (
	BuildCacheMaxAge  = "168h" // uma semana sem uso
	BuildCacheMaxSize = "10gb"
)

// PruneBuildCache apaga o cache de build sem uso ha BuildCacheMaxAge e
// depois corta o que passar de BuildCacheMaxSize (o mais antigo primeiro).
// So perde velocidade no proximo build; nenhuma imagem ou container e
// tocado.
func PruneBuildCache(ctx context.Context) error {
	return pruneBuildCache(ctx, execRunner)
}

func pruneBuildCache(ctx context.Context, run runner) error {
	if _, err := run(ctx, "docker", "builder", "prune", "-f", "--filter", "until="+BuildCacheMaxAge); err != nil {
		return fmt.Errorf("docker builder prune: %w", tail(err, 5))
	}
	// --max-used-space so existe no buildx novo; o docker antigo chama de
	// --keep-storage
	if _, err := run(ctx, "docker", "builder", "prune", "-f", "--max-used-space", BuildCacheMaxSize); err != nil {
		if _, err := run(ctx, "docker", "builder", "prune", "-f", "--keep-storage", BuildCacheMaxSize); err != nil {
			return fmt.Errorf("docker builder prune: %w", tail(err, 5))
		}
	}
	return nil
}
