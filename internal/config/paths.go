package config

import (
	"os"
	"path/filepath"
	"runtime"
)

// Onde o cais guarda as coisas na VPS. Configuracao (editada por humano)
// fica em /etc/cais; estado gerenciado pelo cais fica em /var/lib/cais,
// fechado pro root. Ninguem fora do daemon escreve no DataDir: o deploy
// do CI passa pelo socket, nao pelo disco.
//
//	/etc/cais/config.yaml
//	/var/lib/cais/stacks/<stack>/docker-compose.yml
//	/var/lib/cais/apps/<app>/docker-compose.yml
//	/var/lib/cais/apps/<app>/secrets/
//	/var/lib/cais/secrets/           secrets centrais
//	/var/lib/cais/backups/
const (
	DefaultConfigDir = "/etc/cais"
	DefaultDataDir   = "/var/lib/cais"
)

// ConfigFile devolve o caminho do config.yaml. Pode ser trocado pela
// variavel CAIS_CONFIG.
func ConfigFile() string {
	if p := os.Getenv("CAIS_CONFIG"); p != "" {
		return p
	}
	if runtime.GOOS == "linux" {
		return filepath.Join(DefaultConfigDir, "config.yaml")
	}
	// fora do Linux e so desenvolvimento local
	return filepath.Join(os.TempDir(), "cais", "config.yaml")
}

// DataDir devolve a raiz do estado do cais. Pode ser trocada pela variavel
// CAIS_DATA_DIR (ex: pra testar no PC sem mexer em /var/lib).
func DataDir() string {
	if p := os.Getenv("CAIS_DATA_DIR"); p != "" {
		return p
	}
	if runtime.GOOS == "linux" {
		return DefaultDataDir
	}
	return filepath.Join(os.TempDir(), "cais", "data")
}

// StackDir e a pasta de uma stack base (proxy, postgres, mysql, registry, woodpecker).
func StackDir(name string) string { return filepath.Join(DataDir(), "stacks", name) }

// AppDir e a pasta de um app.
func AppDir(name string) string { return filepath.Join(DataDir(), "apps", name) }

// AppSecretsDir e a pasta de secrets de um app, a unica que o compose dele
// pode referenciar em "file:".
func AppSecretsDir(name string) string { return filepath.Join(AppDir(name), "secrets") }

// SecretsDir guarda os secrets centrais (senhas das instancias de banco etc).
func SecretsDir() string { return filepath.Join(DataDir(), "secrets") }

// BackupsDir guarda os backups gerados pelo cais.
func BackupsDir() string { return filepath.Join(DataDir(), "backups") }
