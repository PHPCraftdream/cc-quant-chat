package owner

import (
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const testIter = minIter // the real count (600000) is for real keys; tests keep to the minimum

func TestSealedKeyOpensOnlyWithThePassword(t *testing.T) {
	priv, f, err := Generate("correct horse", testIter)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.Open("correct horse")
	if err != nil || !got.Equal(priv) {
		t.Fatalf("right password: err %v, same key %v", err, got.Equal(priv))
	}
	for _, bad := range []string{"wrong", "", "correct horse "} {
		if _, err := f.Open(bad); !errors.Is(err, ErrWrongPassword) {
			t.Fatalf("password %q: err %v, want ErrWrongPassword", bad, err)
		}
	}
	sig := ed25519.Sign(got, []byte("m"))
	pub, _ := f.PublicKey()
	if !ed25519.Verify(pub, []byte("m"), sig) {
		t.Fatal("the opened key does not match the stored public key")
	}
}

func TestAlteredFileDoesNotOpen(t *testing.T) {
	_, f, err := Generate("pw", testIter)
	if err != nil {
		t.Fatal(err)
	}
	other, _, _ := Generate("pw2", testIter)
	swapped := f
	swapped.Pub = Encode(other.Public().(ed25519.PublicKey)) // the public key is authenticated data
	if _, err := swapped.Open("pw"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("swapped public key: %v", err)
	}
	flipped := f
	ct, _ := Decode(f.CT)
	ct[0] ^= 1
	flipped.CT = Encode(ct)
	if _, err := flipped.Open("pw"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("flipped ciphertext: %v", err)
	}
	weak := f
	weak.Iter = 1 // a rewritten iteration count is refused, not honored
	if _, err := weak.Open("pw"); err == nil {
		t.Fatal("a key file asking for 1 iteration was accepted")
	}
}

func TestEveryKeyHasItsOwnSaltAndNonce(t *testing.T) {
	_, a, _ := Generate("pw", testIter)
	_, b, _ := Generate("pw", testIter)
	if a.Salt == b.Salt || a.Nonce == b.Nonce || a.CT == b.CT {
		t.Fatal("two keys share salt, nonce or ciphertext")
	}
	if strings.Contains(a.CT, a.Pub) {
		t.Fatal("ciphertext holds the public key")
	}
}

func TestEmptyPasswordIsRefused(t *testing.T) {
	if _, _, err := Generate("", testIter); err == nil {
		t.Fatal("an empty password was accepted")
	}
	if _, _, err := Generate("x", 10); err == nil {
		t.Fatal("too few iterations were accepted")
	}
}

func TestFingerprintNamesTheKey(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	fp := Fingerprint(pub)
	if !strings.HasPrefix(fp, "SHA256:") || fp != Fingerprint(pub) {
		t.Fatalf("fingerprint %q", fp)
	}
	other, _, _ := ed25519.GenerateKey(nil)
	if Fingerprint(other) == fp {
		t.Fatal("two keys, one fingerprint")
	}
}

func TestSaveAndLoadRoundTripPrivately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "owner.key")
	priv, f, err := Generate("pw", testIter)
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(path, f); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil || fi.Mode().Perm()&0o077 != 0 {
			t.Fatalf("mode %v, err %v; want no access for group/others", fi.Mode(), err)
		}
	}
	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := back.Open("pw")
	if err != nil || !got.Equal(priv) {
		t.Fatalf("after a round trip: %v", err)
	}
	// Saving again replaces the file and leaves no temp files.
	if err := Save(path, f); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("%d files left in the folder, want 1", len(entries))
	}
}
