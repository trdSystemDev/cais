package buckets

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Como os apps chegam no Garage, pela rede Network.
const (
	Container = "garage"
	Endpoint  = "http://garage:3900"
	Region    = "garage"
	Network   = "garage_network"
)

// Exec roda um comando no container.
type Exec func(ctx context.Context, container string, args ...string) (string, error)

func garage(ctx context.Context, exec Exec, args ...string) (string, error) {
	return exec(ctx, Container, append([]string{"/garage"}, args...)...)
}

// CheckName confere o nome de um bucket: minusculas, numeros e -, de 3 a 63
// (vale no S3 e no endereco path-style). Sem _ e sem ponto: o nome vira
// nome de secret trocando - por _.
func CheckName(name string) error {
	if len(name) < 3 || len(name) > 63 || name[0] == '-' || name[len(name)-1] == '-' {
		return fmt.Errorf("nome de bucket invalido %q (3 a 63 letras minusculas, numeros e -)", name)
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return fmt.Errorf("nome de bucket invalido %q (3 a 63 letras minusculas, numeros e -)", name)
		}
	}
	return nil
}

// Key e a chave de acesso S3 de um bucket.
type Key struct {
	ID     string
	Secret string
}

// Create cria o bucket e uma chave de mesmo nome com leitura, escrita e
// dono (pra configurar CORS, por exemplo) so nesse bucket. A secret key sai
// pelo stdout do Garage, nunca na linha de comando.
func Create(ctx context.Context, exec Exec, name string) (Key, error) {
	if err := CheckName(name); err != nil {
		return Key{}, err
	}
	if _, err := garage(ctx, exec, "bucket", "create", name); err != nil {
		return Key{}, fmt.Errorf("criando o bucket %s: %w", name, err)
	}
	out, err := garage(ctx, exec, "key", "create", name)
	if err != nil {
		return Key{}, fmt.Errorf("bucket %s criado, mas a chave nao: %w", name, err)
	}
	key, err := parseKey(out)
	if err != nil {
		return Key{}, fmt.Errorf("bucket %s criado, mas a chave nao: %w", name, err)
	}
	if _, err := garage(ctx, exec, "bucket", "allow", "--read", "--write", "--owner", "--key", key.ID, name); err != nil {
		return Key{}, fmt.Errorf("liberando a chave no bucket %s: %w", name, err)
	}
	return key, nil
}

// parseKey le o Key ID e a Secret key da saida do "garage key create".
func parseKey(out string) (Key, error) {
	var k Key
	for _, line := range strings.Split(out, "\n") {
		label, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(label) {
		case "Key ID":
			k.ID = strings.TrimSpace(v)
		case "Secret key":
			k.Secret = strings.TrimSpace(v)
		}
	}
	if k.ID == "" || k.Secret == "" {
		return Key{}, errors.New("o garage nao devolveu o Key ID e a Secret key")
	}
	return k, nil
}

// Size e o espaco ocupado pelo bucket, como o Garage escreve (ex: 1.2 MB).
func Size(ctx context.Context, exec Exec, name string) (string, error) {
	out, err := garage(ctx, exec, "bucket", "info", name)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "Size:"); ok {
			v, _, _ = strings.Cut(strings.TrimSpace(v), " (")
			return v, nil
		}
	}
	return "", errors.New("o garage nao informou o tamanho")
}
