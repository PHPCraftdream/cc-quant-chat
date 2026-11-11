//go:build !windows

// Package ipc: local broker endpoint. Unix: socket with 0600 in a 0700 dir.
package ipc

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Path: CCQC_SOCK or <user cache dir>/ccqc/broker.sock.
func Path() string {
	if p := os.Getenv("CCQC_SOCK"); p != "" {
		return p
	}
	return filepath.Join(baseDir(), "broker.sock")
}

// baseDir: <user cache dir>/ccqc; without a cache dir, a per-user folder in the temp dir.
// listen checks that the folder is ours and private before using it.
func baseDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "ccqc-"+strconv.Itoa(os.Getuid()))
	}
	return filepath.Join(dir, "ccqc")
}

// checkPrivateDir: dir belongs to the current user and nobody else can enter it.
func checkPrivateDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || !ok || int(st.Uid) != os.Getuid() || fi.Mode().Perm()&0o077 != 0 {
		return errors.New(dir + " is not a private folder of the current user (want a directory owned by you with mode 0700)")
	}
	return nil
}

// checkSocketOwner refuses to talk to a socket another user created at our path.
func checkSocketOwner(p string) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if fi.Mode()&os.ModeSocket == 0 || !ok || int(st.Uid) != os.Getuid() {
		return errors.New(p + " is not a socket of the current user")
	}
	return nil
}

func dial(p string, timeout time.Duration) (net.Conn, error) {
	if err := checkSocketOwner(p); err != nil {
		return nil, err
	}
	return net.DialTimeout("unix", p, timeout)
}

func Listen() (net.Listener, error) { return listen(Path()) }

func Dial(timeout time.Duration) (net.Conn, error) {
	return dial(Path(), timeout)
}

// SessionPath: per-Claude-Code-session socket between `ccqc mcp` and `ccqc tail`.
func SessionPath(key int) string {
	return filepath.Join(baseDir(), "sess-"+strconv.Itoa(key)+".sock")
}

func ListenSession(key int) (net.Listener, error) { return listen(SessionPath(key)) }

func DialSession(key int, timeout time.Duration) (net.Conn, error) {
	return dial(SessionPath(key), timeout)
}

// held keeps listener locks open for the life of the process.
var held []*os.File

func listen(p string) (net.Listener, error) {
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if dir == baseDir() {
		if err := checkPrivateDir(dir); err != nil {
			return nil, err
		}
	}
	// The lock makes "check, remove stale socket, listen" atomic across processes:
	// without it two starting brokers could each remove the other's socket.
	lock, err := os.OpenFile(p+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("already listening: " + p)
	}
	_ = os.Remove(p) // stale socket of a dead process; the lock proves nobody owns it
	ln, err := net.Listen("unix", p)
	if err != nil {
		lock.Close()
		return nil, err
	}
	if err := os.Chmod(p, 0o600); err != nil {
		ln.Close()
		lock.Close()
		return nil, err
	}
	held = append(held, lock)
	return ln, nil
}
