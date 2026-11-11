package broker

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store/sqlite"
)

// projection is everything the projections of a chat show, with the numbers: messages (number, room,
// sender, text, the message replied to), tasks, comments and sprints.
func projection(t *testing.T, db *sqlite.DB, chatID string) string {
	t.Helper()
	var lines []string
	recs, err := db.Load(chatID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.T == "msg" && r.Msg != nil {
			m := r.Msg
			lines = append(lines, fmt.Sprintf("msg #%d @%s %s: %q reply=%d to=%q", m.ID, m.Room, m.From, m.Text, m.ReplyTo, m.To))
		}
	}
	err = db.View(chatID, func(tx store.BoardTx) error {
		sprints, err := tx.ListSprints()
		if err != nil {
			return err
		}
		for _, s := range sprints {
			lines = append(lines, fmt.Sprintf("sprint #%d %q %s deleted=%v", s.ID, s.Name, s.State, s.Deleted))
		}
		tasks, err := tx.ListTasks(store.TaskFilter{Sprint: -1})
		if err != nil {
			return err
		}
		for _, k := range tasks {
			lines = append(lines, fmt.Sprintf("task #%d %q %q %s sprint=%d p%d", k.ID, k.Title, k.Description, k.Status, k.Sprint, k.Priority))
			comments, err := tx.ListComments(k.ID, 0, 100)
			if err != nil {
				return err
			}
			for _, c := range comments {
				lines = append(lines, fmt.Sprintf("comment #%d on #%d: %q", c.ID, k.ID, c.Text))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

var revertID = regexp.MustCompile(`--undo ([0-9a-f]{12})`)

func TestRevertingANodeLeavesItsContributionOutOfTheProjectionsKeepsTheJournalAndUndoRestoresEverythingExactly(t *testing.T) {
	p := newTwoBrokers(t)
	k := newKey(t)
	register(t, p.a, k)
	alice, adm, bob := p.a.connect(t), p.a.connect(t), p.b.connect(t)
	if _, code := alice.join(p.tok, "alice", proto.ProtoVersion); code != "" {
		t.Fatalf("alice joins: %q", code)
	}
	if _, code := bob.join(p.tok, "bob", proto.ProtoVersion); code != "" {
		t.Fatalf("bob joins: %q", code)
	}
	bobAtA := remote("bob", p.b)
	eventually(t, "alice knows bob", hasMember(t, alice, bobAtA))

	// What both give: messages, a reply of alice's to bob's message, tasks, a comment of alice's on
	// bob's task, and bob's edit of a field of alice's task.
	if _, code := alice.send(proto.RoomCommon, "", "alice one"); code != "" {
		t.Fatal(code)
	}
	if _, code := bob.send(proto.RoomCommon, "", "bob one"); code != "" {
		t.Fatal(code)
	}
	bobOne := next(t, alice, "bob's message", func(m proto.Message, _ bool) bool { return m.Text == "bob one" })
	if code := alice.call("send", proto.SendArgs{Room: proto.RoomCommon, Text: "alice reply", ReplyTo: bobOne.ID}, nil); code != "" {
		t.Fatal(code)
	}
	mustBoard(t, alice, proto.BoardArgs{Op: "task.create", Title: sp("alice task")})
	mustBoard(t, bob, proto.BoardArgs{Op: "task.create", Title: sp("bob task")})
	eventually(t, "both tasks on both nodes", func() bool {
		return taskByTitle(t, p.adb, p.id, "bob task") != 0 && taskByTitle(t, p.bdb, p.id, "alice task") != 0
	})
	mustBoard(t, alice, proto.BoardArgs{Op: "task.comment", ID: taskByTitle(t, p.adb, p.id, "bob task"), Text: "alice on bob's task"})
	mustBoard(t, bob, proto.BoardArgs{Op: "task.update", ID: taskByTitle(t, p.bdb, p.id, "alice task"), Title: sp("alice task, renamed by bob")})
	eventually(t, "the projections agree", func() bool {
		a := boardOf(t, p.adb, p.id)
		return a == boardOf(t, p.bdb, p.id) && strings.Contains(a, "renamed by bob") && strings.Contains(a, "alice on bob's task") && textsOf(t, p.adb, p.id) == textsOf(t, p.bdb, p.id)
	})
	before := projection(t, p.adb, p.id)
	if !strings.Contains(before, `"bob one"`) || !strings.Contains(before, `reply=`+fmt.Sprint(bobOne.ID)) {
		t.Fatalf("the starting point is not what the test needs:\n%s", before)
	}
	journalBefore, _ := p.adb.Vector(p.id)

	// Only the owner reverts.
	if _, code := adminCall(t, adm, newKey(t), "peer_revert", proto.PeerRevert{Origin: p.b.origin}); code != "admin_only" {
		t.Fatalf("without the owner's signature: %q", code)
	}
	if _, code := adminCall(t, adm, k, "peer_revert", proto.PeerRevert{Origin: p.a.origin}); code != "bad_args" {
		t.Fatalf("this node: %q", code)
	}
	if _, code := adminCall(t, adm, k, "peer_revert", proto.PeerRevert{Origin: otherNode}); code != "no_such_events" {
		t.Fatalf("a node with no events here: %q", code)
	}
	text, code := adminCall(t, adm, k, "peer_revert", proto.PeerRevert{Origin: p.b.origin})
	if code != "" {
		t.Fatalf("peer_revert: %q %q", text, code)
	}
	m := revertID.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("no id to undo with: %q", text)
	}
	id := m[1]

	// The projections are as if bob had never been here: what he wrote is gone, his edit of alice's
	// task is gone, alice's comment on his task (it names a thing that is gone) is left out, and her
	// reply to his message stays, with no link.
	after := projection(t, p.adb, p.id)
	for _, gone := range []string{"bob one", "bob task", "alice on bob's task", "renamed by bob"} {
		if strings.Contains(after, gone) {
			t.Fatalf("%q is still shown:\n%s", gone, after)
		}
	}
	if !strings.Contains(after, `"alice task"`) || !strings.Contains(after, `"alice one"`) || !strings.Contains(after, `"alice reply" reply=0`) {
		t.Fatalf("what alice wrote:\n%s", after)
	}
	// The journal keeps everything and verifies; the projections are what the journal gives.
	if vec, _ := p.adb.Vector(p.id); vec[p.b.origin] != journalBefore[p.b.origin] || vec[p.b.origin] == 0 {
		t.Fatalf("bob's events: %v then %v", journalBefore, vec)
	}
	consistent(t, p.adb, "the revert")
	// The count of this node's own events that were left out is shown.
	var l proto.PeerList
	if code := adm.call("peer_list", nil, &l); code != "" || len(l.Peers) != 1 || !strings.Contains(l.Peers[0].Reverted, "1 of this node's own") {
		t.Fatalf("peer list: %q %+v", code, l.Peers)
	}
	// The chat saw it, and what the broker holds in memory is what the store shows: alice's history.
	var h proto.HistoryResult
	if code := alice.call("history", proto.HistoryArgs{Room: proto.RoomCommon}, &h); code != "" {
		t.Fatal(code)
	}
	for _, msg := range h.Messages {
		if msg.From == bobAtA {
			t.Fatalf("the history still has a message of bob's: %+v", msg)
		}
	}

	// Undoing it brings back exactly what was there before: the same rows under the same numbers.
	if _, code := adminCall(t, adm, k, "peer_revert", proto.PeerRevert{Undo: "000000000000"}); code != "no_such_revert" {
		t.Fatalf("an id that is not a revert: %q", code)
	}
	if text, code := adminCall(t, adm, k, "peer_revert", proto.PeerRevert{Undo: id}); code != "" {
		t.Fatalf("undo: %q %q", text, code)
	}
	if got := projection(t, p.adb, p.id); got != before {
		t.Fatalf("after undo the projections differ from what they were:\n--- before\n%s\n--- after\n%s", before, got)
	}
	consistent(t, p.adb, "the undo")
	if _, code := adminCall(t, adm, k, "peer_revert", proto.PeerRevert{Undo: id}); code != "no_such_revert" {
		t.Fatalf("undone twice: %q", code)
	}

	// A revert holds for what the node gives afterwards too: it is kept in the journal, not shown.
	if text, code = adminCall(t, adm, k, "peer_revert", proto.PeerRevert{Origin: p.b.origin}); code != "" {
		t.Fatalf("%q %q", text, code)
	}
	id = revertID.FindStringSubmatch(text)[1]
	if _, code := bob.send(proto.RoomCommon, "", "bob later"); code != "" {
		t.Fatal(code)
	}
	eventually(t, "bob's later event is in the journal of a", func() bool {
		vec, _ := p.adb.Vector(p.id)
		return vec[p.b.origin] > journalBefore[p.b.origin]
	})
	if strings.Contains(projection(t, p.adb, p.id), "bob later") {
		t.Fatal("a message of a reverted node was shown")
	}
	consistent(t, p.adb, "an event after the revert")
	if _, code := adminCall(t, adm, k, "peer_revert", proto.PeerRevert{Undo: id}); code != "" {
		t.Fatalf("undo: %q", code)
	}
	if got := projection(t, p.adb, p.id); !strings.Contains(got, "bob later") || !strings.Contains(got, `"bob one"`) {
		t.Fatalf("after the second undo:\n%s", got)
	}
	consistent(t, p.adb, "the second undo")
}
