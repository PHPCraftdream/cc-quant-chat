package local

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// BRIDGE for the deployment of KEY-7 — delete it (and Entry.Token) right after that deployment.
//
// Until KEY-7 a session remembered a chat by its token. A chat is now joined by its id, and the token is no
// secret to keep: the first time a folder file is read, an entry that has a token becomes the entry of the chat
// (id = the hex SHA-256 of the token, as the broker always made it; name = the token's prefix) and the token is
// erased by rewriting the file. It is never printed or logged.

// bridgeEntry turns an entry of a token into the entry of its chat; false: it has no token.
func bridgeEntry(e *Entry) bool {
	if e.Token == "" {
		return false
	}
	tok := strings.TrimSpace(e.Token)
	if i := strings.LastIndexByte(tok, '.'); i > 0 {
		h := sha256.Sum256([]byte(tok))
		if e.ChatID == "" {
			e.ChatID = hex.EncodeToString(h[:])
		}
		if e.Chat == "" {
			e.Chat = tok[:i]
		}
	}
	e.Token = ""
	return true
}

// bridgeFolder applies bridgeEntry to every entry of a folder file; true: something changed (rewrite it).
func bridgeFolder(f *folderFile) bool {
	changed := false
	for _, s := range f.Sessions {
		for i := range s.Chats {
			if bridgeEntry(&s.Chats[i]) {
				changed = true
			}
		}
	}
	return changed
}
