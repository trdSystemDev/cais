package github

import (
	"context"
	"fmt"
	"os"
	"testing"
)

func TestTmpPeekRepo(t *testing.T) {
	keyPath := os.Getenv("PEEK_KEY")
	if keyPath == "" {
		t.Skip()
	}
	pem, _ := os.ReadFile(keyPath)
	k, err := ParseKey(pem)
	if err != nil {
		t.Fatal(err)
	}
	s, err := App{ID: 5512022, Key: k}.Open(context.Background(), Repo{"trdSystemDev", "TRDSmartPodCast"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"docker-compose.producao.yaml", "docker-compose.yaml", "app/Dockerfile"} {
		data, err := s.ReadFile(context.Background(), p)
		fmt.Printf("===== %s (%v)\n%s\n", p, err, data)
	}
}
