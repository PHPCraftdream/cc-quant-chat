//go:build windows

// Package ipc: local broker endpoint. Windows: named pipe restricted to the current user.
package ipc

import (
	"errors"
	"fmt"
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

func Dial(timeout time.Duration) (net.Conn, error) { return dial(Path(), timeout) }

// SessionPath: per-Claude-Code-session pipe between `ccqc mcp` and `ccqc tail`.
func SessionPath(key int) string { return `\\.\pipe\ccqc-sess-` + strconv.Itoa(key) }

func ListenSession(key int) (net.Listener, error) { return listen(SessionPath(key)) }

func DialSession(key int, timeout time.Duration) (net.Conn, error) {
	return dial(SessionPath(key), timeout)
}

// dial connects and checks who created the pipe: any local user can create a pipe under our
// name while no broker runs, and tokens must not be sent to it.
func dial(path string, timeout time.Duration) (net.Conn, error) {
	c, err := winio.DialPipe(path, &timeout)
	if err != nil {
		return nil, err
	}
	if err := verifyOwner(c); err != nil {
		c.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// verifyOwner accepts a pipe owned by the current user or by Administrators (the owner of
// objects made by an elevated process of the same user). Anything unverifiable is refused.
func verifyOwner(c net.Conn) error {
	h, ok := c.(interface{ Fd() uintptr })
	if !ok {
		return errors.New("can't verify the pipe owner")
	}
	sd, err := windows.GetSecurityInfo(windows.Handle(h.Fd()), windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("can't read the pipe owner: %w", err)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return errors.New("pipe has no owner")
	}
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("can't read the current user: %w", err)
	}
	if owner.Equals(tu.User.Sid) {
		return nil
	}
	if admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid); err == nil && owner.Equals(admins) {
		return nil
	}
	return fmt.Errorf("the pipe was created by another user (%s); refusing to connect", owner)
}

func listen(path string) (net.Listener, error) {
	// Full access for the current user only, protected DACL. No user, no pipe: never fall back
	// to a default descriptor.
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("can't read the current user: %w", err)
	}
	sddl := "D:P(A;;GA;;;" + tu.User.Sid.String() + ")"
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
