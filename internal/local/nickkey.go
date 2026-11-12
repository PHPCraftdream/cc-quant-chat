package local

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sort"
	"strings"
)

// A nick is an Ed25519 key pair. The private seed lives in nick-keys.json next to the nick's old
// secret, under "<chat id>/<nick>/ed25519" (a nick can't contain "/", so no secret's key clashes).

func seedKey(chatID, nick string) string { return chatID + "/" + nick + "/ed25519" }

// NickPrivateKey returns the key of a nick of this machine, creating and saving it on first use.
// Two sessions asking at once get the same key.
func NickPrivateKey(chatID, nick string) (ed25519.PrivateKey, error) {
	var priv ed25519.PrivateKey
	err := withLock(func() error {
		keys := map[string]string{}
		if err := readJSON(keysPath(), &keys); err != nil {
			return err
		}
		if s, ok := keys[seedKey(chatID, nick)]; ok {
			seed, err := base64.RawURLEncoding.DecodeString(s)
			if err != nil || len(seed) != ed25519.SeedSize {
				return errors.New("nick-keys.json: the saved key of @" + nick + " is damaged")
			}
			priv = ed25519.NewKeyFromSeed(seed)
			return nil
		}
		seed := make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			return err
		}
		keys[seedKey(chatID, nick)] = base64.RawURLEncoding.EncodeToString(seed)
		if err := writeJSON(keysPath(), keys); err != nil {
			return err
		}
		priv = ed25519.NewKeyFromSeed(seed)
		return nil
	})
	return priv, err
}

// NickKeyRef is a nick of this machine as nick-keys.json knows it.
type NickKeyRef struct {
	ChatID, Nick string
	Seed         bool // a private key in the clear
	Secret       bool // the old secret of the nick (proto 3)
}

// NickKeyRefs lists the nicks that nick-keys.json holds something for, ordered by chat and nick.
func NickKeyRefs() ([]NickKeyRef, error) {
	keys := map[string]string{}
	if err := withLock(func() error { return readJSON(keysPath(), &keys) }); err != nil {
		return nil, err
	}
	by := map[string]*NickKeyRef{}
	for k := range keys {
		parts := strings.Split(k, "/")
		if len(parts) < 2 {
			continue
		}
		id := parts[0] + "/" + parts[1]
		r := by[id]
		if r == nil {
			r = &NickKeyRef{ChatID: parts[0], Nick: parts[1]}
			by[id] = r
		}
		if len(parts) == 3 && parts[2] == "ed25519" {
			r.Seed = true
		} else if len(parts) == 2 {
			r.Secret = true
		}
	}
	out := make([]NickKeyRef, 0, len(by))
	for _, r := range by {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ChatID != out[j].ChatID {
			return out[i].ChatID < out[j].ChatID
		}
		return out[i].Nick < out[j].Nick
	})
	return out, nil
}

// SetNickPrivateKey keeps the key of a nick of this machine, replacing the one it had (an invitation
// for a nick that exists replaces its key).
func SetNickPrivateKey(chatID, nick string, priv ed25519.PrivateKey) error {
	return withLock(func() error {
		keys := map[string]string{}
		if err := readJSON(keysPath(), &keys); err != nil {
			return err
		}
		keys[seedKey(chatID, nick)] = base64.RawURLEncoding.EncodeToString(priv.Seed())
		return writeJSON(keysPath(), keys)
	})
}
