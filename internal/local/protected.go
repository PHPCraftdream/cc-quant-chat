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
