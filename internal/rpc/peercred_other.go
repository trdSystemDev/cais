//go:build !linux

package rpc

import (
	"errors"
	"net"
)

// fora do Linux nao ha SO_PEERCRED: um socket com checagem de quem conecta
// recusa todo mundo (so desenvolvimento usa outros sistemas).
func peerCred(net.Conn) (Peer, error) {
	return Peer{}, errors.New("identificar quem conecta (SO_PEERCRED) so funciona no Linux")
}
