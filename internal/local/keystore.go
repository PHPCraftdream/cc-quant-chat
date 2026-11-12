package local

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/PHPCraftdream/cc-quant-chat/internal/keys"
)

// The client secret A (docs/design-keys.md): kept in the store of the OS where there is one, else in a
// file outside CCQC_HOME, with a warning. The nick keys of this machine are derived from it and from
// the broker's secret; nick-keys.json holds no private key once a nick is protected.

// Kinds of store, as written to keystore.json.
const (
	KindDPAPI         = "dpapi"          // Windows: a DPAPI blob of the current user in a file
	KindKeychain      = "keychain"       // macOS: the login keychain, through /usr/bin/security
	KindSecretService = "secret-service" // Linux: libsecret, through secret-tool
	KindFile          = "file"           // no store of the OS: a file outside CCQC_HOME
)

var (
	// ErrSecretNotFound: the store holds no client secret.
	ErrSecretNotFound = errors.New("the key store holds no client secret")
	// ErrSecretLost: keystore.json says there was a secret and the store no longer has it. A new one is
	// never made silently: the nicks protected by the old one need the owner's invitations again.
	ErrSecretLost = errors.New("the client secret is gone from the key store; the nicks it protected need new invitations from the owner")
)

// secretStore keeps one secret.
type secretStore interface {
	get() ([]byte, error) // ErrSecretNotFound when there is none
	put(a []byte) error
}

// Keystore says where the client secret is and how good that place is.
type Keystore struct {
	Kind    string
	Warning string // not empty when the place is weaker than a store of the OS
}

// KeystoreStatus is Keystore without touching anything: Planned is the kind that the next first use
// would take; Present says whether the secret can be read now.
type KeystoreStatus struct {
	Keystore
	Set     bool // keystore.json exists
	Present bool
	Planned string
}

type keystoreFile struct {
	V    int    `json:"v"`
	Kind string `json:"kind"`
}

var goos = runtime.GOOS

func keystorePath() string { return filepath.Join(home(), "keystore.json") }

// secretDir is where the file store keeps A: not in CCQC_HOME, so a copy of that folder (the broker's
// data lives there too) does not carry both halves.
func secretDir() string {
	if d := os.Getenv("CCQC_SECRET_DIR"); d != "" {
		return d
	}
	if goos == "windows" {
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "ccqc")
		}
	}
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "ccqc")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".local", "state", "ccqc")
	}
	return filepath.Join(os.TempDir(), "ccqc-secret")
}

func storeFor(kind string) (secretStore, error) {
	switch kind {
	case KindDPAPI:
		return newDPAPIStore(filepath.Join(home(), "client-secret.dpapi")), nil
	case KindKeychain, KindSecretService:
		return &cmdStore{kind: kind, run: runCmd}, nil
	case KindFile:
		return &fileStore{path: filepath.Join(secretDir(), "client-secret.key")}, nil
	}
	return nil, fmt.Errorf("keystore.json names an unknown store %q (made by a newer ccqc?)", kind)
}

// candidateKinds: the stores to try, best first; the file is the last resort. CCQC_KEYSTORE=file
// skips the store of the OS (servers without one, tests).
func candidateKinds() []string {
	if os.Getenv("CCQC_KEYSTORE") == "file" {
		return []string{KindFile}
	}
	switch goos {
	case "windows":
		return []string{KindDPAPI, KindFile}
	case "darwin":
		return []string{KindKeychain, KindFile}
	case "linux", "freebsd", "openbsd", "netbsd":
		return []string{KindSecretService, KindFile}
	}
	return []string{KindFile}
}

// ClientSecret returns A, making it on the first use: it is stored and read back before it is
// handed out, so a key never gets registered from a secret that can't be had again. A secret that is
// gone from the store it was put in is ErrSecretLost, not a reason to make another.
func ClientSecret() ([]byte, Keystore, error) {
	var a []byte
	var ks Keystore
	err := withLock(func() error {
		var kf keystoreFile
		if err := readJSON(keystorePath(), &kf); err != nil {
			return fmt.Errorf("keystore.json: %w", err)
		}
		var err error
		if kf.Kind != "" {
			st, err := storeFor(kf.Kind)
			if err != nil {
				return err
			}
			if a, err = st.get(); errors.Is(err, ErrSecretNotFound) {
				return ErrSecretLost
			} else if err != nil {
				return fmt.Errorf("the %s key store: %w", kf.Kind, err)
			}
			ks = Keystore{Kind: kf.Kind, Warning: warningFor(kf.Kind, "")}
			return nil
		}
		a, ks, err = createSecret()
		return err
	})
	return a, ks, err
}

func createSecret() ([]byte, Keystore, error) {
	var why error
	for _, kind := range candidateKinds() {
		st, err := storeFor(kind)
		if err == nil {
			var a []byte
			if a, err = keys.NewSecret(); err == nil {
				if err = st.put(a); err == nil {
					var got []byte
					if got, err = st.get(); err == nil && !bytes.Equal(got, a) {
						err = errors.New("what was read back is not what was stored")
					}
				}
				if err == nil {
					if err = writeJSON(keystorePath(), keystoreFile{V: 1, Kind: kind}); err == nil {
						return a, Keystore{Kind: kind, Warning: warningFor(kind, whyText(why))}, nil
					}
				}
				keys.Wipe(a)
			}
		}
		why = fmt.Errorf("%s: %w", kind, err)
	}
	return nil, Keystore{}, fmt.Errorf("no place for the client secret: %w", why)
}

func whyText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// warningFor: the file store is weaker (the secret and the broker's data are both readable by whoever
// copies the user's files), and says so; why is what kept the store of the OS out.
func warningFor(kind, why string) string {
	if kind != KindFile {
		return ""
	}
	w := "no key store of the OS is used: the client secret is kept in a file (" + filepath.Join(secretDir(), "client-secret.key") +
		"); whoever copies it together with the broker's data can sign as your nicks"
	if why != "" {
		w += " [" + why + "]"
	}
	return w
}

// available: whether the tool a store needs is on this machine.
func available(kind string) bool {
	switch kind {
	case KindKeychain:
		_, err := exec.LookPath("security")
		return err == nil
	case KindSecretService:
		_, err := exec.LookPath("secret-tool")
		return err == nil
	}
	return true
}

// Info describes the key store without creating anything.
func Info() (KeystoreStatus, error) {
	var st KeystoreStatus
	err := withLock(func() error {
		var kf keystoreFile
		if err := readJSON(keystorePath(), &kf); err != nil {
			return fmt.Errorf("keystore.json: %w", err)
		}
		for _, k := range candidateKinds() {
			if st.Planned = k; available(k) {
				break
			}
		}
		if kf.Kind == "" {
			return nil
		}
		st.Set, st.Kind = true, kf.Kind
		st.Warning = warningFor(kf.Kind, "")
		s, err := storeFor(kf.Kind)
		if err != nil {
			return err
		}
		a, err := s.get()
		st.Present = err == nil
		keys.Wipe(a)
		if err != nil && !errors.Is(err, ErrSecretNotFound) {
			return fmt.Errorf("the %s key store: %w", kf.Kind, err)
		}
		return nil
	})
	return st, err
}

func encodeSecret(a []byte) string { return base64.RawURLEncoding.EncodeToString(a) }

func decodeBlob(s string) ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) == 0 {
		return nil, errors.New("the stored client secret is damaged")
	}
	return b, nil
}

func decodeSecret(s string) ([]byte, error) {
	a, err := decodeBlob(s)
	if err != nil || len(a) != keys.SecretSize {
		return nil, errors.New("the stored client secret is damaged")
	}
	return a, nil
}
