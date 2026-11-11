package broker

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store/sqlite"
)

// boardBroker is a broker on a real SQLite database, which the board needs.
func boardBroker(t *testing.T) (*Broker, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ccqc.db")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return newTestBroker(db), path
}

func (c *client) board(a proto.BoardArgs) (string, string) {
	c.t.Helper()
	var r proto.BoardReply
	code := c.call("board", a, &r)
	return r.Text, code
}

func sp(s string) *string { return &s }

// boardEvents drains the board events that arrived within a short while.
func boardEvents(c *client) []proto.Event {
	var out []proto.Event
	for {
		select {
		case ev := <-c.events:
			if ev.Kind == "board" {
				out = append(out, ev)
			}
		case <-time.After(150 * time.Millisecond):
			return out
		}
	}
}

func TestBoardNeedsTheDatabase(t *testing.T) {
	b := newTestBroker(newMemStore()) // not a store.Board
	tok, _ := token(t)
	a := b.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)
	if _, code := a.board(proto.BoardArgs{Op: "task.list"}); code != "board_unavailable" {
		t.Fatalf("code = %q, want board_unavailable", code)
	}
}

func TestEveryMemberSeesAndChangesTheBoardOfItsChatOnly(t *testing.T) {
	b, _ := boardBroker(t)
	tok1, _ := token(t)
	tok2, _ := token(t)
	a, c := b.connect(t), b.connect(t)
	a.join(tok1, "alice", proto.ProtoVersion)
	c.join(tok1, "carol", proto.ProtoVersion)
	other := b.connect(t)
	other.join(tok2, "mallory", proto.ProtoVersion)

	text, code := a.board(proto.BoardArgs{Op: "task.create", Title: sp("Fix CI"), Description: sp("long text")})
	if code != "" || !strings.HasPrefix(text, `T-1 created todo p0 "Fix CI"`) {
		t.Fatalf("create: %q %q", text, code)
	}
	// Carol, a plain member, reads and moves it: "everyone changes for now".
	list, code := c.board(proto.BoardArgs{Op: "task.list"})
	if code != "" || !strings.Contains(list, "T-1 todo - p0 Fix CI") || strings.Contains(list, "long text") {
		t.Fatalf("carol's list: %q %q", list, code)
	}
	if text, code := c.board(proto.BoardArgs{Op: "task.move", ID: 1, Status: "doing"}); code != "" || !strings.Contains(text, "T-1 todo → doing (log #2)") {
		t.Fatalf("move: %q %q", text, code)
	}
	if text, _ := other.board(proto.BoardArgs{Op: "task.list"}); strings.Contains(text, "Fix CI") {
		t.Fatalf("a member of another chat sees the board: %q", text)
	}
	if _, code := other.board(proto.BoardArgs{Op: "task.get", ID: 1}); code != "no_such_task" {
		t.Fatalf("task of another chat: %q", code)
	}
	get, _ := a.board(proto.BoardArgs{Op: "task.get", ID: 1})
	if !strings.Contains(get, "long text") || !strings.Contains(get, "task.move") {
		t.Fatalf("task get: %q", get)
	}
}

func TestOnlyTheAssigneeHearsOfAChangeAndNotTheAuthor(t *testing.T) {
	b, _ := boardBroker(t)
	tok, _ := token(t)
	a, bob, carol := b.connect(t), b.connect(t), b.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)
	bob.join(tok, "bob", proto.ProtoVersion)
	carol.join(tok, "carol", proto.ProtoVersion)
	for _, c := range []*client{a, bob, carol} {
		c.msgs() // drain presence
	}

	a.board(proto.BoardArgs{Op: "task.create", Title: sp("Ship it"), Assignee: sp("@bob")})
	got := boardEvents(bob)
	if len(got) != 1 || !got[0].Wake || !strings.Contains(got[0].Text, `@alice assigned you T-1 "Ship it"`) {
		t.Fatalf("bob's events after an assignment: %+v", got)
	}
	if ev := boardEvents(a); len(ev) != 0 {
		t.Fatalf("the author heard of its own change: %+v", ev)
	}
	if ev := boardEvents(carol); len(ev) != 0 {
		t.Fatalf("a bystander heard of it: %+v", ev)
	}

	// The assignee's own change is not echoed to it; a status change by somebody else wakes it.
	bob.board(proto.BoardArgs{Op: "task.move", ID: 1, Status: "doing"})
	if ev := boardEvents(bob); len(ev) != 0 {
		t.Fatalf("bob heard of its own move: %+v", ev)
	}
	carol.board(proto.BoardArgs{Op: "task.move", ID: 1, Status: "review"})
	if ev := boardEvents(bob); len(ev) != 1 || !ev[0].Wake || !strings.Contains(ev[0].Text, "doing → review") {
		t.Fatalf("a move by another: %+v", ev)
	}
	// A new title arrives quietly.
	carol.board(proto.BoardArgs{Op: "task.update", ID: 1, Title: sp("Ship it now")})
	if ev := boardEvents(bob); len(ev) != 1 || ev[0].Wake {
		t.Fatalf("a title change must not wake: %+v", ev)
	}
	// Nobody else heard any of it.
	if ev := boardEvents(a); len(ev) != 0 {
		t.Fatalf("the author hears about others' changes to a task it does not hold: %+v", ev)
	}
}

