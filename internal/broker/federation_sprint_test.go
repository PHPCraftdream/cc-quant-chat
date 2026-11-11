package broker

import (
	"strings"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store/sqlite"
)

// After a merge a chat may hold two active sprints (one started at each node). A node still refuses
// to start another while one is active, and the board still shows a current sprint.
func TestTwoActiveSprintsAfterAMergeDoNotBreakTheBoardAndAStartStillRefuses(t *testing.T) {
	b, path := boardBroker(t)
	tok, id := token(t)
	alice := b.connect(t)
	if _, code := alice.join(tok, "alice", proto.ProtoVersion); code != "" {
		t.Fatalf("join: %q", code)
	}
	for _, n := range []string{"one", "two", "three"} {
		mustBoard(t, alice, proto.BoardArgs{Op: "sprint.create", Name: sp(n)})
	}
	mustBoard(t, alice, proto.BoardArgs{Op: "sprint.start", ID: 1})
	if _, code := alice.board(proto.BoardArgs{Op: "sprint.start", ID: 2}); code != "sprint_active" {
		t.Fatalf("starting a second sprint: %q", code)
	}

	// The second one was started at the other node: both are active here now.
	raw, err := sqlite.OpenRaw(chatFile(path, id))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`UPDATE board_sprints SET state = 'active', started_at = '2999-01-01T00:00:00Z' WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	if _, code := alice.board(proto.BoardArgs{Op: "sprint.start", ID: 3}); code != "sprint_active" {
		t.Fatalf("a third start with two active: %q", code)
	}
	text, code := alice.board(proto.BoardArgs{Op: "sprint.list"})
	if code != "" || !strings.Contains(text, "two") {
		t.Fatalf("the sprint list: %q %q", text, code)
	}
	text, code = alice.board(proto.BoardArgs{Op: "task.list", SprintFilter: "active"})
	if code != "" {
		t.Fatalf("the tasks of the current sprint: %q %q", text, code)
	}
	if !strings.Contains(text, "two") { // the one started last is the current one
		t.Fatalf("the current sprint is the one started last: %q", text)
	}
}
