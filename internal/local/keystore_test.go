package local

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fileOnly: no store of the OS (CCQC_KEYSTORE=file), the folders private to the test.
func fileOnly(t *testing.T) (home, secrets string) {
	t.Helper()
	home, secrets = t.TempDir(), t.TempDir()
	t.Setenv("CCQC_HOME", home)
	t.Setenv("CCQC_SECRET_DIR", secrets)
	t.Setenv("CCQC_KEYSTORE", "file")
	return home, secrets
}

func TestTheClientSecretIsMadeOnceKeptOutsideTheHomeAndSharedBySessions(t *testing.T) {
	home, secrets := fileOnly(t)
	var wg sync.WaitGroup
	got := make([][]byte, 8)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, ks, err := ClientSecret()
			if err != nil || ks.Kind != KindFile || ks.Warning == "" {
				t.Errorf("%v %+v", err, ks)
			}
			got[i] = a
		}()
	}
	wg.Wait()
	for _, a := range got {
		if len(a) != 32 || !bytes.Equal(a, got[0]) {
			t.Fatal("sessions starting at once must get one secret")
		}
	}
	if _, err := os.Stat(filepath.Join(secrets, "client-secret.key")); err != nil {
		t.Fatalf("the secret is not where it was promised: %v", err)
	}
	enc := encodeSecret(got[0])
	filepath.WalkDir(home, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), enc) {
				t.Errorf("the secret is in %s, inside the home", p)
			}
		}
		return nil
	})
	again, _, err := ClientSecret()
	if err != nil || !bytes.Equal(again, got[0]) {
		t.Fatalf("the secret must persist: %v", err)
	}
}

func TestASecretGoneFromItsStoreIsNotMadeAgainSilently(t *testing.T) {
	home, secrets := fileOnly(t)
	if _, _, err := ClientSecret(); err != nil {
		t.Fatal(err)
	}
	kfile, _ := os.ReadFile(filepath.Join(home, "keystore.json"))
	if err := os.Remove(filepath.Join(secrets, "client-secret.key")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ClientSecret(); !errors.Is(err, ErrSecretLost) {
		t.Fatalf("got %v, want ErrSecretLost", err)
	}
	if _, err := os.Stat(filepath.Join(secrets, "client-secret.key")); err == nil {
		t.Fatal("a new secret was made behind the back")
	}
	if after, _ := os.ReadFile(filepath.Join(home, "keystore.json")); !bytes.Equal(after, kfile) {
		t.Fatal("keystore.json was rewritten")
	}
}

func TestADamagedSecretIsAnErrorAndNotANewSecret(t *testing.T) {
	_, secrets := fileOnly(t)
	if _, _, err := ClientSecret(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secrets, "client-secret.key"), []byte("not a secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ClientSecret(); err == nil || errors.Is(err, ErrSecretLost) {
		t.Fatalf("got %v", err)
	}
}

func TestInfoDoesNotCreateAnythingAndTellsWhatIsThere(t *testing.T) {
	home, _ := fileOnly(t)
	st, err := Info()
	if err != nil || st.Set || st.Present || st.Planned != KindFile {
		t.Fatalf("%+v %v", st, err)
	}
	if _, err := os.Stat(filepath.Join(home, "keystore.json")); err == nil {
		t.Fatal("Info made keystore.json")
	}
	if _, _, err := ClientSecret(); err != nil {
		t.Fatal(err)
	}
	if st, err = Info(); err != nil || !st.Set || !st.Present || st.Kind != KindFile || st.Warning == "" {
		t.Fatalf("%+v %v", st, err)
	}
}

// fake keeps what security / secret-tool would, and records what it was asked.
type fake struct {
	mu      sync.Mutex
	val     string
	calls   [][]string
	stdins  []string
	failAll string // stderr of every call, with exit status 1
}

func (f *fake) run(stdin []byte, name string, args ...string) ([]byte, []byte, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string{name}, args...))
	f.stdins = append(f.stdins, string(stdin))
	if f.failAll != "" {
		return nil, []byte(f.failAll), 1, nil
	}
	switch {
	case name == "security" && args[0] == "add-generic-password":
		f.val = args[len(args)-1]
		return nil, nil, 0, nil
	case name == "security":
		if f.val == "" {
			return nil, []byte("The specified item could not be found"), 44, nil
		}
		return []byte(f.val + "\n"), nil, 0, nil
	case args[0] == "store":
		f.val = string(stdin)
		return nil, nil, 0, nil
	default: // secret-tool lookup
		if f.val == "" {
			return nil, nil, 1, nil
		}
		return []byte(f.val), nil, 0, nil
	}
}

