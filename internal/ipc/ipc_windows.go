//go:build windows

// Package ipc: local broker endpoint. Windows: named pipe restricted to the current user.
package ipc

import (
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// Path: CCQC_PIPE (full path or bare pipe name) or \\.\pipe\ccqc-<user>.
func Path() string {
	if p := os.Getenv("CCQC_PIPE"); p != "" {
		if !strings.HasPrefix(p, `\\`) {
			p = `\\.\pipe\` + strings.TrimLeft(p, `\/.`)
		}
		return p
	}
	return `\\.\pipe\ccqc-` + sanitize(os.Getenv("USERNAME"))
}

func Listen() (net.Listener, error) { return listen(Path()) }

func Dial(timeout time.Duration) (net.Conn, error) {
	return winio.DialPipe(Path(), &timeout)
}

// SessionPath: per-Claude-Code-session pipe between `ccqc mcp` and `ccqc tail`.
func SessionPath(key int) string { return `\\.\pipe\ccqc-sess-` + strconv.Itoa(key) }

func ListenSession(key int) (net.Listener, error) { return listen(SessionPath(key)) }

func DialSession(key int, timeout time.Duration) (net.Conn, error) {
	return winio.DialPipe(SessionPath(key), &timeout)
}

func listen(path string) (net.Listener, error) {
	sddl := ""
	if tu, err := windows.GetCurrentProcessToken().GetTokenUser(); err == nil {
		// Full access for the current user only; protected DACL.
		sddl = "D:P(A;;GA;;;" + tu.User.Sid.String() + ")"
	}
	return winio.ListenPipe(path, &winio.PipeConfig{
		SecurityDescriptor: sddl,
		InputBufferSize:    64 << 10,
		OutputBufferSize:   64 << 10,
	})
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "user"
	}
	return b.String()
}
