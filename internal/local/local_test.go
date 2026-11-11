package local

import (
	"fmt"
	"os"
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
