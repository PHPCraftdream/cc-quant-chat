package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// fileStore: A in a file of its own, readable by the user only.
type fileStore struct{ path string }

func (s *fileStore) get() ([]byte, error) {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrSecretNotFound
	}
	if err != nil {
		return nil, err
	}
	return decodeSecret(strings.TrimSpace(string(b)))
}

func (s *fileStore) put(a []byte) error { return writeFileAtomic(s.path, []byte(encodeSecret(a)+"\n")) }

// writeFileAtomic replaces the file through a private temp file in the same folder.
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp") // 0600
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return err
}

// runner runs a command: its output, its error output, its exit status (when it ran and ended with
// one); err only when it could not be run at all.
type runner func(stdin []byte, name string, args ...string) (out, errOut []byte, code int, err error)

var runCmd runner = execRunner

const cmdTimeout = 30 * time.Second // a locked keyring may ask for an unlock: do not wait for it forever

func execRunner(stdin []byte, name string, args ...string) ([]byte, []byte, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ctx.Err() == nil {
		return out.Bytes(), errOut.Bytes(), ee.ExitCode(), nil
	}
	if err != nil {
		return nil, nil, 0, err
	}
	return out.Bytes(), errOut.Bytes(), 0, nil
}

const (
	cmdService = "ccqc"
	cmdAccount = "client-secret-v1"
	keychainNF = 44 // security(1): errSecItemNotFound
)

// cmdStore: A in the keychain of macOS (security) or in libsecret (secret-tool). No Go dependency, no cgo.
type cmdStore struct {
	kind string
	run  runner
}

func (s *cmdStore) fail(what string, errOut []byte, code int) error {
	return fmt.Errorf("%s failed (exit %d): %s", what, code, strings.TrimSpace(string(errOut)))
}

func (s *cmdStore) put(a []byte) error {
	enc := encodeSecret(a)
	if s.kind == KindKeychain {
		// security takes the value only in its arguments: the processes of this user can see it for the
		// instant the command runs (the class of attacker the design does not claim to stop).
		_, e, code, err := s.run(nil, "security", "add-generic-password", "-U", "-s", cmdService, "-a", cmdAccount, "-w", enc)
		if err != nil {
			return err
		}
		if code != 0 {
			return s.fail("security add-generic-password", e, code)
		}
		return nil
	}
	_, e, code, err := s.run([]byte(enc), "secret-tool", "store", "--label=ccqc", "service", cmdService, "account", cmdAccount)
	if err != nil {
		return err
	}
	if code != 0 {
		return s.fail("secret-tool store", e, code)
	}
	return nil
}

func (s *cmdStore) get() ([]byte, error) {
	var out, e []byte
	var code int
	var err error
	if s.kind == KindKeychain {
		out, e, code, err = s.run(nil, "security", "find-generic-password", "-s", cmdService, "-a", cmdAccount, "-w")
		if err == nil && code == keychainNF {
			return nil, ErrSecretNotFound
		}
	} else {
		out, e, code, err = s.run(nil, "secret-tool", "lookup", "service", cmdService, "account", cmdAccount)
		if err == nil && code != 0 && len(bytes.TrimSpace(e)) == 0 { // nothing found: no output, no complaint
			return nil, ErrSecretNotFound
		}
	}
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, s.fail(s.kind+" lookup", e, code)
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, ErrSecretNotFound
	}
	return decodeSecret(strings.TrimSpace(string(out)))
}
