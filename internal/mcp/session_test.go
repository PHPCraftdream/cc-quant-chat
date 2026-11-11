package mcp

import (
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/local"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

func TestFreshDropsRepeatedMessages(t *testing.T) {
	cc := &chatConn{}
	for _, c := range []struct {
		id   int64
		want bool
	}{{5, true}, {5, false}, {4, false}, {6, true}} {
		if got := cc.fresh(c.id); got != c.want {
			t.Fatalf("fresh(%d) = %v, want %v", c.id, got, c.want)
		}
	}
}

func TestPermanentJoinErrors(t *testing.T) {
	for code, want := range map[string]bool{
		"name_taken": true, "bad_token": true, "bad_name": true,
		"name_online": false, "store_unavailable": false, "store_failed": false,
	} {
		if got := permanentJoinError(&proto.Error{Code: code}); got != want {
			t.Errorf("%s: permanent = %v, want %v", code, got, want)
		}
	}
	if permanentJoinError(proto.Errf("x", "y")) || permanentJoinError(nil) {
		t.Error("unknown codes and nil are transient")
	}
}

func TestSessionIDChanges(t *testing.T) {
	t.Setenv("CCQC_HOME", t.TempDir())
	folder := t.TempDir()
	t.Setenv("CLAUDE_PROJECT_DIR", folder)
	if err := local.RememberChat(folder, "old-sid", local.Entry{Token: "dev.x", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	s := newSession(func(string, map[string]string) {})
	s.sid = "old-sid"

	s.setSessionID("bad sid;", "clear", true)
	s.setSessionID("other", "", false) // a wait hook never decides
	if s.sid != "old-sid" {
		t.Fatalf("sid = %q; invalid ids and non-SessionStart hooks must be ignored", s.sid)
	}

	s.setSessionID("new-sid", "clear", true) // /clear: the same conversation, chats move
	if s.sid != "new-sid" {
		t.Fatalf("sid = %q after clear", s.sid)
	}
	if got, _ := local.SessionChats(folder, "new-sid"); len(got) != 1 {
		t.Fatalf("chats of the new id = %v; /clear must carry them over", got)
	}
	if got, _ := local.SessionChats(folder, "old-sid"); len(got) != 0 {
		t.Fatalf("chats of the old id = %v", got)
	}
}

func TestResumeDoesNotStealChats(t *testing.T) {
	t.Setenv("CCQC_HOME", t.TempDir())
	folder := t.TempDir()
	t.Setenv("CLAUDE_PROJECT_DIR", folder)
	if err := local.RememberChat(folder, "first", local.Entry{Token: "dev.x", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	s := newSession(func(string, map[string]string) {})
	s.sid = "first"
	s.setSessionID("second", "resume", true) // another session: no chats of its own
	if got, _ := local.SessionChats(folder, "first"); len(got) != 1 {
		t.Fatalf("the first session lost its chats on a resume: %v", got)
	}
	if got, _ := local.SessionChats(folder, "second"); len(got) != 0 {
		t.Fatalf("the second session got chats it never joined: %v", got)
	}
	if s.auto == nil {
		t.Fatal("the new session's chats are not being joined")
	}
	<-s.auto.done
}

func TestHookQueueIsBounded(t *testing.T) {
	s := newSession(func(string, map[string]string) {})
	s.hook.used = true
	items := make([]pendItem, pendingCap+10)
	for i := range items {
		items[i] = pendItem{line: "x"}
	}
	s.route(items)
	if n := len(s.hook.pending); n != pendingCap {
		t.Fatalf("pending = %d, want %d", n, pendingCap)
	}
}

func TestCompactMovesAndUnknownSourceChangesNothing(t *testing.T) {
	t.Setenv("CCQC_HOME", t.TempDir())
	folder := t.TempDir()
	t.Setenv("CLAUDE_PROJECT_DIR", folder)
	if err := local.RememberChat(folder, "a", local.Entry{Token: "dev.x", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	s := newSession(func(string, map[string]string) {})
	s.sid = "a"
	s.setSessionID("b", "-", true) // source unknown
	if s.sid != "a" {
		t.Fatalf("sid = %q; an unknown source must change nothing", s.sid)
	}
	s.setSessionID("b", "compact", true)
	if got, _ := local.SessionChats(folder, "b"); len(got) != 1 || s.sid != "b" {
		t.Fatalf("compact must carry the chats over: %v, sid %q", got, s.sid)
	}
}
