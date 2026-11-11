package sqlite

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/invite"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func nodeInvite(t *testing.T, scope store.NodeScope) (invite.Invitation, invite.Record) {
	t.Helper()
	in, err := invite.New(invite.KindNode, 0, t0, func(i *invite.Invitation) { i.Key = "key" })
	if err != nil {
		t.Fatal(err)
	}
	return in, invite.NewRecord(in, scope.String(), "owner", t0)
}

func TestAnInvitationOfANodeIsUsedOnceAndListsTheNodeAsPendingWithItsChats(t *testing.T) {
	db, _ := openTemp(t)
	chat := store.ChatRef{ID: strings.Repeat("ab", 32), Name: "team"}
	_, rec := nodeInvite(t, store.NodeScope{Trust: "member", Chats: []store.ChatRef{chat}})
	if err := db.IssueNodeInvite(rec); err != nil {
		t.Fatal(err)
	}
	b := strings.Repeat("b", 32)
	p := store.Pending{Origin: b, Pub: []byte("pub"), Trust: "trusted", Until: t0.Add(time.Hour), Code: "123456"}
	if _, err := db.AcceptNodeInvite(rec.ID, t0, p, func(invite.Record) bool { return false }); !errors.Is(err, invite.ErrInvalid) {
		t.Fatalf("a wrong proof: %v", err)
	}
	if ps, _ := db.Peers(); len(ps) != 0 {
		t.Fatalf("a wrong proof listed a node: %+v", ps)
	}
	if _, err := db.AcceptNodeInvite(rec.ID, t0, p, func(invite.Record) bool { return true }); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AcceptNodeInvite(rec.ID, t0, p, func(invite.Record) bool { return true }); !errors.Is(err, invite.ErrInvalid) {
		t.Fatalf("the second use: %v", err)
	}
	ps, err := db.Peers()
	if err != nil || len(ps) != 1 {
		t.Fatalf("%+v %v", ps, err)
	}
	// The level is the invitation's, not what the node asked for; the chats are held, not yet exchanged.
	if g := ps[0]; g.State != "pending" || g.Trust != "member" || len(g.Chats) != 1 || g.Chats[0] != chat.ID || g.PendingUntil == "" {
		t.Errorf("%+v", g)
	}
	if code, ok, _ := db.PendingCode(b); !ok || code != "123456" {
		t.Errorf("%q %v", code, ok)
	}
}

func TestThreeWrongProofsBurnANodeInvitationAndLeaveNoNode(t *testing.T) {
	db, _ := openTemp(t)
	_, rec := nodeInvite(t, store.NodeScope{Trust: "member"})
	if err := db.IssueNodeInvite(rec); err != nil {
		t.Fatal(err)
	}
	p := store.Pending{Origin: strings.Repeat("b", 32), Until: t0.Add(time.Hour), Code: "1"}
	var bad *invite.BadProof
	for i := 1; i <= invite.MaxTries; i++ {
		_, err := db.AcceptNodeInvite(rec.ID, t0, p, func(invite.Record) bool { return false })
		if !errors.As(err, &bad) || bad.Tries != i {
			t.Fatalf("try %d: %v", i, err)
		}
	}
	if _, err := db.AcceptNodeInvite(rec.ID, t0, p, func(invite.Record) bool { return true }); !errors.Is(err, invite.ErrInvalid) {
		t.Fatalf("a burnt invitation worked: %v", err)
	}
	if ps, _ := db.Peers(); len(ps) != 0 {
		t.Fatalf("%+v", ps)
	}
}

func TestBothOwnersConfirmingMakesTheNodeActiveAndAWrongCodeCountsThree(t *testing.T) {
	db, _ := openTemp(t)
	o := strings.Repeat("c", 32)
	pend := store.Pending{Origin: o, Addr: "tcp://127.0.0.1:1", Trust: "member", Until: t0.Add(time.Hour), Code: "424242",
		Chats: []store.ChatRef{{ID: strings.Repeat("ab", 32), Name: "team"}}}
	if err := db.AddPendingPeer(pend); err != nil {
		t.Fatal(err)
	}
	if cs, _ := db.PendingChats(o); len(cs) != 1 || cs[0].Name != "team" {
		t.Fatalf("%+v", cs)
	}
	if r, err := db.Confirm(o, "000000"); err != nil || r.Right || r.Tries != 1 || r.Gone {
		t.Fatalf("%+v %v", r, err)
	}
	if r, err := db.Confirm(o, "424242"); err != nil || !r.Right || r.Active {
		t.Fatalf("this side only: %+v %v", r, err)
	}
	if active, err := db.ConfirmedRemote(o); err != nil || !active {
		t.Fatalf("both: %v %v", active, err)
	}
	ps, _ := db.Peers()
	if g := ps[0]; g.State != "active" || g.PendingUntil != "" || !g.Confirmed {
		t.Errorf("%+v", g)
	}
	if _, ok, _ := db.PendingCode(o); ok {
		t.Error("the code outlived the pending state")
	}

	// Three wrong codes drop a node that is still pending, with what was offered.
	z := strings.Repeat("d", 32)
	if err := db.AddPendingPeer(store.Pending{Origin: z, Trust: "read", Until: t0.Add(time.Hour), Code: "111111"}); err != nil {
		t.Fatal(err)
	}
	var r store.ConfirmResult
	for i := 0; i < 3; i++ {
		r, _ = db.Confirm(z, "999999")
	}
	if !r.Gone || r.Tries != 3 {
		t.Fatalf("%+v", r)
	}
	if _, err := db.Confirm(z, "111111"); err == nil {
		t.Error("a dropped node was confirmed")
	}
}

func TestAPendingNodeIsDroppedWhenItsTimeIsOutAndAnActiveOneIsNot(t *testing.T) {
	db, _ := openTemp(t)
	old, fresh, active := strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Repeat("3", 32)
	for o, until := range map[string]time.Time{old: t0.Add(time.Minute), fresh: t0.Add(time.Hour)} {
		if err := db.AddPendingPeer(store.Pending{Origin: o, Trust: "member", Until: until, Code: "1"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.AddPeer(active, ""); err != nil {
		t.Fatal(err)
	}
	gone, err := db.DropExpired(t0.Add(10 * time.Minute))
	if err != nil || len(gone) != 1 || gone[0].Origin != old {
		t.Fatalf("%+v %v", gone, err)
	}
	if ps, _ := db.Peers(); len(ps) != 2 {
		t.Fatalf("%+v", ps)
	}
}
