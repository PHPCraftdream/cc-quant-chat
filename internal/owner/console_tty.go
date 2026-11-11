//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package owner

import (
	"bufio"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// ReadSecret asks for a secret on the terminal, without echo. The terminal is opened directly
// (/dev/tty): never stdin, the environment or the arguments, so a program started by an agent
// without a controlling terminal can't be fed the secret. No terminal: ErrNoConsole.
func ReadSecret(prompt string) (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", ErrNoConsole
	}
	defer tty.Close()
	fd := int(tty.Fd())
	old, err := unix.IoctlGetTermios(fd, ioctlGet)
	if err != nil {
		return "", ErrNoConsole
	}
	quiet := *old
	quiet.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, ioctlSet, &quiet); err != nil {
		return "", err
	}
	defer unix.IoctlSetTermios(fd, ioctlSet, old)

	_, _ = tty.WriteString(prompt)
	line, err := bufio.NewReader(tty).ReadString('\n')
	_, _ = tty.WriteString("\n")
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
