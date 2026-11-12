// Package keys derives the keys of a nick from two secrets, one of the client (A) and one of the
// broker (B), so that neither side's data alone is enough to sign as the nick (docs/design-keys.md).
package keys

import (
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
)

// SecretSize is the size of A and of B: random, full entropy, so no password stretching is needed.
const SecretSize = 32

// Salts keep the two derivations apart: the auth key (from A only) and the nick key (from A and B).
const (
	saltAuth = "ccqc/auth/v1"
	saltSign = "ccqc/nick-key/v1"
)

// NewSecret returns SecretSize random bytes (a new A, or a new B).
func NewSecret() ([]byte, error) {
	b := make([]byte, SecretSize)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// info names the chat and the nick; a nick never holds 0x00, so the pair is unambiguous.
func info(chatID, nick string) string { return chatID + "\x00" + nick }

func seed(ikm []byte, salt, chatID, nick string) ed25519.PrivateKey {
	s, err := hkdf.Key(sha256.New, ikm, []byte(salt), info(chatID, nick), ed25519.SeedSize)
	if err != nil { // only a key longer than 255 hash blocks fails: not this one
		panic(err)
	}
	return ed25519.NewKeyFromSeed(s)
}

// AuthKey is the key a client proves A with, for one (chat, nick): from A alone, so the broker can
// keep its public half and recognize the holder of A without learning A.
func AuthKey(a []byte, chatID, nick string) (ed25519.PrivateKey, error) {
	if len(a) != SecretSize {
		return nil, errors.New("keys: the client secret has a wrong size")
	}
	return seed(a, saltAuth, chatID, nick), nil
}

// SignKey is the key of the nick for one chat: from A and B together, so it is held by nobody who
// has only one of them.
func SignKey(a, b []byte, chatID, nick string) (ed25519.PrivateKey, error) {
	if len(a) != SecretSize || len(b) != SecretSize {
		return nil, errors.New("keys: a secret has a wrong size")
	}
	ikm := make([]byte, 0, 2*SecretSize)
	ikm = append(append(ikm, a...), b...)
	defer Wipe(ikm)
	return seed(ikm, saltSign, chatID, nick), nil
}

// Wipe overwrites a secret in memory (best effort: Go may have copied it).
func Wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
