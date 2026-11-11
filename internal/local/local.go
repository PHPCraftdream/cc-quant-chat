// Package local: per-user client state outside any repository —
// chats a folder joins automatically, and the keys of registered nicks.
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
	"strings"
)

// Entry: a chat a folder joins on session start.
type Entry struct {
	Token string `json:"token"`
	Name  string `json:"name"`
	Alias string `json:"alias,omitempty"`
}

type folderFile struct {
	Folder string  `json:"folder"`
	Chats  []Entry `json:"chats"`
}

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

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Local name of an entry: alias or the chat name from the token.
func (e Entry) Local() string {
	if e.Alias != "" {
		return e.Alias
	}
	if i := strings.LastIndexByte(e.Token, '.'); i > 0 {
		return e.Token[:i]
	}
	return ""
}

func FolderChats(folder string) ([]Entry, error) {
	var f folderFile
	err := readJSON(folderPath(folder), &f)
	return f.Chats, err
}

// RememberChat adds or replaces (by local chat name) a chat the folder joins automatically.
func RememberChat(folder string, e Entry) error {
	p := folderPath(folder)
	var f folderFile
	if err := readJSON(p, &f); err != nil {
		return err
	}
	f.Folder = folder
	out := f.Chats[:0]
	for _, c := range f.Chats {
		if c.Local() != e.Local() {
			out = append(out, c)
		}
	}
	f.Chats = append(out, e)
	return writeJSON(p, f)
}

func ForgetChat(folder, localName string) error {
	p := folderPath(folder)
	var f folderFile
	if err := readJSON(p, &f); err != nil {
		return err
	}
	out := f.Chats[:0]
	for _, c := range f.Chats {
		if c.Local() != localName {
			out = append(out, c)
		}
	}
	f.Chats = out
	if len(out) == 0 {
		err := os.Remove(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	return writeJSON(p, f)
}

func keysPath() string { return filepath.Join(home(), "nick-keys.json") }

// NickKey: key of a nick registered from this machine; "" if unknown.
func NickKey(chatID, nick string) string {
	keys := map[string]string{}
	_ = readJSON(keysPath(), &keys)
	return keys[chatID+"/"+nick]
}

func SetNickKey(chatID, nick, key string) error {
	keys := map[string]string{}
	if err := readJSON(keysPath(), &keys); err != nil {
		return err
	}
	keys[chatID+"/"+nick] = key
	return writeJSON(keysPath(), keys)
}
