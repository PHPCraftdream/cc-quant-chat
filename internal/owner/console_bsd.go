//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package owner

import "golang.org/x/sys/unix"

const (
	ioctlGet = unix.TIOCGETA
	ioctlSet = unix.TIOCSETA
)