func TestBoardChangesAreRateLimitedAndCheckTheAssignee(t *testing.T) {
	b, _ := boardBroker(t)
	tok, _ := token(t)
	a := b.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)
	if _, code := a.board(proto.BoardArgs{Op: "task.create", Title: sp("x"), Assignee: sp("ghost")}); code != "no_such_member" {
		t.Fatalf("unknown assignee: %q", code)
	}
	for i := 0; i < boardLimit-1; i++ { // the refused one above counted too
		if _, code := a.board(proto.BoardArgs{Op: "task.create", Title: sp("t")}); code != "" {
			t.Fatalf("create %d: %q", i, code)
		}
	}
	if _, code := a.board(proto.BoardArgs{Op: "task.create", Title: sp("one too many")}); code != "rate_limited" {
		t.Fatalf("over the limit: %q", code)
	}
	if _, code := a.board(proto.BoardArgs{Op: "task.list"}); code != "" {
		t.Fatalf("reads are not limited: %q", code)
	}
}

func TestBoardSurvivesABrokerRestartAndRefusesChangesWhileStopping(t *testing.T) {
	b1, path := boardBroker(t)
	tok, _ := token(t)
	a := b1.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)
	a.board(proto.BoardArgs{Op: "sprint.create", Name: sp("one")})
	a.board(proto.BoardArgs{Op: "sprint.start", ID: 1})
	a.board(proto.BoardArgs{Op: "task.create", Title: sp("kept"), Sprint: func() *int64 { v := int64(1); return &v }()})

	b1.stopping.Store(true)
	if _, code := a.board(proto.BoardArgs{Op: "task.create", Title: sp("late")}); code != "restarting" {
		t.Fatalf("a change while stopping: %q", code)
	}
	if text, code := a.board(proto.BoardArgs{Op: "task.list"}); code != "" || !strings.Contains(text, "kept") {
		t.Fatalf("reads keep working: %q %q", text, code)
	}

	db2, err := sqlite.Open(path) // a new broker process on the same database file
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	b2 := newTestBroker(db2)
	a2 := b2.connect(t)
	a2.key = a.key
	a2.join(tok, "alice", proto.ProtoVersion)
	if text, _ := a2.board(proto.BoardArgs{Op: "task.list"}); !strings.Contains(text, `Sprint S-1 "one" (active)`) || !strings.Contains(text, "kept") {
		t.Fatalf("after a restart: %q", text)
	}
	if text, _ := a2.board(proto.BoardArgs{Op: "board.log"}); !strings.Contains(text, "task.create") || !strings.Contains(text, "sprint.start") {
		t.Fatalf("the journal after a restart: %q", text)
	}
}

func TestTheJournalRecordsTheOwnerAndRevertsWorkThroughTheOp(t *testing.T) {
	b, _ := boardBroker(t)
	tok, _ := token(t)
	k := newKey(t)
	register(t, b, k)
	o, code := ownerJoin(t, b, k, tok, "owner")
	if code != "" {
		t.Fatal(code)
	}
	a := b.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)

	o.board(proto.BoardArgs{Op: "task.create", Title: sp("by the owner")})
	a.board(proto.BoardArgs{Op: "task.move", ID: 1, Status: "doing"})
	log, _ := a.board(proto.BoardArgs{Op: "board.log"})
	if !strings.Contains(log, "OWNER owner task.create") || strings.Contains(log, "OWNER alice") {
		t.Fatalf("the journal must mark the owner's entries and only them:\n%s", log)
	}
	text, code := a.board(proto.BoardArgs{Op: "board.revert", LogID: 2})
	if code != "" || !strings.Contains(text, "reverted #2") {
		t.Fatalf("revert: %q %q", text, code)
	}
	if get, _ := a.board(proto.BoardArgs{Op: "task.get", ID: 1}); !strings.Contains(get, "todo") {
		t.Fatalf("after the revert: %q", get)
	}
	if log, _ := a.board(proto.BoardArgs{Op: "board.log", Limit: 1}); !strings.Contains(log, "revert") || !strings.Contains(log, "(of #2)") {
		t.Fatalf("the revert is itself in the journal: %q", log)
	}
}
