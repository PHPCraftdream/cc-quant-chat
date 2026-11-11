package local

import (
	"fmt"
	"os"
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
	must(RememberChat(folder, "s1", Entry{Token: "dev.x", Name: "alice"}))
	must(RememberChat(folder, "s2", Entry{Token: "dev.x", Name: "bob"}))
	must(RememberChat(folder, "s2", Entry{Token: "ops.y", Name: "bob"}))
	must(RememberChat(folder, "s2", Entry{Token: "dev.x", Name: "bobby"})) // same chat: replaced

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
	must(MoveSession(folder, "s1", "s1b"))
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
		if err := RememberChat(folder, fmt.Sprintf("s%03d", i), Entry{Token: "dev.x", Name: "n"}); err != nil {
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

func TestNickKeys(t *testing.T) {
	t.Setenv("CCQC_HOME", t.TempDir())
	if NickKey("c1", "alice") != "" {
		t.Fatal("unknown nick must have no key")
	}
	if err := SetNickKey("c1", "alice", "k1"); err != nil {
		t.Fatal(err)
	}
	if NickKey("c1", "alice") != "k1" || NickKey("c2", "alice") != "" {
		t.Fatal("keys must be per chat and nick")
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
			errs <- RememberChat(folder, fmt.Sprintf("s%d", i), Entry{Token: "dev.x", Name: fmt.Sprintf("n%d", i)})
		}()
		go func() { // and nick keys of one user being stored
			defer wg.Done()
			errs <- SetNickKey("chat", fmt.Sprintf("n%d", i), fmt.Sprintf("key%d", i))
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
		if got := NickKey("chat", fmt.Sprintf("n%d", i)); got != fmt.Sprintf("key%d", i) {
			t.Fatalf("nick key n%d = %q", i, got)
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

func TestFindChatTokenTakesTheNewestSessionsEntry(t *testing.T) {
	t.Setenv("CCQC_HOME", t.TempDir())
	folderA, folderB := t.TempDir(), t.TempDir()
	if err := RememberChat(folderA, "s1", Entry{Token: "team.old", Name: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := RememberChat(folderB, "s2", Entry{Token: "team.new", Name: "b"}); err != nil {
		t.Fatal(err)
	}
	// Make the first one the older by touching nothing; s2 was remembered later (same-second
	// timestamps tie, which keeps the first found: both are valid tokens of the chat).
	got, ok := FindChatToken("team")
	if !ok || (got != "team.new" && got != "team.old") {
		t.Fatalf("token = %q, found %v", got, ok)
	}
	if _, ok := FindChatToken("nope"); ok {
		t.Fatal("found a token for a chat nobody joined")
	}
}
