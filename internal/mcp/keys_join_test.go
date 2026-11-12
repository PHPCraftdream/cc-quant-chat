package mcp

import (
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/local"
)

// What a session tells the broker of its id: the current one, and after /clear (or a compaction) the one
// the broker has the nick bound to, kept with the chat so that a restart of Claude Code does not lose it.
func TestASessionTellsTheBrokerItsIdAndTheChainAfterClear(t *testing.T) {
	t.Setenv("CCQC_HOME", t.TempDir())
	folder := t.TempDir()
	t.Setenv("CLAUDE_PROJECT_DIR", folder)
	chat := "b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1"
	if err := local.RememberChat(folder, "s1", local.Entry{ChatID: chat, Chat: "team", Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	if err := local.SetBinding(folder, "s1", "team"); err != nil {
		t.Fatal(err)
	}
	s := &Session{sid: "s1"}
	if ids := s.idsFor("team"); ids.SID != "s1" || ids.PrevSID != "" {
		t.Fatalf("before /clear: %+v", ids)
	}
	// /clear: the session has a new id; the chat moved with it; the broker still has s1.
	if err := local.MoveSession(folder, "s1", "s2", "clear"); err != nil {
		t.Fatal(err)
	}
	s.sid = "s2"
	if ids := s.idsFor("team"); ids.SID != "s2" || ids.PrevSID != "s1" || ids.Source != "clear" {
		t.Fatalf("after /clear: %+v", ids)
	}
	// A restart of Claude Code (a new process, the same session by --resume): the chain is read from the disk.
	again := &Session{sid: "s2"}
	if ids := again.idsFor("team"); ids.PrevSID != "s1" || ids.Source != "clear" {
		t.Fatalf("after a restart: %+v", ids)
	}
	// A terminal, or a session whose id is not known yet, says nothing of a chain.
	if ids := (&Session{}).idsFor("team"); ids.SID != "" || ids.PrevSID != "" {
		t.Fatalf("no session: %+v", ids)
	}
	// Another chat of the session, not protected, has none.
	if ids := again.idsFor("other"); ids.PrevSID != "" {
		t.Fatalf("a chat that is not protected: %+v", ids)
	}
}
