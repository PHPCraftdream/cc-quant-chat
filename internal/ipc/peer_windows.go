//go:build windows

package ipc

import (
	"net"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// PeerOf asks the kernel which process is at the client end of a named pipe the broker accepted.
func PeerOf(c net.Conn) (Peer, error) {
	h, ok := c.(interface{ Fd() uintptr })
	if !ok {
		return Peer{}, ErrNoPeer
	}
	var pid uint32
	if err := windows.GetNamedPipeClientProcessId(windows.Handle(h.Fd()), &pid); err != nil {
		return Peer{}, err
	}
	p := Peer{PID: int(pid)}
	ph, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil { // gone already, or not ours to look at: the pid is still known
		return p, nil
	}
	defer windows.CloseHandle(ph)
	buf := make([]uint16, 1024)
	size := uint32(len(buf))
	if windows.QueryFullProcessImageName(ph, 0, &buf[0], &size) == nil {
		p.Image = string(utf16.Decode(buf[:size]))
	}
	var created, exit, kernel, user windows.Filetime
	if windows.GetProcessTimes(ph, &created, &exit, &kernel, &user) == nil {
		p.Started = time.Unix(0, created.Nanoseconds())
	}
	return p, nil
}
