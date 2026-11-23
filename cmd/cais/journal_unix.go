//go:build unix

package main

import (
	"fmt"
	"os"
	"syscall"
)

// stderrIsJournal diz se o stderr vai direto pro journald: o systemd poe
// em JOURNAL_STREAM o dispositivo e o inode do stream que ele deu ao
// servico. So a variavel nao basta: um terminal aberto pelo systemd a
// herda, e la o horario faz falta.
func stderrIsJournal() bool {
	var dev, ino uint64
	if _, err := fmt.Sscanf(os.Getenv("JOURNAL_STREAM"), "%d:%d", &dev, &ino); err != nil {
		return false
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(int(os.Stderr.Fd()), &st); err != nil {
		return false
	}
	return uint64(st.Dev) == dev && uint64(st.Ino) == ino
}
