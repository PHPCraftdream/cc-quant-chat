//go:build windows

package owner

import (
	"errors"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// ReadSecret asks for a secret on the console, without echo. The console is opened directly
// (CONIN$ / CONOUT$): never stdin, the environment or the arguments, so a program started by an
// agent without a console of its own can't be fed the secret. No console: ErrNoConsole.
func ReadSecret(prompt string) (string, error) {
	in, err := windows.CreateFile(windows.StringToUTF16Ptr("CONIN$"), windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return "", ErrNoConsole
	}
	defer windows.CloseHandle(in)
	out, err := windows.CreateFile(windows.StringToUTF16Ptr("CONOUT$"), windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return "", ErrNoConsole
	}
	defer windows.CloseHandle(out)

	var mode uint32
	if err := windows.GetConsoleMode(in, &mode); err != nil {
		return "", ErrNoConsole
	}
	if err := windows.SetConsoleMode(in, mode&^windows.ENABLE_ECHO_INPUT); err != nil {
		return "", errors.New("owner: can't switch the console echo off")
	}
	defer windows.SetConsoleMode(in, mode)

	write := func(s string) {
		u := utf16.Encode([]rune(s))
		if len(u) == 0 {
			return
		}
		var n uint32
		_ = windows.WriteConsole(out, &u[0], uint32(len(u)), &n, nil)
	}
	write(prompt)
	var line []uint16
	buf := make([]uint16, 256)
	for {
		var n uint32
		if err := windows.ReadConsole(in, &buf[0], uint32(len(buf)), &n, nil); err != nil {
			write("\r\n")
			return "", err
		}
		line = append(line, buf[:n]...)
		if n == 0 || (len(line) > 0 && line[len(line)-1] == '\n') {
			break
		}
	}
	write("\r\n")
	return strings.TrimRight(string(utf16.Decode(line)), "\r\n"), nil
}
