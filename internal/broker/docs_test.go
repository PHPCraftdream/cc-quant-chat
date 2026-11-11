package broker

import (
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

func (c *client) doc(a proto.DocArgs) (proto.DocReply, string) {
	c.t.Helper()
	var r proto.DocReply
	code := c.call("doc", a, &r)
	return r, code
}

func presenceTexts(c *client) []string {
	var out []string
	for {
		select {
		case ev := <-c.events:
			if ev.Kind == "presence" {
				out = append(out, ev.Text)
			}
		case <-time.After(150 * time.Millisecond):
			return out
		}
	}
}

func TestDocsNeedTheDatabase(t *testing.T) {
	b := newTestBroker(newMemStore())
	tok, _ := token(t)
	a := b.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)
	if _, code := a.doc(proto.DocArgs{Op: "tree"}); code != "docs_unavailable" {
		t.Fatalf("code = %q, want docs_unavailable", code)
	}
}

func TestEveryMemberReadsAndWritesTheTreeOfItsChatOnly(t *testing.T) {
	b, path := boardBroker(t)
	tok1, _ := token(t)
	tok2, _ := token(t)
	a, c, other := b.connect(t), b.connect(t), b.connect(t)
	a.join(tok1, "alice", proto.ProtoVersion)
	c.join(tok1, "carol", proto.ProtoVersion)
	other.join(tok2, "mallory", proto.ProtoVersion)
	a.msgs()
	c.msgs()

	body := "# Broker\nstate lives in SQLite"
	r, code := a.doc(proto.DocArgs{Op: "put", Path: "/arch/broker", Body: body})
	if code != "" || r.Version != 1 || !strings.Contains(r.Text, "created v1") {
		t.Fatalf("put: %+v %q", r, code)
	}
	// A quiet line in the presence stream tells the others; nobody is woken by it.
	if lines := presenceTexts(c); len(lines) != 1 || lines[0] != "alice updated /arch/broker v1" {
		t.Fatalf("carol heard: %v", lines)
	}
	got, code := c.doc(proto.DocArgs{Op: "get", Path: "/arch/broker"})
	if code != "" || got.Body != body || got.Version != 1 {
		t.Fatalf("carol reads: %+v %q", got, code)
	}
	// Carol replaces it knowing the version she read; a stale base conflicts.
	base := got.Version
	if _, code := c.doc(proto.DocArgs{Op: "put", Path: "/arch/broker", Body: body + "\nmore", Base: &base}); code != "" {
		t.Fatalf("carol's put: %q", code)
	}
	if _, code := a.doc(proto.DocArgs{Op: "put", Path: "/arch/broker", Body: "mine", Base: &base}); code != "conflict" {
		t.Fatalf("alice's stale put: %q", code)
	}
	if tree, _ := a.doc(proto.DocArgs{Op: "tree", All: true}); !strings.Contains(tree.Text, "/arch/broker") || len(tree.Paths) != 1 {
		t.Fatalf("tree: %+v", tree)
	}
	if tree, _ := other.doc(proto.DocArgs{Op: "tree", All: true}); strings.Contains(tree.Text, "/arch") || len(tree.Paths) != 0 {
		t.Fatalf("a member of another chat sees the tree: %+v", tree)
	}
	if _, code := other.doc(proto.DocArgs{Op: "get", Path: "/arch/broker"}); code != "no_such_page" {
		t.Fatalf("page of another chat: %q", code)
	}

	// A new broker process on the same database sees it.
	b2 := newTestBroker(b.store)
	a2 := b2.connect(t)
	a2.key = a.key
	a2.join(tok1, "alice", proto.ProtoVersion)
	if got, code := a2.doc(proto.DocArgs{Op: "get", Path: "/arch/broker"}); code != "" || !strings.HasSuffix(got.Body, "more") || got.Version != 2 {
		t.Fatalf("after a restart: %+v %q (db %s)", got, code, path)
	}
}

func TestDocWritesShareTheBoardLimitAndStopWhileRestarting(t *testing.T) {
	b, _ := boardBroker(t)
	tok, _ := token(t)
	a := b.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)
	for i := 0; i < boardLimit; i++ {
		if _, code := a.doc(proto.DocArgs{Op: "put", Path: "/p/" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Body: "x"}); code != "" {
			t.Fatalf("write %d: %q", i, code)
		}
	}
	if _, code := a.doc(proto.DocArgs{Op: "put", Path: "/p/over", Body: "x"}); code != "rate_limited" {
		t.Fatalf("over the shared limit: %q", code)
	}
	if _, code := a.board(proto.BoardArgs{Op: "task.create", Title: sp("t")}); code != "rate_limited" {
		t.Fatalf("the board shares the limit: %q", code)
	}
	if _, code := a.doc(proto.DocArgs{Op: "tree"}); code != "" {
		t.Fatalf("reads are not limited: %q", code)
	}
	b.stopping.Store(true)
	if _, code := a.doc(proto.DocArgs{Op: "delete", Path: "/p/aa"}); code != "restarting" {
		t.Fatalf("a write while stopping: %q", code)
	}
}
