package broker

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store/sqlite"
)

// boardOf is the board of a chat as words, without the numbers (each node has its own): the tasks
// with their status, sprint name and priority, the sprints, the visible comments.
func boardOf(t *testing.T, db *sqlite.DB, chatID string) string {
	t.Helper()
	var lines []string
	err := db.View(chatID, func(tx store.BoardTx) error {
		sprints, err := tx.ListSprints()
		if err != nil {
			return err
		}
		names := map[int64]string{0: "-"}
		for _, s := range sprints {
			names[s.ID] = s.Name
			lines = append(lines, fmt.Sprintf("sprint %q %s deleted=%v", s.Name, s.State, s.Deleted))
		}
		tasks, err := tx.ListTasks(store.TaskFilter{Sprint: -1})
		if err != nil {
			return err
		}
		for _, k := range tasks {
			line := fmt.Sprintf("task %q %q %s sprint=%s p%d", k.Title, k.Description, k.Status, names[k.Sprint], k.Priority)
			comments, err := tx.ListComments(k.ID, 0, 100)
			if err != nil {
				return err
			}
			var texts []string
			for _, c := range comments {
				texts = append(texts, c.Text)
			}
			sort.Strings(texts)
			lines = append(lines, line+" comments="+strings.Join(texts, "|"))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// taskByTitle is the number a node shows a task under.
func taskByTitle(t *testing.T, db *sqlite.DB, chatID, title string) int64 {
	t.Helper()
	var id int64
	_ = db.View(chatID, func(tx store.BoardTx) error {
		tasks, err := tx.ListTasks(store.TaskFilter{Sprint: -1})
		for _, k := range tasks {
			if k.Title == title {
				id = k.ID
			}
		}
		return err
	})
	return id
}

func sprintByName(t *testing.T, db *sqlite.DB, chatID, name string) int64 {
	t.Helper()
	var id int64
	_ = db.View(chatID, func(tx store.BoardTx) error {
		list, err := tx.ListSprints()
		for _, s := range list {
			if s.Name == name {
				id = s.ID
			}
		}
		return err
	})
	return id
}

func mustBoard(t *testing.T, c *client, a proto.BoardArgs) string {
	t.Helper()
	text, code := c.board(a)
	if code != "" {
		t.Fatalf("%s: %q", a.Op, code)
	}
	return text
}

func TestTheBoardsOfTwoNodesMeetAndConcurrentEditsOfOneFieldAgree(t *testing.T) {
	p := newTwoBrokers(t)
	alice, bob := p.a.connect(t), p.b.connect(t)
	if _, code := alice.join(p.tok, "alice", proto.ProtoVersion); code != "" {
		t.Fatalf("alice joins: %q", code)
	}
	if _, code := bob.join(p.tok, "bob", proto.ProtoVersion); code != "" {
		t.Fatalf("bob joins: %q", code)
	}

	// A task made at a, with a sprint, arrives at b in b's numbers.
	mustBoard(t, alice, proto.BoardArgs{Op: "sprint.create", Name: sp("first"), Goal: sp("g")})
	spA := sprintByName(t, p.adb, p.id, "first")
	mustBoard(t, alice, proto.BoardArgs{Op: "task.create", Title: sp("write the docs"), Sprint: &spA})
	eventually(t, "the task and the sprint reach b", func() bool {
		return taskByTitle(t, p.bdb, p.id, "write the docs") != 0 && sprintByName(t, p.bdb, p.id, "first") != 0
	})
	if got := boardOf(t, p.bdb, p.id); !strings.Contains(got, `sprint=first`) {
		t.Fatalf("the task at b is not in the sprint of b:\n%s", got)
	}

	// Moves, comments and the start of the sprint go back and forth.
	idB := taskByTitle(t, p.bdb, p.id, "write the docs")
	mustBoard(t, bob, proto.BoardArgs{Op: "task.move", ID: idB, Status: "doing"})
	mustBoard(t, bob, proto.BoardArgs{Op: "task.comment", ID: idB, Text: "on it"})
	idA := taskByTitle(t, p.adb, p.id, "write the docs")
	eventually(t, "the move and the comment reach a", func() bool {
		return strings.Contains(boardOf(t, p.adb, p.id), `doing sprint=first p0 comments=on it`)
	})
	mustBoard(t, alice, proto.BoardArgs{Op: "task.comment", ID: idA, Text: "thanks"})
	mustBoard(t, alice, proto.BoardArgs{Op: "sprint.start", ID: spA})
	eventually(t, "the boards agree", func() bool { return boardOf(t, p.adb, p.id) == boardOf(t, p.bdb, p.id) })
	if got := boardOf(t, p.bdb, p.id); !strings.Contains(got, "active") || !strings.Contains(got, "on it|thanks") {
		t.Fatalf("b does not hold what a did:\n%s", got)
	}

	// The two nodes cut off from each other edit the same field of the same task, and more.
	setShared := func(share bool) {
		t.Helper()
		for _, n := range []struct {
			db *sqlite.DB
			to string
			b  *Broker
		}{{p.adb, p.b.origin, p.a}, {p.bdb, p.a.origin, p.b}} {
			var err error
			if share {
				err = n.db.SharePeerChat(p.id, n.to)
			} else {
				err = n.db.UnsharePeerChat(p.id, n.to)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := n.b.peerMgr.Refresh(); err != nil {
				t.Fatal(err)
			}
		}
	}
	setShared(false)
	eventually(t, "the nodes are apart", func() bool { return len(p.a.peerMgr.Sessions()) == 0 && len(p.b.peerMgr.Sessions()) == 0 })
	mustBoard(t, alice, proto.BoardArgs{Op: "task.update", ID: idA, Title: sp("title from a"), Priority: ip(1)})
	mustBoard(t, bob, proto.BoardArgs{Op: "task.update", ID: idB, Title: sp("title from b"), Description: sp("only b wrote this")})
	mustBoard(t, bob, proto.BoardArgs{Op: "task.create", Title: sp("made at b while apart")})
	setShared(true)

	eventually(t, "the boards agree again", func() bool {
		a, b := boardOf(t, p.adb, p.id), boardOf(t, p.bdb, p.id)
		return a == b && strings.Contains(a, "made at b while apart")
	})
	got := boardOf(t, p.adb, p.id)
	if !strings.Contains(got, `"title from a"`) && !strings.Contains(got, `"title from b"`) {
		t.Fatalf("one of the two titles stands:\n%s", got)
	}
	if strings.Contains(got, `"title from a"`) && strings.Contains(got, `"title from b"`) {
		t.Fatalf("both titles stand:\n%s", got)
	}
	// The fields that were not contended come from their own writers.
	if !strings.Contains(got, "only b wrote this") || !strings.Contains(got, "p1") {
		t.Fatalf("fields written by one node alone were lost:\n%s", got)
	}
	consistent(t, p.adb, "the boards meet (node a)")
	consistent(t, p.bdb, "the boards meet (node b)")
}

func ip(n int) *int { return &n }
