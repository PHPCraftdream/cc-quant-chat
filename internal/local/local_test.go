package local

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func names(t *testing.T, folder, sid string) []string {
	t.Helper()
	es, err := SessionChats(folder, sid)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Local()+"@"+e.Name)
	}
	return out
}

func TestSessionsInOneFolderAreSeparate(t *testing.T) {
	t.Setenv("CCQC_HOME", t.TempDir())
	folder := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(RememberChat(folder, "s1", Entry{Chat: "dev", Name: "alice"}))
	must(RememberChat(folder, "s2", Entry{Chat: "dev", Name: "bob"}))
	must(RememberChat(folder, "s2", Entry{Chat: "ops", Name: "bob"}))
	must(RememberChat(folder, "s2", Entry{Chat: "dev", Name: "bobby"})) // same chat: replaced

	if got := names(t, folder, "s1"); len(got) != 1 || got[0] != "dev@alice" {
		t.Fatalf("s1 = %v", got)
	}
	if got := names(t, folder, "s2"); len(got) != 2 || got[0] != "ops@bob" || got[1] != "dev@bobby" {
		t.Fatalf("s2 = %v", got)
	}
	if got := names(t, folder, "s3"); len(got) != 0 {
		t.Fatalf("unknown session = %v, want none", got)
	}

	// /clear gives the session a new id: its chats move along.
	must(MoveSession(folder, "s1", "s1b", "clear"))
	if got := names(t, folder, "s1"); len(got) != 0 {
		t.Fatalf("old id still has chats: %v", got)
	}
	if got := names(t, folder, "s1b"); len(got) != 1 || got[0] != "dev@alice" {
		t.Fatalf("new id = %v", got)
	}

	must(ForgetChat(folder, "s1b", "dev"))
	must(ForgetChat(folder, "s2", "dev"))
	must(ForgetChat(folder, "s2", "ops"))
	if _, err := os.Stat(folderPath(folder)); !os.IsNotExist(err) {
		t.Fatalf("folder file must be removed when no session has chats, stat err = %v", err)
	}
}

func TestOldSessionsArePruned(t *testing.T) {
	t.Setenv("CCQC_HOME", t.TempDir())
	folder := t.TempDir()
	for i := 0; i < keepSessions+5; i++ {
		if err := RememberChat(folder, fmt.Sprintf("s%03d", i), Entry{Chat: "dev", Name: "n"}); err != nil {
			t.Fatal(err)
		}
	}
	var f folderFile
	if err := readJSON(folderPath(folder), &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Sessions) != keepSessions {
		t.Fatalf("sessions kept = %d, want %d", len(f.Sessions), keepSessions)
	}
}

func TestConcurrentWritersLoseNothing(t *testing.T) {
	t.Setenv("CCQC_HOME", t.TempDir())
	folder := t.TempDir()
	const n = 24
	var wg sync.WaitGroup
	errs := make(chan error, 2*n)
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() { // sessions of one folder remembering chats at the same time
			defer wg.Done()
			errs <- RememberChat(folder, fmt.Sprintf("s%d", i), Entry{Chat: "dev", Name: fmt.Sprintf("n%d", i)})
		}()
		go func() { // and another session of the folder moving on
			defer wg.Done()
			errs <- TouchSession(folder, fmt.Sprintf("s%d", i))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < n; i++ {
		if got := names(t, folder, fmt.Sprintf("s%d", i)); len(got) != 1 {
			t.Fatalf("session s%d lost its chat: %v", i, got)
		}
	}
}

func TestTouchSessionOnlyTouchesExisting(t *testing.T) {
	t.Setenv("CCQC_HOME", t.TempDir())
	folder := t.TempDir()
	if err := TouchSession(folder, "nope"); err != nil {
		t.Fatal(err)
	}
	if got := names(t, folder, "nope"); len(got) != 0 {
		t.Fatalf("touch created a session: %v", got)
	}
}

// The bridge for the deployment of KEY-7: an entry that a session remembered by a token becomes the entry of its chat
// (id = the SHA-256 of the token, name = its prefix) the first time the folder is read, and the token is erased from the file.
func TestAnEntryOfATokenBecomesTheEntryOfItsChatAndTheTokenIsErasedFromTheFile(t *testing.T) {
	t.Setenv("CCQC_HOME", t.TempDir())
	folder := t.TempDir()
	token := "team.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	p := folderPath(folder)
	old := `{"folder":"x","sessions":{"sid":{"used":"2026-11-12T10:00:00Z","chats":[{"token":"` + token + `","name":"alice"}]}}}`
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := SessionChats(folder, "sid")
	h := sha256.Sum256([]byte(token))
	if err != nil || len(got) != 1 || got[0].ChatID != hex.EncodeToString(h[:]) || got[0].Chat != "team" || got[0].Token != "" || got[0].Local() != "team" {
		t.Fatalf("%+v %v", got, err)
	}
	raw, _ := os.ReadFile(p)
	if bytes.Contains(raw, []byte("AAAAAAAAAA")) || bytes.Contains(raw, []byte("token")) {
		t.Fatalf("the token is still in the file: %s", raw)
	}
}

func TestAChatForInvitationsIsRememberedWithoutAnythingThatLetsAStrangerIn(t *testing.T) {
	t.Setenv("CCQC_HOME", t.TempDir())
	folder := t.TempDir()
	e := Entry{ChatID: strings.Repeat("ab", 32), Chat: "team", Name: "bob"}
	if e.Local() != "team" {
		t.Fatalf("local name %q", e.Local())
	}
	if err := RememberChat(folder, "sid", e); err != nil {
		t.Fatal(err)
	}
	got, err := SessionChats(folder, "sid")
	if err != nil || len(got) != 1 || got[0].ChatID != e.ChatID || got[0].Token != "" {
		t.Fatalf("%+v %v", got, err)
	}
	raw, err := os.ReadFile(folderPath(folder))
	if err == nil && bytes.Contains(raw, []byte("token")) {
		t.Errorf("a chat without a token is remembered with a token field: %s", raw)
	}
}
