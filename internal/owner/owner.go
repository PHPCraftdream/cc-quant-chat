// Package owner holds the owner's Ed25519 key: how it is created, kept on disk under a password
// and fingerprinted. The broker only ever sees the public key and signatures.
//
// The private key (its 32-byte seed) is encrypted with AES-256-GCM under a key derived from the
// password with PBKDF2-HMAC-SHA256; the public key is the cipher's additional data and is kept
// beside it in clear, so `ccqc owner status` can compare fingerprints without the password.
package owner

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	// Iterations of PBKDF2 for new keys.
	Iterations = 600_000
	minIter    = 1000
	maxIter    = 10_000_000
	kdfName    = "pbkdf2-sha256"
)

// ErrNoConsole: there is no console to ask on. Passwords are only read from one.
var ErrNoConsole = errors.New("no console: run this command in your own terminal (the password is never read from stdin, arguments or the environment)")

// ErrWrongPassword: the password does not open the key file (or the file was altered).
var ErrWrongPassword = errors.New("wrong password, or the key file is damaged")

// File is the key file: {v, kdf, iter, salt, nonce, ct, pub}, all binary fields in base64.
type File struct {
	V     int    `json:"v"`
	KDF   string `json:"kdf"`
	Iter  int    `json:"iter"`
	Salt  string `json:"salt"`
	Nonce string `json:"nonce"`
	CT    string `json:"ct"`
	Pub   string `json:"pub"`
}

var b64 = base64.StdEncoding

// Generate makes a new key pair and seals the private key under the password.
func Generate(password string, iter int) (ed25519.PrivateKey, File, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, File{}, err
	}
	f, err := Seal(priv, password, iter)
	return priv, f, err
}

// Seal encrypts priv's seed under the password.
func Seal(priv ed25519.PrivateKey, password string, iter int) (File, error) {
	if iter < minIter || iter > maxIter {
		return File{}, fmt.Errorf("owner: %d iterations is outside %d..%d", iter, minIter, maxIter)
	}
	if password == "" {
		return File{}, errors.New("owner: an empty password")
	}
	salt := make([]byte, 16)
	nonce := make([]byte, 12)
	if _, err := rand.Read(salt); err != nil {
		return File{}, err
	}
	if _, err := rand.Read(nonce); err != nil {
		return File{}, err
	}
	pub := priv.Public().(ed25519.PublicKey)
	gcm, err := cipherFor(password, salt, iter)
	if err != nil {
		return File{}, err
	}
	ct := gcm.Seal(nil, nonce, priv.Seed(), pub)
	return File{V: 1, KDF: kdfName, Iter: iter, Salt: b64.EncodeToString(salt),
		Nonce: b64.EncodeToString(nonce), CT: b64.EncodeToString(ct), Pub: b64.EncodeToString(pub)}, nil
}

func cipherFor(password string, salt []byte, iter int) (cipher.AEAD, error) {
	key, err := pbkdf2.Key(sha256.New, password, salt, iter, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// PublicKey is the key the file says it holds (not checked against the ciphertext).
func (f File) PublicKey() (ed25519.PublicKey, error) {
	pub, err := b64.DecodeString(f.Pub)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, errors.New("owner: the key file holds no valid public key")
	}
	return pub, nil
}

// Open decrypts the private key. A wrong password, a changed file or a seed that does not give
// the stored public key all fail with ErrWrongPassword.
func (f File) Open(password string) (ed25519.PrivateKey, error) {
	if f.V != 1 || f.KDF != kdfName || f.Iter < minIter || f.Iter > maxIter {
		return nil, errors.New("owner: unsupported key file")
	}
	pub, err := f.PublicKey()
	if err != nil {
		return nil, err
	}
	salt, e1 := b64.DecodeString(f.Salt)
	nonce, e2 := b64.DecodeString(f.Nonce)
	ct, e3 := b64.DecodeString(f.CT)
	if e1 != nil || e2 != nil || e3 != nil {
		return nil, errors.New("owner: the key file is damaged")
	}
	gcm, err := cipherFor(password, salt, f.Iter)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, errors.New("owner: the key file is damaged")
	}
	seed, err := gcm.Open(nil, nonce, ct, pub)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, ErrWrongPassword
	}
	priv := ed25519.NewKeyFromSeed(seed)
	if !priv.Public().(ed25519.PublicKey).Equal(ed25519.PublicKey(pub)) {
		return nil, ErrWrongPassword
	}
	return priv, nil
}

// Fingerprint names a public key for people to compare: "SHA256:" and the base64 of its hash.
func Fingerprint(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(h[:])
}

// Load reads a key file.
func Load(path string) (File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return File{}, err
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return File{}, fmt.Errorf("owner: %s is not a key file: %w", path, err)
	}
	return f, nil
}

// Save writes the key file, readable by the user only, replacing any earlier one atomically.
func Save(path string, f File) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp") // 0600
	if err != nil {
		return err
	}
	_, err = tmp.Write(append(raw, '\n'))
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

// Encode and Decode: keys and signatures travel as base64.
func Encode(b []byte) string { return b64.EncodeToString(b) }

func Decode(s string) ([]byte, error) { return b64.DecodeString(s) }
