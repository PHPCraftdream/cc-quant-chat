package ipc

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// The kernel names the process at the other end: here, this test process, through the real endpoint.
func TestThePeerOfAConnectionIsTheProcessThatDialedAndItsImageIsKnown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("CCQC_PIPE", `ccqc-peertest-`+filepath.Base(t.TempDir()))
	} else {
		t.Setenv("CCQC_SOCK", filepath.Join(shortTemp(t), "b.sock"))
	}
	ln, err := Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan Peer, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			got <- Peer{}
			return
		}
		defer c.Close()
		p, err := PeerOf(c)
		if err != nil {
			t.Errorf("PeerOf: %v", err)
		}
		got <- p
	}()
	c, err := Dial(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var p Peer
	select {
	case p = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("no peer")
	}
	if p.PID != os.Getpid() {
		t.Fatalf("pid %d, want %d", p.PID, os.Getpid())
	}
	exe, _ := os.Executable()
	if p.Image == "" || filepath.Base(p.Image) != filepath.Base(exe) {
		t.Fatalf("image %q, want %q", p.Image, exe)
	}
	if s := p.String(); s == "unknown" || filepath.IsAbs(s) {
		t.Fatalf("the log line must name the program, not its path: %q", s)
	}
}

func TestAConnectionThatIsNotOfTheEndpointHasNoPeer(t *testing.T) {
	if _, err := PeerOf(nil); err == nil {
		t.Fatal("a nil connection has a peer")
	}
}

// shortTemp: unix socket paths are short.
func shortTemp(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "ccqc")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}
