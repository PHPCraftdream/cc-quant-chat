//go:build !windows

// Package ipc: local broker endpoint. Unix: socket with 0600 in a 0700 dir.
package ipc

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Path: CCQC_SOCK or <user cache dir>/ccqc/broker.sock.
func Path() string {
	if p := os.Getenv("CCQC_SOCK"); p != "" {
		return p
	}
	return filepath.Join(baseDir(), "broker.sock")
}

func baseDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "ccqc")
}

func Listen() (net.Listener, error) { return listen(Path()) }

func Dial(timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("unix", Path(), timeout)
}

// SessionPath: per-Claude-Code-session socket between `ccqc mcp` and `ccqc tail`.
func SessionPath(key int) string {
	return filepath.Join(baseDir(), "sess-"+strconv.Itoa(key)+".sock")
}

func ListenSession(key int) (net.Listener, error) { return listen(SessionPath(key)) }

func DialSession(key int, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("unix", SessionPath(key), timeout)
}

func listen(p string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	if c, err := net.DialTimeout("unix", p, 200*time.Millisecond); err == nil {
		c.Close()
		return nil, errors.New("already listening: " + p)
	}
	_ = os.Remove(p)
	ln, err := net.Listen("unix", p)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(p, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}
