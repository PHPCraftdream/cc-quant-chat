//go:build darwin

package ipc

import (
	"net"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// PeerOf: the pid of the other end of a unix socket (LOCAL_PEERPID) and the name of its program.
func PeerOf(c net.Conn) (Peer, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return Peer{}, ErrNoPeer
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return Peer{}, err
	}
	pid, serr := 0, error(nil)
	if err := raw.Control(func(fd uintptr) { pid, serr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID) }); err != nil {
		return Peer{}, err
	}
	if serr != nil {
		return Peer{}, serr
	}
	p := Peer{PID: pid}
	if out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output(); err == nil {
		p.Image = strings.TrimSpace(string(out))
	}
	return p, nil
}
