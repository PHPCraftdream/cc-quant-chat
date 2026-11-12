// Package local: per-user client state outside any repository —
// the chats each Claude Code session joins automatically, and the keys of registered nicks.
// Files live in CCQC_HOME or <user config dir>/ccqc, readable by the user only.
package local

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Entry: a chat a session joins automatically: by its id (ChatID) and the protected key of the nick; nothing
// that lets a stranger in is kept here.
type Entry struct {
	Token  string `json:"token,omitempty"` // only to be read once and erased (bridge.go)
	ChatID string `json:"chat_id,omitempty"`
	Chat   string `json:"chat,omitempty"` // the chat's name
	Name   string `json:"name"`
	Alias  string `json:"alias,omitempty"`
	// Bound is the session id the broker has a protected nick bound to, when it is not the session that
	// holds this entry now (the session went on after /clear or a compaction and the broker has not
	// been told yet); BoundVia says why it changed. Together they are the chain the broker is shown
	// at the next login, and survive a restart of Claude Code (docs/design-keys.md section 8).
	Bound     string `json:"bound,omitempty"`
	BoundVia  string `json:"bound_via,omitempty"`
	Protected bool   `json:"protected,omitempty"` // the nick has a protected key, bound to this session
}

// Local name of an entry: alias, or the chat's name.
func (e Entry) Local() string {
	if e.Alias != "" {
		return e.Alias
	}
	if e.Chat != "" {
		return e.Chat
	}
	return ""
}

// Sessions are keyed by Claude Code session id: several sessions in one folder each keep
// their own chats and nicks; resuming a session finds them again.
type folderFile struct {
	Folder   string              `json:"folder"`
	Sessions map[string]*session `json:"sessions,omitempty"`
}

type session struct {
	Used  string  `json:"used"` // RFC 3339; the least recently used are pruned
	Chats []Entry `json:"chats"`
}

const keepSessions = 50

func home() string {
	if d := os.Getenv("CCQC_HOME"); d != "" {
		return d
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "ccqc")
}

// ProjectDir: the folder Claude Code runs in.
func ProjectDir() string {
	if d := os.Getenv("CLAUDE_PROJECT_DIR"); d != "" {
		return d
	}
	d, _ := os.Getwd()
	return d
}

func folderPath(folder string) string {
	abs, err := filepath.Abs(folder)
	if err != nil {
		abs = folder
	}
	abs = filepath.Clean(abs)
	if runtime.GOOS == "windows" {
		abs = strings.ToLower(abs)
	}
	h := sha256.Sum256([]byte(abs))
	return filepath.Join(home(), "folders", hex.EncodeToString(h[:8])+".json")
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// withLock runs fn under an exclusive lock on <home>/.lock: sessions of one user write these
// files concurrently, and a read-modify-write must not lose another session's update.
func withLock(fn func() error) error {
	if err := os.MkdirAll(home(), 0o700); err != nil {
		return err
	}
	lf, err := os.OpenFile(filepath.Join(home(), ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := lockFile(lf); err != nil {
		return err
	}
	defer unlockFile(lf)
	return fn()
}

// writeJSON replaces the file atomically through a private temp file in the same folder.
func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp") // 0600
	if err != nil {
		return err
	}
	_, err = tmp.Write(append(b, '\n'))
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

// update loads the folder file, applies fn and saves it (or removes it when empty).
func update(folder string, fn func(f *folderFile)) error {
	return withLock(func() error { return updateLocked(folder, fn) })
}

func updateLocked(folder string, fn func(f *folderFile)) error {
	p := folderPath(folder)
	f := folderFile{}
	if err := readJSON(p, &f); err != nil {
		return err
	}
	bridgeFolder(&f) // the rewrite below erases the tokens
	if f.Sessions == nil {
		f.Sessions = map[string]*session{}
	}
	f.Folder = folder
	fn(&f)
	for id, s := range f.Sessions {
		if len(s.Chats) == 0 {
			delete(f.Sessions, id)
		}
	}
	if len(f.Sessions) > keepSessions {
		ids := make([]string, 0, len(f.Sessions))
		for id := range f.Sessions {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return f.Sessions[ids[i]].Used > f.Sessions[ids[j]].Used })
		for _, id := range ids[keepSessions:] {
			delete(f.Sessions, id)
		}
	}
	if len(f.Sessions) == 0 {
		err := os.Remove(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	return writeJSON(p, f)
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// SessionChats: chats the session joins automatically.
func SessionChats(folder, sessionID string) ([]Entry, error) {
	var f folderFile
	if err := withLock(func() error {
		p := folderPath(folder)
		if err := readJSON(p, &f); err != nil {
			return err
		}
		if bridgeFolder(&f) { // tokens of the days before KEY-7: made entries of chats, and erased from the file
			return writeJSON(p, f)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if s := f.Sessions[sessionID]; s != nil {
		return s.Chats, nil
	}
	return nil, nil
}

// RememberChat adds or replaces (by local chat name) a chat of the session.
func RememberChat(folder, sessionID string, e Entry) error {
	return update(folder, func(f *folderFile) {
		s := f.Sessions[sessionID]
		if s == nil {
			s = &session{}
			f.Sessions[sessionID] = s
		}
		out := s.Chats[:0]
		for _, c := range s.Chats {
			if c.Local() != e.Local() {
				out = append(out, c)
			}
		}
		s.Chats, s.Used = append(out, e), now()
	})
}

// TouchSession marks the session as used now, so pruning keeps the sessions people come back to.
func TouchSession(folder, sessionID string) error {
	return update(folder, func(f *folderFile) {
		if s := f.Sessions[sessionID]; s != nil {
			s.Used = now()
		}
	})
}

func ForgetChat(folder, sessionID, localName string) error {
	return update(folder, func(f *folderFile) {
		s := f.Sessions[sessionID]
		if s == nil {
			return
		}
		out := s.Chats[:0]
		for _, c := range s.Chats {
			if c.Local() != localName {
				out = append(out, c)
			}
		}
		s.Chats = out
	})
}

// MoveSession hands a session's chats to its new id (Claude Code starts a new id on /clear and after a
// compaction; source says which). A chat whose nick is bound to a session at the broker keeps the id
// it is bound to and learns why the id changed.
func MoveSession(folder, from, to, source string) error {
	return update(folder, func(f *folderFile) {
		if s := f.Sessions[from]; s != nil && from != to {
			for i := range s.Chats {
				if e := &s.Chats[i]; e.Bound == "" && e.Protected {
					e.Bound = from
				}
				if s.Chats[i].Bound != "" {
					s.Chats[i].BoundVia = source
				}
			}
			s.Used = now()
			f.Sessions[to] = s
			delete(f.Sessions, from)
		}
	})
}

// SetBinding records, for the chat of a session, that its nick has a protected key and that the broker
// has it bound to this session now (a join, or a login the broker accepted): no older id to tell.
func SetBinding(folder, sessionID, localName string) error {
	return update(folder, func(f *folderFile) {
		if s := f.Sessions[sessionID]; s != nil {
			for i := range s.Chats {
				if s.Chats[i].Local() == localName {
					s.Chats[i].Protected, s.Chats[i].Bound, s.Chats[i].BoundVia = true, "", ""
				}
			}
		}
	})
}