func withFake(t *testing.T, os string, f *fake) {
	t.Helper()
	oldRun, oldOS := runCmd, goos
	runCmd, goos = f.run, os
	t.Cleanup(func() { runCmd, goos = oldRun, oldOS })
}

func TestTheKeychainAndLibsecretStoresSpeakTheToolsAndLibsecretKeepsTheSecretOffTheCommandLine(t *testing.T) {
	for _, c := range []struct{ os, kind string }{{"darwin", KindKeychain}, {"linux", KindSecretService}} {
		t.Run(c.os, func(t *testing.T) {
			t.Setenv("CCQC_HOME", t.TempDir())
			t.Setenv("CCQC_SECRET_DIR", t.TempDir())
			t.Setenv("CCQC_KEYSTORE", "")
			f := &fake{}
			withFake(t, c.os, f)
			a, ks, err := ClientSecret()
			if err != nil || ks.Kind != c.kind || ks.Warning != "" {
				t.Fatalf("%v %+v", err, ks)
			}
			again, _, err := ClientSecret()
			if err != nil || !bytes.Equal(a, again) {
				t.Fatalf("the secret did not come back: %v", err)
			}
			enc := encodeSecret(a)
			for i, call := range f.calls {
				argv := strings.Join(call, " ")
				if c.kind == KindSecretService && strings.Contains(argv, enc) {
					t.Fatalf("libsecret: the secret is on the command line: %s", argv)
				}
				_ = i
			}
			if c.kind == KindSecretService && f.stdins[0] != enc {
				t.Fatalf("libsecret: the secret did not go through stdin: %q", f.stdins[0])
			}
		})
	}
}

func TestWithoutAStoreOfTheOSTheFileIsUsedWithAWarningThatNamesTheReason(t *testing.T) {
	_, secrets := fileOnly(t)
	t.Setenv("CCQC_KEYSTORE", "")
	f := &fake{failAll: "Cannot autolaunch D-Bus without X11 $DISPLAY"}
	withFake(t, "linux", f)
	a, ks, err := ClientSecret()
	if err != nil || ks.Kind != KindFile {
		t.Fatalf("%v %+v", err, ks)
	}
	if !strings.Contains(ks.Warning, "D-Bus") || !strings.Contains(ks.Warning, "no key store of the OS") {
		t.Fatalf("the warning does not say why: %q", ks.Warning)
	}
	if _, err := os.Stat(filepath.Join(secrets, "client-secret.key")); err != nil {
		t.Fatal(err)
	}
	// Later the store of the OS may work; the secret stays where it was put.
	f.failAll = ""
	if again, ks, err := ClientSecret(); err != nil || ks.Kind != KindFile || !bytes.Equal(a, again) {
		t.Fatalf("%v %+v", err, ks)
	}
}

func TestAMissingItemInTheKeychainIsNotFoundAndAnErrorIsAnError(t *testing.T) {
	f := &fake{}
	s := &cmdStore{kind: KindKeychain, run: f.run}
	if _, err := s.get(); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("%v", err)
	}
	t2 := &cmdStore{kind: KindSecretService, run: f.run}
	if _, err := t2.get(); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("%v", err)
	}
	f.failAll = "locked"
	if _, err := t2.get(); err == nil || errors.Is(err, ErrSecretNotFound) || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("%v", err)
	}
}
