package buckets

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCheckName(t *testing.T) {
	for _, ok := range []string{"fotos", "portal-arquivos", "a1b"} {
		if err := CheckName(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "ab", "Fotos", "com_underline", "com.ponto", "-fotos", "fotos-", strings.Repeat("a", 64)} {
		if CheckName(bad) == nil {
			t.Errorf("aceitou %q", bad)
		}
	}
}

func TestCreate(t *testing.T) {
	var calls []string
	exec := func(_ context.Context, container string, args ...string) (string, error) {
		calls = append(calls, container+" "+strings.Join(args, " "))
		if len(args) > 2 && args[1] == "key" {
			return "==== ACCESS KEY INFORMATION ====\nKey ID:              GK5d0e\nKey name:            fotos\nSecret key:          6c9b79\n", nil
		}
		return "", nil
	}
	k, err := Create(context.Background(), exec, "fotos")
	if err != nil || k.ID != "GK5d0e" || k.Secret != "6c9b79" {
		t.Fatalf("%+v %v", k, err)
	}
	want := []string{
		"garage /garage bucket create fotos",
		"garage /garage key create fotos",
		"garage /garage bucket allow --read --write --owner --key GK5d0e fotos",
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("chamadas\n%s", strings.Join(calls, "\n"))
	}
	if _, err := Create(context.Background(), exec, "Ruim"); err == nil {
		t.Error("aceitou nome invalido")
	}
}

func TestSize(t *testing.T) {
	exec := func(context.Context, string, ...string) (string, error) {
		return "==== BUCKET INFORMATION ====\nBucket:   c7da\n\nSize:            1.2 MB (1.1 MiB)\nObjects:         3\n", nil
	}
	if s, err := Size(context.Background(), exec, "fotos"); err != nil || s != "1.2 MB" {
		t.Errorf("%q %v", s, err)
	}
}

func TestDelete(t *testing.T) {
	var calls []string
	fail := map[string]error{}
	exec := func(_ context.Context, _ string, args ...string) (string, error) {
		calls = append(calls, strings.Join(args[1:3], " "))
		return "", fail[args[1]]
	}
	if err := Delete(context.Background(), exec, "fotos", "GK1"); err != nil || strings.Join(calls, ",") != "bucket delete,key delete" {
		t.Fatalf("%v %v", calls, err)
	}
	// ja apagados (repetir depois de uma falha)
	fail["bucket"] = errors.New("Error: GetBucketInfo returned NoSuchBucket (404)")
	fail["key"] = errors.New("Error: GetKeyInfo returned NoSuchAccessKey (404)")
	if err := Delete(context.Background(), exec, "fotos", "GK1"); err != nil {
		t.Errorf("repetido: %v", err)
	}
	fail["bucket"] = errors.New("Error: DeleteBucket returned BucketNotEmpty (409): Tried to delete a non-empty bucket")
	if err := Delete(context.Background(), exec, "fotos", "GK1"); err == nil || !strings.Contains(err.Error(), "tem arquivos") {
		t.Errorf("com arquivos: %v", err)
	}
}
