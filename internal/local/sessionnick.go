package local

import (
	"path/filepath"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// Which Claude Code session holds a nick of a chat, as the sessions' files remember it: so a nick moved to a
// protected key from a terminal (ccqc keys migrate) is bound to the session that uses it, not left
// unbound.

// entryChatID is the id of the chat an entry is for.
func entryChatID(e Entry) string {
	if e.ChatID != "" {
		return e.ChatID
	}
	if e.Token != "" {
		if _, id, err := proto.ParseToken(e.Token); err == nil {
			return id
		}
	}
	return ""
}

// NickSession is the id of the most recently used session that has the nick of the chat ("" if none).
func NickSession(chatID, nick string) string {
	files, _ := filepath.Glob(filepath.Join(home(), "folders", "*.json"))
	best, bestUsed := "", ""
	for _, p := range files {
		var f folderFile
		if err := withLock(func() error { return readJSON(p, &f) }); err != nil {
			continue
		}
		for sid, s := range f.Sessions {
			for _, e := range s.Chats {
				if e.Name == nick && entryChatID(e) == chatID && (best == "" || s.Used > bestUsed) {
					best, bestUsed = sid, s.Used
				}
			}
		}
	}
	return best
}

// BindNick records, in every session that has the nick of the chat, that the nick has a protected key
// bound to that session now (as SetBinding does for one).
func BindNick(chatID, nick string) error {
	files, _ := filepath.Glob(filepath.Join(home(), "folders", "*.json"))
	for _, p := range files {
		err := withLock(func() error {
			var f folderFile
			if err := readJSON(p, &f); err != nil {
				return err
			}
			changed := false
			for _, s := range f.Sessions {
				for i := range s.Chats {
					if e := &s.Chats[i]; e.Name == nick && entryChatID(*e) == chatID {
						e.Protected, e.Bound, e.BoundVia = true, "", ""
						changed = true
					}
				}
			}
			if !changed {
				return nil
			}
			return writeJSON(p, f)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// TokenOfChat is the token of a chat of tokens that some session joined (by the chat's id).
func TokenOfChat(chatID string) (string, bool) {
	files, _ := filepath.Glob(filepath.Join(home(), "folders", "*.json"))
	best, bestUsed := "", ""
	for _, p := range files {
		var f folderFile
		if err := withLock(func() error { return readJSON(p, &f) }); err != nil {
			continue
		}
		for _, s := range f.Sessions {
			for _, e := range s.Chats {
				if e.Token != "" && entryChatID(e) == chatID && (best == "" || s.Used > bestUsed) {
					best, bestUsed = e.Token, s.Used
				}
			}
		}
	}
	return best, best != ""
}
