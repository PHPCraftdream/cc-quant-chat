package local

import (
	"path/filepath"
	"sort"
)

// OwnerKeyPath is where the owner's key file lives: <CCQC_HOME>/owner.key.
func OwnerKeyPath() string { return filepath.Join(home(), "owner.key") }

// FindChatToken looks for the token of a chat among those the user's sessions joined (any
// folder), by the chat's local name. The most recently used session wins.
func FindChatToken(chat string) (string, bool) {
	files, _ := filepath.Glob(filepath.Join(home(), "folders", "*.json"))
	sort.Strings(files)
	best, bestUsed := "", ""
	for _, p := range files {
		var f folderFile
		if err := withLock(func() error { return readJSON(p, &f) }); err != nil {
			continue
		}
		for _, s := range f.Sessions {
			for _, e := range s.Chats {
				if e.Local() == chat && e.Token != "" && (best == "" || s.Used > bestUsed) {
					best, bestUsed = e.Token, s.Used
				}
			}
		}
	}
	return best, best != ""
}

// NodeKeyPath is where the broker's node key lives: <CCQC_HOME>/broker.key.
func NodeKeyPath() string { return filepath.Join(home(), "broker.key") }
