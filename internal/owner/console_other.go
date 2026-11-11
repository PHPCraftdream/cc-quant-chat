//go:build !windows && !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package owner

// ReadSecret is not available here: secrets are only read from a console.
func ReadSecret(string) (string, error) { return "", ErrNoConsole }
