package rpc

import (
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

// DefaultSocketPath e onde o daemon escuta se nada for informado. Pode ser
// trocado pela variavel CAIS_SOCKET ou pela flag --socket.
func DefaultSocketPath() string {
	if p := os.Getenv("CAIS_SOCKET"); p != "" {
		return p
	}
	if runtime.GOOS == "linux" {
		return "/run/cais/cais.sock"
	}
	// fora do Linux e so desenvolvimento local
	return filepath.Join(os.TempDir(), "cais.sock")
}

// Listen cria o socket do daemon.
//
// Quem consegue abrir o socket controla a VPS inteira (como o docker.sock),
// por isso ele nasce com permissao 0660: so o dono (root) e, se group for
// informado, os membros desse grupo.
//
// Se ja existir um socket no caminho, Listen tenta conectar: se alguem
// responde, ja ha um daemon rodando e o erro e devolvido; se ninguem
// responde, e sobra de um daemon que morreu e o arquivo e removido.
func Listen(path, group string) (net.Listener, error) {
	if err := CheckSocketPath(path); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("criando pasta do socket: %w", err)
	}

	if _, err := os.Lstat(path); err == nil {
		if conn, err := net.DialTimeout("unix", path, time.Second); err == nil {
			conn.Close()
			return nil, fmt.Errorf("ja existe um daemon ouvindo em %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("removendo socket antigo %s: %w", path, err)
		}
	}

	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}

	if err := os.Chmod(path, 0o660); err != nil {
		ln.Close()
		return nil, fmt.Errorf("ajustando permissao do socket: %w", err)
	}
	if group != "" {
		if err := chownGroup(path, group); err != nil {
			ln.Close()
			return nil, err
		}
	}
	return ln, nil
}

// maxSocketPath e o limite de sun_path (108 bytes com o terminador nulo)
// no Linux e no Windows. Acima disso o sistema so devolve "invalid
// argument", que nao diz o que esta errado.
const maxSocketPath = 107

// CheckSocketPath recusa caminhos que o sistema nao aceita como socket.
func CheckSocketPath(path string) error {
	if len(path) > maxSocketPath {
		return fmt.Errorf("caminho do socket tem %d caracteres, o maximo e %d: %s", len(path), maxSocketPath, path)
	}
	return nil
}

func chownGroup(path, group string) error {
	g, err := user.LookupGroup(group)
	if err != nil {
		return fmt.Errorf("grupo %q: %w", group, err)
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return fmt.Errorf("gid invalido %q do grupo %q", g.Gid, group)
	}
	if err := os.Chown(path, -1, gid); err != nil {
		return fmt.Errorf("passando o socket pro grupo %q: %w", group, err)
	}
	return nil
}
