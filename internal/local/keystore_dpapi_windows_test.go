//go:build windows

package local

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestOnWindowsTheSecretIsADPAPIBlobThatDoesNotHoldTheSecret(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CCQC_HOME", home)
	t.Setenv("CCQC_SECRET_DIR", t.TempDir())
	t.Setenv("CCQC_KEYSTORE", "")
	a, ks, err := ClientSecret()
	if err != nil || ks.Kind != KindDPAPI || ks.Warning != "" {
		t.Fatalf("%v %+v", err, ks)
	}
	raw, err := os.ReadFile(filepath.Join(home, "client-secret.dpapi"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, a) || bytes.Contains(raw, []byte(encodeSecret(a))) {
		t.Fatal("the blob holds the secret in the clear")
	}
	again, _, err := ClientSecret()
	if err != nil || !bytes.Equal(a, again) {
		t.Fatalf("the secret did not come back: %v", err)
	}
	// A blob that was changed does not open: the secret is not guessed or made again.
	damaged := append([]byte(nil), raw...)
	damaged[len(damaged)/2] ^= 1
	if err := os.WriteFile(filepath.Join(home, "client-secret.dpapi"), damaged, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ClientSecret(); err == nil {
		t.Fatal("a changed blob was accepted")
	}
}
