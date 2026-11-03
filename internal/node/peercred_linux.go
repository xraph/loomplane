package node

import (
	"errors"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// peerAllowed reports whether the process on the other end of c is root or
// runs as the same user as this node.
func peerAllowed(c net.Conn) (bool, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return false, errors.New("not a unix connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return false, err
	}
	var cred *unix.Ucred
	var serr error
	if err := raw.Control(func(fd uintptr) {
		cred, serr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) //nolint:gosec // fd is a socket descriptor
	}); err != nil {
		return false, err
	}
	if serr != nil {
		return false, serr
	}
	return cred.Uid == 0 || int(cred.Uid) == os.Geteuid(), nil
}
