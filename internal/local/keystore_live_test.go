package local

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The key stores of macOS and Linux against the real tools, not a stand-in for them. They need a keychain or a
// keyring that is unlocked and that nobody is asked about, which a developer's machine has no business changing, so the
// test runs only where CCQC_LIVE_KEYSTORE names the store (keychain, secret-service): the jobs of CI (.github/workflows/
// ci.yml) set it up and set the variable. Where it is set and the store does not work, the test FAILS and says why:
// it is never skipped for that, and the file store is never accepted in its place.
//
// The round: make A (stored and read back), read it again, see it present, take it out of the store behind ccqc's
// back (the tool's own delete), and ask again: ErrSecretLost, not a new secret.
func TestLiveKeyStore(t *testing.T) {
	kind := os.Getenv("CCQC_LIVE_KEYSTORE")
	if kind == "" {
		t.Skip("set CCQC_LIVE_KEYSTORE=keychain (macOS) or secret-service (Linux) to try the real key store")
	}
	want := map[string]string{"keychain": "darwin", "secret-service": "linux"}
	if sys, ok := want[kind]; !ok || sys != goos {
		t.Fatalf("CCQC_LIVE_KEYSTORE=%q does not fit this system (%s): want keychain on darwin, secret-service on linux", kind, goos)
	}
	if !available(kind) {
		t.Fatalf("the tool of the %s store is not on this machine (security / secret-tool not on PATH)", kind)
	}
	t.Setenv("CCQC_HOME", t.TempDir())
	secretDirForTest := t.TempDir()
	t.Setenv("CCQC_SECRET_DIR", secretDirForTest)
	t.Setenv("CCQC_KEYSTORE", "") // the store of the OS, not the file

	clear := func() {
		var code int
		var err error
		if kind == KindKeychain {
			_, _, code, err = runCmd(nil, "security", "delete-generic-password", "-s", cmdService, "-a", cmdAccount)
		} else {
			_, _, code, err = runCmd(nil, "secret-tool", "clear", "service", cmdService, "account", cmdAccount)
		}
		_ = code
		_ = err // nothing to take out is fine
	}
	clear() // an earlier run's leftover must not decide this one
	t.Cleanup(clear)

	a, ks, err := ClientSecret()
	if err != nil {
		t.Fatalf("making A in the %s store: %v", kind, err)
	}
	if ks.Kind != kind {
		t.Fatalf("A went to the %q store, not %q (the fallback took it): %s", ks.Kind, kind, ks.Warning)
	}
	if ks.Warning != "" {
		t.Fatalf("the store of the OS carries a warning: %s", ks.Warning)
	}
	if _, err := os.Stat(filepath.Join(secretDirForTest, "client-secret.key")); err == nil {
		t.Fatal("a file with the secret was made as well as the store")
	}

	again, ks2, err := ClientSecret()
	if err != nil || ks2.Kind != kind || !bytes.Equal(again, a) {
		t.Fatalf("reading A back from the %s store: kind %q, same %v, %v", kind, ks2.Kind, bytes.Equal(again, a), err)
	}
	st, err := Info()
	if err != nil || !st.Set || !st.Present || st.Kind != kind {
		t.Fatalf("Info: %+v %v", st, err)
	}
	if got, _, err := ClientSecretIfAny(); err != nil || !bytes.Equal(got, a) {
		t.Fatalf("ClientSecretIfAny: %v", err)
	}

	clear() // the secret goes from the store behind ccqc's back
	if _, _, err := ClientSecret(); !errors.Is(err, ErrSecretLost) {
		t.Fatalf("after the secret was taken out of the %s store: %v, want ErrSecretLost", kind, err)
	}
	st, err = Info()
	if err != nil || !st.Set || st.Present {
		t.Fatalf("Info after the loss: %+v %v", st, err)
	}
	if _, _, err := ClientSecretIfAny(); !errors.Is(err, ErrSecretLost) {
		t.Fatalf("ClientSecretIfAny after the loss: %v", err)
	}
}
