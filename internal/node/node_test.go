package node

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestKeyIsCreatedOnceAndTheOriginIsStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "broker.key")
	k1, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := Load(path)
	if err != nil || !k1.Equal(k2) {
		t.Fatalf("second load: %v, same key %v", err, k1.Equal(k2))
	}
	o := Origin(k1.Public().(ed25519.PublicKey))
	if len(o) != 32 || o != Origin(k2.Public().(ed25519.PublicKey)) {
		t.Fatalf("origin %q", o)
	}
	other, _ := Load(filepath.Join(t.TempDir(), "other.key"))
	if Origin(other.Public().(ed25519.PublicKey)) == o {
		t.Fatal("two nodes, one origin")
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm()&0o077 != 0 {
			t.Fatalf("mode %v, err %v; want 0600", fi.Mode(), err)
		}
	}
}

func TestDamagedKeyFileIsAnErrorNotANewIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broker.key")
	if err := os.WriteFile(path, []byte(`{"v":1,"seed":"AAAA"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("a damaged key file silently produced a new identity")
	}
}
