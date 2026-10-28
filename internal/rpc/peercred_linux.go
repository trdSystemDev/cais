package rpc

import (
	"errors"
	"net"

	"golang.org/x/sys/unix"
)

// peerCred pergunta ao kernel quem esta do outro lado do socket (uid, gid
// e pid de quem conectou). Nao da pra falsificar: vem do SO_PEERCRED.
func peerCred(c net.Conn) (Peer, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return Peer{}, errors.New("conexao nao e de socket unix")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return Peer{}, err
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return Peer{}, err
	}
	if credErr != nil {
		return Peer{}, credErr
	}
	return Peer{UID: cred.Uid, GID: cred.Gid, PID: cred.Pid}, nil
}
