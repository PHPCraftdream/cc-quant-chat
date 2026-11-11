// Package node is the identity of a broker: an Ed25519 key pair kept in a file outside the
// database, and the origin derived from its public key. Nothing is signed with it yet; it only
// names the node, so that rows written by different brokers can be told apart if databases are
// ever merged.
//
// The key file has no password on purpose: the broker starts without a human. It guards the node
// against other nodes, not against processes of the same user.
package node

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

type file struct {
	V    int    `json:"v"`
	Seed string `json:"seed"` // base64
}

// Origin is the node's name: the first 16 bytes of SHA-256(public key), hex (32 characters).
func Origin(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:16])
}

// Short is the first four characters of an origin: what follows the name of a member of another
// node (nick@abcd).
func Short(origin string) string {
	if len(origin) < 4 {
		return origin
	}
	return origin[:4]
}

// Load reads the key at path, creating it (mode 0600) on the first run.
func Load(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		var f file
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, err
		}
		seed, err := base64.StdEncoding.DecodeString(f.Seed)
		if err != nil || len(seed) != ed25519.SeedSize {
			return nil, errors.New("node: the key file is damaged: " + path)
		}
		return ed25519.NewKeyFromSeed(seed), nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	out, _ := json.MarshalIndent(file{V: 1, Seed: base64.StdEncoding.EncodeToString(priv.Seed())}, "", "  ")
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp") // 0600
	if err != nil {
		return nil, err
	}
	_, err = tmp.Write(append(out, '\n'))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return nil, err
	}
	return priv, nil
}
