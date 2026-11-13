package docker

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestPruneBuildCacheIdadeETamanho(t *testing.T) {
	var calls []string
	run := func(_ context.Context, _ string, args ...string) (string, error) {
		c := strings.Join(args, " ")
		calls = append(calls, c)
		if strings.Contains(c, "--max-used-space") {
			return "", errors.New("unknown flag: --max-used-space")
		}
		return "", nil
	}
	if err := pruneBuildCache(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"builder prune -f --filter until=168h",
		"builder prune -f --max-used-space 10gb",
		"builder prune -f --keep-storage 10gb", // docker antigo
	}
	if !slices.Equal(calls, want) {
		t.Errorf("chamadas %v", calls)
	}
}
