package docker

import "strings"

// stderrLimit e o maximo de stderr guardado de um comando: o fim, que e onde
// esta o erro. Um build verboso nao cresce a memoria do daemon sem limite.
const stderrLimit = 64 * 1024

// CmdError e a falha de um comando externo. A mensagem e o stderr (quem le
// ve o que o docker disse); a causa (saida com codigo, prazo estourado)
// continua na cadeia pra errors.Is e errors.As.
type CmdError struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *CmdError) Error() string {
	if e.Stderr != "" {
		return e.Stderr
	}
	return e.Err.Error()
}

func (e *CmdError) Unwrap() error { return e.Err }

// tailError mostra so as ultimas linhas da mensagem de outro erro, sem
// tira-lo da cadeia. A ultima linha vem primeiro: e nela que o docker diz o
// erro, e quem mostra so a primeira linha (a TUI) ve o motivo e nao o
// progresso ("Network x Creating"). As de antes vem depois, na ordem.
type tailError struct {
	err error
	n   int
}

func (e *tailError) Error() string {
	lines := strings.Split(strings.TrimSpace(e.err.Error()), "\n")
	if len(lines) > e.n {
		lines = lines[len(lines)-e.n:]
	}
	last := len(lines) - 1
	return strings.Join(append([]string{lines[last]}, lines[:last]...), "\n")
}

func (e *tailError) Unwrap() error { return e.err }

// tail fica com o fim da saida do compose: e la que esta o erro, e o comeco
// e so progresso de pull.
func tail(err error, n int) error {
	return &tailError{err: err, n: n}
}

// tailBuffer guarda so os ultimos limit bytes escritos.
type tailBuffer struct {
	limit int
	data  []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) >= b.limit {
		b.data = append(b.data[:0], p[len(p)-b.limit:]...)
		return n, nil
	}
	if extra := len(b.data) + len(p) - b.limit; extra > 0 {
		b.data = b.data[extra:]
	}
	b.data = append(b.data, p...)
	return n, nil
}

func (b *tailBuffer) String() string { return string(b.data) }
