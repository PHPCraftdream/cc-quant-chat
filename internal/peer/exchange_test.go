package peer

import (
	"testing"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

const wait = 15 * time.Second

func TestTwoNodesExchangeTheirJournalsBothWaysAndKeepExchanging(t *testing.T) {
	a, b := newTnode(t, "alice"), newTnode(t, "bob")
	b.listen()
	a.chat(chatX)
	b.chat(chatX)
	a.say(chatX, "one")
	a.say(chatX, "two")
	b.say(chatX, "three")
	befriend(a, b, chatX)

	eventually(t, wait, "both journals hold all three events", func() bool {
		ja, jb := a.journal(chatX), b.journal(chatX)
		return len(ja) == 3 && sameJournal(ja, jb)
	})
	// Later events go across at once.
	b.say(chatX, "four")
	a.say(chatX, "five")
	eventually(t, wait, "five events", func() bool {
		ja, jb := a.journal(chatX), b.journal(chatX)
		return len(ja) == 5 && sameJournal(ja, jb)
	})
	for _, n := range []*tnode{a, b} {
		rep, err := n.db.Verify(chatX)
		if err != nil || len(rep.Issues) != 0 || rep.Events != 5 {
			t.Fatalf("%s: verify %+v %v", n.name, rep, err)
		}
	}
}

func TestOnlyTheChatsSharedByBothAreExchanged(t *testing.T) {
	a, b := newTnode(t, "alice"), newTnode(t, "bob")
	b.listen()
	for _, n := range []*tnode{a, b} {
		n.chat(chatX)
		n.chat(chatY)
	}
	a.say(chatX, "shared")
	a.say(chatY, "private to alice")
	// Y is shared by a only; X by both.
	if err := a.db.AddPeer(b.origin, b.addr); err != nil {
		t.Fatal(err)
	}
	if err := b.db.AddPeer(a.origin, ""); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{chatX, chatY} {
		if err := a.db.SharePeerChat(c, b.origin); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.db.SharePeerChat(chatX, a.origin); err != nil {
		t.Fatal(err)
	}
	a.m.Refresh()
	b.m.Refresh()
	eventually(t, wait, "X arrives", func() bool { return len(b.journal(chatX)) == 1 })
	time.Sleep(300 * time.Millisecond) // enough for Y to have gone, were it going to
	if got := b.journal(chatY); len(got) != 0 {
		t.Fatalf("a chat that b did not share was given to it: %v", got)
	}
	// Stop sharing X on a's side: what a says next stays with a.
	if err := a.db.UnsharePeerChat(chatX, b.origin); err != nil {
		t.Fatal(err)
	}
	a.m.Refresh()
	time.Sleep(300 * time.Millisecond)
	a.say(chatX, "after the share ended")
	time.Sleep(500 * time.Millisecond)
	if got := b.journal(chatX); len(got) != 1 {
		t.Fatalf("an unshared chat went on being given: %v", got)
	}
}

func TestAThirdNodeGetsTheEventsOfTheFirstThroughTheSecondAndChecksThem(t *testing.T) {
	a, b, c := newTnode(t, "alice"), newTnode(t, "bob"), newTnode(t, "carol")
	b.listen()
	c.listen()
	for _, n := range []*tnode{a, b, c} {
		n.chat(chatX)
	}
	a.say(chatX, "from alice")
	// a -- b -- c, a and c are not peers.
	befriend(a, b, chatX)
	befriend(b, c, chatX)
	eventually(t, wait, "alice's event reaches carol through bob", func() bool {
		return len(c.journal(chatX)) == 1
	})
	if !sameJournal(a.journal(chatX), c.journal(chatX)) {
		t.Fatal("carol holds something other than alice wrote")
	}
	if _, ok, _ := c.db.OriginKey(a.origin); !ok {
		t.Fatal("carol checked alice's event without knowing her key")
	}
	if got := c.m.Sessions(); len(got) != 1 || got[0] != b.origin {
		t.Fatalf("carol is connected to %v, want bob only", got)
	}
	// And the other way round.
	c.say(chatX, "from carol")
	eventually(t, wait, "carol's event reaches alice", func() bool { return len(a.journal(chatX)) == 2 })
	if rep, err := a.db.Verify(chatX); err != nil || len(rep.Issues) != 0 {
		t.Fatalf("alice: %+v %v", rep, err)
	}
}

func TestWhatIsPrivateGoesAcrossRedactedAndTheChainStillVerifies(t *testing.T) {
	a, b := newTnode(t, "alice"), newTnode(t, "bob")
	b.listen()
	a.chat(chatX)
	b.chat(chatX)
	a.say(chatX, "public")
	dm := &proto.Message{ID: 50, TS: time.Now().UTC().Format(time.RFC3339), Room: proto.RoomDirect, From: "alice", To: "dave", Text: "secret"}
	if err := a.db.Append(chatX, store.Record{T: "msg", Key: "d:alice|dave", Msg: dm}); err != nil {
		t.Fatal(err)
	}
	if err := a.db.Append(chatX, store.Record{T: "nick", Nick: "alice", Hash: "hash-of-the-secret", Pub: "alice-key"}); err != nil {
		t.Fatal(err)
	}
	if err := a.db.Append(chatX, store.Record{T: "pin", Nick: "alice", Role: "lead"}); err != nil {
		t.Fatal(err)
	}
	a.say(chatX, "after")
	befriend(a, b, chatX)
	eventually(t, wait, "five events", func() bool { return len(b.journal(chatX)) == 5 })
	if !sameJournal(a.journal(chatX), b.journal(chatX)) {
		t.Fatal("the chain of a redacted journal must give the same event IDs")
	}
	evs, err := b.db.EventsAfter(chatX, a.origin, 0, 10)
	if err != nil || len(evs) != 5 {
		t.Fatalf("%d events, %v", len(evs), err)
	}
	for _, e := range evs {
		switch e.Kind {
		case "msg":
			if e.Redacted() == (e.Seq != 1 && e.Seq != 5) { // the two public messages are whole, the direct one is not
				continue
			}
			t.Errorf("message #%d: redacted %v", e.Seq, e.Redacted())
		case "nick.register":
			if e.Redacted() || e.Payload == "" || e.Assigned != "{}" {
				t.Errorf("who a nick is goes whole, without its secret: %+v", e)
			}
		case "nick.pin":
			if !e.Redacted() {
				t.Error("a pinned role is the owner's business of the node that wrote it")
			}
		}
		if e.Redacted() && (e.Payload != "" || e.Sig != "" || e.Assigned != "") {
			t.Errorf("a redacted event leaks: %+v", e)
		}
	}
	if rep, err := b.db.Verify(chatX); err != nil || len(rep.Issues) != 0 || rep.Events != 5 {
		t.Fatalf("verify: %+v %v", rep, err)
	}
}

func TestAConnectionIsRestoredAndWhatWasMissedIsSent(t *testing.T) {
	a, b := newTnode(t, "alice"), newTnode(t, "bob")
	b.listen()
	a.chat(chatX)
	b.chat(chatX)
	befriend(a, b, chatX)
	a.say(chatX, "before")
	eventually(t, wait, "the first event", func() bool { return len(b.journal(chatX)) == 1 })
	eventually(t, wait, "a session", func() bool { return len(a.m.Sessions()) == 1 && len(b.m.Sessions()) == 1 })

	// The connection breaks; events are written meanwhile on both sides.
	for _, s := range a.m.sessionsOf(chatX) {
		s.close()
	}
	a.say(chatX, "while apart (a)")
	b.say(chatX, "while apart (b)")
	eventually(t, wait, "everything after the reconnection", func() bool {
		ja, jb := a.journal(chatX), b.journal(chatX)
		return len(ja) == 3 && sameJournal(ja, jb)
	})
}

func TestACertificateOfAnotherKeyIsRefused(t *testing.T) {
	a, b, impostor := newTnode(t, "alice"), newTnode(t, "bob"), newTnode(t, "impostor")
	impostor.listen()
	a.chat(chatX)
	impostor.chat(chatX)
	// Alice believes the node at this address is bob.
	if err := a.db.AddPeer(b.origin, impostor.addr); err != nil {
		t.Fatal(err)
	}
	if err := a.db.SharePeerChat(chatX, b.origin); err != nil {
		t.Fatal(err)
	}
	if err := impostor.db.AddPeer(a.origin, ""); err != nil { // the impostor would be glad to talk to her
		t.Fatal(err)
	}
	if err := impostor.db.SharePeerChat(chatX, a.origin); err != nil {
		t.Fatal(err)
	}
	impostor.say(chatX, "lies")
	a.m.Refresh()
	impostor.m.Refresh()
	eventually(t, wait, "a refusal is logged", func() bool { return a.logged("not the expected") })
	time.Sleep(200 * time.Millisecond)
	if len(a.journal(chatX)) != 0 || len(a.m.Sessions()) != 0 {
		t.Fatal("alice took events from a node that is not the one she added")
	}
}

func TestANodeThatIsNotAPeerCannotConnect(t *testing.T) {
	a, stranger := newTnode(t, "alice"), newTnode(t, "stranger")
	a.listen()
	a.chat(chatX)
	stranger.chat(chatX)
	if err := stranger.db.AddPeer(a.origin, a.addr); err != nil { // she was not added by alice
		t.Fatal(err)
	}
	if err := stranger.db.SharePeerChat(chatX, a.origin); err != nil {
		t.Fatal(err)
	}
	stranger.say(chatX, "let me in")
	stranger.m.Refresh()
	time.Sleep(700 * time.Millisecond)
	if len(a.journal(chatX)) != 0 || len(a.m.Sessions()) != 0 {
		t.Fatal("a node nobody added got in")
	}
}

func TestTwoNodesThatDialEachOtherEndUpWithOneSession(t *testing.T) {
	a, b := newTnode(t, "alice"), newTnode(t, "bob")
	a.listen()
	b.listen()
	a.chat(chatX)
	b.chat(chatX)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(a.db.AddPeer(b.origin, b.addr))
	must(b.db.AddPeer(a.origin, a.addr))
	must(a.db.SharePeerChat(chatX, b.origin))
	must(b.db.SharePeerChat(chatX, a.origin))
	a.say(chatX, "x")
	must(a.m.Refresh())
	must(b.m.Refresh())
	eventually(t, wait, "the event arrives", func() bool { return len(b.journal(chatX)) == 1 })
	time.Sleep(time.Second)
	if len(a.m.Sessions()) != 1 || len(b.m.Sessions()) != 1 {
		t.Fatalf("sessions: a %v, b %v", a.m.Sessions(), b.m.Sessions())
	}
	b.say(chatX, "y")
	eventually(t, wait, "and the way back", func() bool { return len(a.journal(chatX)) == 2 })
}

func TestOneConnectionInEitherDirectionIsEnoughAndTheOtherSideIsNotTried(t *testing.T) {
	a, b := newTnode(t, "alice"), newTnode(t, "bob")
	a.listen() // bob listens to no one, as a node behind a NAT
	a.chat(chatX)
	b.chat(chatX)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// Alice has an address for bob that nobody answers at; bob reaches alice by a URI.
	must(a.db.AddPeer(b.origin, "tcp://127.0.0.1:1"))
	must(b.db.AddPeer(a.origin, "tcp://"+a.addr))
	must(a.db.SharePeerChat(chatX, b.origin))
	must(b.db.SharePeerChat(chatX, a.origin))
	a.say(chatX, "from alice")
	must(a.m.Refresh())
	must(b.m.Refresh())

	eventually(t, wait, "alice's event reaches bob", func() bool { return len(b.journal(chatX)) == 1 })
	b.say(chatX, "from bob")
	eventually(t, wait, "bob's event reaches alice, over the same connection", func() bool { return len(a.journal(chatX)) == 2 })
	if len(a.m.Sessions()) != 1 || len(b.m.Sessions()) != 1 {
		t.Fatalf("sessions: a %v, b %v", a.m.Sessions(), b.m.Sessions())
	}
	time.Sleep(3 * time.Second)
	if n := a.logCount(b.origin + " at"); n > 1 {
		t.Fatalf("alice logged %d failed attempts to reach bob; a node that is connected is not dialed, and a failure is told once", n)
	}
	if len(a.m.Sessions()) != 1 || len(b.m.Sessions()) != 1 {
		t.Fatalf("a working session was closed: a %v, b %v", a.m.Sessions(), b.m.Sessions())
	}

	// The connection breaks: bob, who dials, restores it.
	for _, s := range b.m.sessionsOf(chatX) {
		s.close()
	}
	a.say(chatX, "while apart")
	eventually(t, wait, "bob restores the connection and catches up", func() bool {
		jb := b.journal(chatX)
		return len(jb) == 3 && sameJournal(a.journal(chatX), jb)
	})
}
