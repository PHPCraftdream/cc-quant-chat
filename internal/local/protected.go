package local

import (
	"path/filepath"
	"sort"
)

// Which nicks of this machine have a protected key (docs/design-keys.md). The key itself is nowhere:
// it is derived from the client secret and the broker's. This file only says that the nick is
// protected, so `ccqc keys status` can tell, and holds nothing secret.

func protectedPath() string { return filepath.Join(home(), "protected-nicks.json") }

// MarkProtected records that the nick of a chat has a protected key.
func MarkProtected(chatID, nick string) error {
	return withLock(func() error {
		m := map[string]string{}
		if err := readJSON(protectedPath(), &m); err != nil {
			return err
		}
		m[chatID+"/"+nick] = now()
		return writeJSON(protectedPath(), m)
	})
}

// ProtectedNicks lists the nicks recorded as protected ("chat id/nick"), sorted.
func ProtectedNicks() ([]string, error) {
	m := map[string]string{}
	if err := withLock(func() error { return readJSON(protectedPath(), &m) }); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

// DropNickKeys removes what nick-keys.json holds for a nick (its private key in the clear and its
// old secret): done only after the broker has the protected key, so the nick is never left without one.
func DropNickKeys(chatID, nick string) error {
	return withLock(func() error {
		keys := map[string]string{}
		if err := readJSON(keysPath(), &keys); err != nil {
			return err
		}
		changed := false
		for _, k := range []string{chatID + "/" + nick, seedKey(chatID, nick)} {
			if _, ok := keys[k]; ok {
				delete(keys, k)
				changed = true
			}
		}
		if !changed {
			return nil
		}
		return writeJSON(keysPath(), keys)
	})
}

// PlainNickKey is the private key of a nick in nick-keys.json, if there is one (nil: none): it never
// makes one, unlike NickPrivateKey.
func PlainNickKey(chatID, nick string) ([]byte, bool, error) {
	keys := map[string]string{}
	if err := withLock(func() error { return readJSON(keysPath(), &keys) }); err != nil {
		return nil, false, err
	}
	s, ok := keys[seedKey(chatID, nick)]
	if !ok {
		return nil, false, nil
	}
	seed, err := decodeSecretSeed(s)
	return seed, err == nil, err
}
