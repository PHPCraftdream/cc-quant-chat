//go:build linux

package ipc

import (
	"net"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// PeerOf asks the kernel which process is at the other end of a unix socket (SO_PEERCRED).
func PeerOf(c net.Conn) (Peer, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return Peer{}, ErrNoPeer
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return Peer{}, err
	}
	var cred *unix.Ucred
	var serr error
	if err := raw.Control(func(fd uintptr) { cred, serr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil {
		return Peer{}, err
	}
	if serr != nil {
		return Peer{}, serr
	}
	p := Peer{PID: int(cred.Pid)}
	dir := "/proc/" + strconv.Itoa(p.PID)
	if img, err := os.Readlink(dir + "/exe"); err == nil {
		p.Image = img
	}
	if fi, err := os.Stat(dir); err == nil {
		p.Started = fi.ModTime() // the directory appears when the process starts: close enough for a log
	}
	return p, nil
}
