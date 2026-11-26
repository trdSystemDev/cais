package apps

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/trdSystemDev/cais/internal/docker"
)

// DeployLogFile e o log do ultimo deploy do app (em Root/<app>): os passos
// e a saida do build, escritos enquanto o deploy roda.
const DeployLogFile = "deploy.log"

// DeployLog e o nome que pede o log do deploy no lugar de um container
// (cais app logs <app> deploy, app_logs com container "deploy"). Nao colide
// com container: os do compose sao <projeto>-<service>-<n>.
const DeployLog = "deploy"

// deployLogLimit e o tamanho maximo do log de um deploy: build que imprime
// sem parar nao enche o disco.
const deployLogLimit = 8 << 20

// logTime e o horario de cada linha, no formato do docker logs
// --timestamps (a TUI e o MCP tratam igual).
const logTime = "2006-01-02T15:04:05.000000000Z07:00"

// deployLog escreve o log de um deploy: cada linha com o horario. Os
// passos do cais comecam com "==>"; o resto e a saida do docker. Falha ao
// gravar nao para o deploy (o log so acompanha). nil nao escreve nada.
type deployLog struct {
	mu      sync.Mutex
	f       *os.File
	now     func() time.Time
	partial []byte // linha da saida do docker ainda sem \n
	size    int
	cut     bool
}

// openDeployLog comeca o log do deploy do app, apagando o do anterior.
func (m Manager) openDeployLog(name string) *deployLog {
	f, err := os.OpenFile(filepath.Join(m.dir(name), DeployLogFile), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil
	}
	now := m.Now
	if now == nil {
		now = time.Now
	}
	return &deployLog{f: f, now: now}
}

// Step registra um passo do deploy.
func (l *deployLog) Step(format string, args ...any) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.flushPartial()
	for _, line := range strings.Split(strings.TrimRight(fmt.Sprintf(format, args...), "\n"), "\n") {
		l.line("==> " + line)
	}
}

// Write recebe a saida do docker (stdout e stderr): cada linha completa
// vai pro arquivo com o horario. O \r das barras de progresso vira quebra.
func (l *deployLog) Write(p []byte) (int, error) {
	if l == nil {
		return len(p), nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	data := append(l.partial, bytes.ReplaceAll(p, []byte("\r"), []byte("\n"))...)
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		if s := strings.TrimRight(string(data[:i]), " "); s != "" {
			l.line(s)
		}
		data = data[i+1:]
	}
	l.partial = append(l.partial[:0:0], data...)
	return len(p), nil
}

func (l *deployLog) flushPartial() {
	if s := strings.TrimSpace(string(l.partial)); s != "" {
		l.line(s)
	}
	l.partial = nil
}

func (l *deployLog) line(s string) {
	if l.cut {
		return
	}
	out := l.now().UTC().Format(logTime) + " " + s + "\n"
	if l.size+len(out) > deployLogLimit {
		l.cut = true
		out = l.now().UTC().Format(logTime) + " ==> (log cortado: passou de 8 MiB; o resto do deploy segue sem log)\n"
	}
	n, _ := io.WriteString(l.f, out)
	l.size += n
}

// Close termina o log.
func (l *deployLog) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.flushPartial()
	l.f.Close()
}

// ReadDeployLog devolve o log do ultimo deploy do app (o que esta rodando
// ou o que terminou): as ultimas opt.Tail linhas (padrao 200), so as
// depois de opt.Since (duracao como 10m ou horario RFC 3339).
func (m Manager) ReadDeployLog(name string, opt docker.LogsOptions) (string, error) {
	if _, err := m.Get(name); err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(m.dir(name), DeployLogFile))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(data) == 0 {
		lines = nil
	}
	if opt.Since != "" {
		since, err := parseSince(opt.Since, m.now())
		if err != nil {
			return "", err
		}
		kept := lines[:0]
		for _, l := range lines {
			at, _, _ := strings.Cut(l, " ")
			if t, err := time.Parse(time.RFC3339Nano, at); err != nil || !t.Before(since) {
				kept = append(kept, l)
			}
		}
		lines = kept
	}
	tail := opt.Tail
	if tail <= 0 {
		tail = 200
	}
	if len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	if len(lines) == 0 {
		return "", nil
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func (m Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func parseSince(s string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return now.Add(-d), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("since %q: use uma duracao (10m, 2h) ou um horario RFC 3339", s)
	}
	return t, nil
}
