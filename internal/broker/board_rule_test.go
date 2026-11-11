package broker

import (
	"strings"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

func TestTheBoardRuleInTheFourCases(t *testing.T) {
	b, _ := boardBroker(t)
	tok, id := token(t)
	k := newKey(t)
	register(t, b, k)
	adm := b.connect(t)

	alice, bob, carol, dave := b.connect(t), b.connect(t), b.connect(t), b.connect(t)
	alice.join(tok, "alice", proto.ProtoVersion)
	bob.join(tok, "bob", proto.ProtoVersion)
	carol.join(tok, "carol", proto.ProtoVersion)
	dave.join(tok, "dave", proto.ProtoVersion)
	owner, code := ownerJoin(t, b, k, tok, "owner")
	if code != "" {
		t.Fatal(code)
	}
	for _, c := range []string{"one", "two", "three"} {
		if _, code := dave.board(proto.BoardArgs{Op: "task.create", Title: sp(c), Assignee: sp("bob")}); code != "" {
			t.Fatalf("create: %q", code)
		}
	}
	move := func(c *client, id int64, status string) string {
		_, code := c.board(proto.BoardArgs{Op: "task.move", ID: id, Status: status})
		return code
	}

	// 1. No manager role: everyone moves everything.
	if code := move(dave, 1, "doing"); code != "" {
		t.Fatalf("without a manager role dave moves T-1: %q", code)
	}

	// The owner pins a role to carol and makes it the board's manager role.
	if code := adminReq(t, adm, k, "role_give", proto.AdminRole{Chat: id, Nick: "carol", Role: "lead"}); code != "" {
		t.Fatalf("pin: %q", code)
	}
	if code := adminReq(t, adm, k, "config_set", proto.AdminConfig{Chat: id, Key: "board.manager_role", Value: "lead"}); code != "" {
		t.Fatalf("manager role: %q", code)
	}

	// 2. With the role set, only the owner and the pinned member move tasks (here: to a final state).
	if code := move(dave, 1, "review"); code != "forbidden" {
		t.Fatalf("an outsider moving a task: %q, want forbidden", code)
	}
	if _, code := dave.board(proto.BoardArgs{Op: "task.move", ID: 1, Status: "review"}); code != "forbidden" {
		t.Fatal(code)
	}
	text, _ := dave.board(proto.BoardArgs{Op: "task.get", ID: 1})
	if !strings.Contains(text, "doing") {
		t.Fatalf("a refused move changed the task:\n%s", text)
	}
	if code := move(carol, 1, "review"); code != "" {
		t.Fatalf("the pinned member: %q", code)
	}
	if code := move(carol, 1, "done"); code != "" {
		t.Fatalf("the pinned member finishes a task: %q", code)
	}
	if code := move(owner, 2, "doing"); code != "" {
		t.Fatalf("the owner: %q", code)
	}
	if _, code := dave.board(proto.BoardArgs{Op: "task.get", ID: 1}); code != "" {
		t.Fatal(code)
	}

	// 3. The assignee moves its own task between unfinished states.
	if code := move(bob, 2, "review"); code != "" {
		t.Fatalf("assignee, unfinished → unfinished: %q", code)
	}
	if code := move(bob, 2, "doing"); code != "" {
		t.Fatalf("assignee back: %q", code)
	}
	// 4. ... but finishing it is for the owner and the role.
	if code := move(bob, 2, "review"); code != "" {
		t.Fatal(code)
	}
	if code := move(bob, 2, "done"); code != "forbidden" {
		t.Fatalf("assignee finishing its task: %q, want forbidden", code)
	}
	// Somebody else's task is not the assignee's to move at all.
	if code := move(bob, 3, "doing"); code != "" { // T-3 is bob's too: allowed
		t.Fatalf("bob's own T-3: %q", code)
	}
	if _, code := dave.board(proto.BoardArgs{Op: "task.update", ID: 3, Assignee: sp("dave")}); code != "" {
		t.Fatalf("anyone may edit: %q", code)
	}
	if code := move(bob, 3, "review"); code != "forbidden" {
		t.Fatalf("after reassigning T-3 to dave, bob is no longer its assignee: %q", code)
	}

	// Sprints, the process and reverts need the owner or the role; creating, editing, commenting do not.
	if _, code := dave.board(proto.BoardArgs{Op: "sprint.create", Name: sp("s")}); code != "" {
		t.Fatalf("create a sprint: %q", code)
	}
	if _, code := dave.board(proto.BoardArgs{Op: "sprint.start", ID: 1}); code != "forbidden" {
		t.Fatalf("start by an outsider: %q", code)
	}
	if _, code := carol.board(proto.BoardArgs{Op: "sprint.start", ID: 1}); code != "" {
		t.Fatalf("start by the role: %q", code)
	}
	if _, code := dave.board(proto.BoardArgs{Op: "sprint.close", ID: 1}); code != "forbidden" {
		t.Fatalf("close by an outsider: %q", code)
	}
	if _, code := dave.board(proto.BoardArgs{Op: "board.revert", LogID: 1}); code != "forbidden" {
		t.Fatalf("revert by an outsider: %q", code)
	}
	if _, code := dave.board(proto.BoardArgs{Op: "board.workflow_set", Workflow: []byte(`{"statuses":["a"],"initial":"a"}`)}); code != "forbidden" {
		t.Fatalf("process change by an outsider: %q", code)
	}
	if _, code := dave.board(proto.BoardArgs{Op: "task.comment", ID: 1, Text: "fine"}); code != "" {
		t.Fatalf("comments are open: %q", code)
	}
	if _, code := owner.board(proto.BoardArgs{Op: "board.revert", LogID: 1, Force: true}); code != "" && code != "conflict" {
		t.Fatalf("the owner may revert: %q", code)
	}
	_ = alice
}

func TestForbiddenNamesWhoMay(t *testing.T) {
	b, _ := boardBroker(t)
	tok, id := token(t)
	k := newKey(t)
	register(t, b, k)
	adm, a := b.connect(t), b.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)
	a.board(proto.BoardArgs{Op: "task.create", Title: sp("x")})
	adminReq(t, adm, k, "config_set", proto.AdminConfig{Chat: id, Key: "board.manager_role", Value: "lead"})
	var r proto.BoardReply
	if code := a.call("board", proto.BoardArgs{Op: "task.move", ID: 1, Status: "doing"}, &r); code != "forbidden" {
		t.Fatalf("code %q", code)
	}
}
