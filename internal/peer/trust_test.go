package peer

import (
	"testing"
	"time"
)

// befriendAt is befriend with the level that a accepts from b set before the connection is made.
func befriendAt(t *testing.T, a, b *tnode, level string, chats ...string) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(a.db.AddPeer(b.origin, b.addr))
	if _, err := a.db.SetPeerTrust(b.origin, level); err != nil {
		t.Fatal(err)
	}
	must(b.db.AddPeer(a.origin, ""))
	for _, c := range chats {
		must(a.db.SharePeerChat(c, b.origin))
		must(b.db.SharePeerChat(c, a.origin))
	}
	must(a.m.Refresh())
	must(b.m.Refresh())
}

func TestANodeThatIsOnlyReadIsGivenEventsAndHisAreNotTaken(t *testing.T) {
	a, b := newTnode(t, "alice"), newTnode(t, "bob")
	b.listen()
	a.chat(chatX)
	b.chat(chatX)
	befriendAt(t, a, b, "read", chatX) // alice takes nothing from bob
	eventually(t, wait, "a session", func() bool { return len(a.m.Sessions()) == 1 && len(b.m.Sessions()) == 1 })
	b.say(chatX, "from bob")
	a.say(chatX, "from alice")
	eventually(t, wait, "bob gets what alice says", func() bool { return len(b.journal(chatX)) == 2 })
	time.Sleep(600 * time.Millisecond) // long enough for bob's event to have come, were it going to
	if got := a.journal(chatX); len(got) != 1 {
		t.Fatalf("alice took events from a node that is read-only here: %v", got)
	}
}

func TestAMemberBringsHisOwnEventsAndNotThoseOfOthersAndAnUpgradeBringsThemAndADowngradeStopsNewOnes(t *testing.T) {
	a, b, c := newTnode(t, "alice"), newTnode(t, "bob"), newTnode(t, "carol")
	b.listen()
	for _, n := range []*tnode{a, b, c} {
		n.chat(chatX)
	}
	befriendAt(t, c, b, "trusted", chatX) // carol gives bob her events
	befriendAt(t, a, b, "member", chatX)  // alice takes from bob only his own
	c.say(chatX, "carol one")
	b.say(chatX, "bob one")
	eventually(t, wait, "alice holds bob's event", func() bool { return len(a.journal(chatX)) >= 1 })
	eventually(t, wait, "bob holds carol's and his own", func() bool { return len(b.journal(chatX)) == 2 })
	time.Sleep(500 * time.Millisecond)
	if got := a.journal(chatX); len(got) != 1 {
		t.Fatalf("a member brought the event of another source: %v", got)
	}
	if st := a.m.Stats(b.origin); st.Accepted == 0 {
		t.Fatalf("nothing counted as taken: %+v", st)
	}

	// Trusted: alice takes the events of the sources that bob brings.
	if _, err := a.db.SetPeerTrust(b.origin, "trusted"); err != nil {
		t.Fatal(err)
	}
	a.m.Refresh()
	eventually(t, wait, "alice holds carol's event too", func() bool { return len(a.journal(chatX)) == 2 })

	// Member again: what was taken stays, what carol says next does not come through bob.
	if _, err := a.db.SetPeerTrust(b.origin, "member"); err != nil {
		t.Fatal(err)
	}
	a.m.Refresh()
	eventually(t, wait, "a session again", func() bool { return len(a.m.Sessions()) == 1 })
	c.say(chatX, "carol two")
	eventually(t, wait, "bob holds carol's second", func() bool { return len(b.journal(chatX)) == 3 })
	b.say(chatX, "bob two")
	eventually(t, wait, "alice holds bob's second", func() bool { return len(a.journal(chatX)) == 3 })
	time.Sleep(500 * time.Millisecond)
	if got := a.journal(chatX); len(got) != 3 {
		t.Fatalf("after the downgrade: %v (carol's first stays, her second is not taken)", got)
	}
}
