package peer

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func originsIn(j map[string]string) map[string]bool {
	out := map[string]bool{}
	for k := range j {
		out[k[:strings.IndexByte(k, '#')]] = true
	}
	return out
}

func TestASourceBroughtByATrustedNodeIsTakenAndCountedAgainstThatNode(t *testing.T) {
	a, b, c := newTnode(t, "alice"), newTnode(t, "bob"), newTnode(t, "carol")
	b.listen()
	for _, n := range []*tnode{a, b, c} {
		n.chat(chatX)
	}
	befriendAt(t, c, b, "trusted", chatX)
	befriendAt(t, a, b, "trusted", chatX)
	c.say(chatX, "carol brings this")
	eventually(t, wait, "alice holds carol's event through bob", func() bool { return originsIn(a.journal(chatX))[c.origin] })
	via, err := a.db.SourcesVia(b.origin)
	if err != nil || len(via) != 1 || via[0] != c.origin {
		t.Fatalf("sources brought by bob: %v %v", via, err)
	}
}

func TestASourceThatIsANeighborHereAtLevelReadIsNotTakenThroughATrustedNode(t *testing.T) {
	a, b, c := newTnode(t, "alice"), newTnode(t, "bob"), newTnode(t, "carol")
	b.listen()
	for _, n := range []*tnode{a, b, c} {
		n.chat(chatX)
	}
	befriendAt(t, c, b, "trusted", chatX)
	befriendAt(t, a, b, "trusted", chatX)
	// Carol is a neighbor of alice's too, whose events alice does not take.
	if err := a.db.AddPeer(c.origin, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.SetPeerTrust(c.origin, "read"); err != nil {
		t.Fatal(err)
	}
	a.m.Refresh()
	c.say(chatX, "carol, read here")
	b.say(chatX, "bob, a source of his own")
	eventually(t, wait, "alice holds bob's event", func() bool { return originsIn(a.journal(chatX))[b.origin] })
	eventually(t, wait, "bob holds carol's", func() bool { return originsIn(b.journal(chatX))[c.origin] })
	time.Sleep(500 * time.Millisecond)
	if originsIn(a.journal(chatX))[c.origin] {
		t.Fatal("a source that is read here came through a trusted node")
	}
	if st := a.m.Stats(b.origin); st.Refused["source that is read or pending here"] == 0 {
		t.Fatalf("not counted: %+v", st)
	}
}

func TestATrustedNodeBringsAtMostALimitOfSourcesAndTheOwnerIsToldOnceAndTheRefusedAreNotAskedForAgain(t *testing.T) {
	a, b := newTnode(t, "alice"), newTnode(t, "bob")
	srcs := []*tnode{newTnode(t, "carol"), newTnode(t, "dave"), newTnode(t, "erin")}
	l := DefaultLimits
	l.Sources = 2
	a.m.SetLimits(l)
	var alerts atomic.Int32
	a.m.OnAlert = func(origin, text string) {
		if origin != b.origin || !strings.Contains(text, "more than 2 sources") {
			t.Errorf("alert: %s %q", origin, text)
		}
		alerts.Add(1)
	}
	b.listen()
	for _, n := range append([]*tnode{a, b}, srcs...) {
		n.chat(chatX)
	}
	for _, s := range srcs {
		befriendAt(t, s, b, "trusted", chatX)
	}
	befriendAt(t, a, b, "trusted", chatX)
	for _, s := range srcs {
		s.say(chatX, "from "+s.name)
	}
	eventually(t, wait, "bob holds all three", func() bool { return len(originsIn(b.journal(chatX))) == 3 })
	eventually(t, wait, "alice holds two sources", func() bool { return len(originsIn(a.journal(chatX))) == 2 })
	eventually(t, wait, "alice refuses the third", func() bool { return a.m.Stats(b.origin).Refused["too many sources via it"] > 0 })
	// A frame already on its way when the vector that holds the source was sent may come once more; after
	// that the source is not asked for again: the count stops growing.
	time.Sleep(600 * time.Millisecond)
	first := a.m.Stats(b.origin).Refused["too many sources via it"]
	time.Sleep(800 * time.Millisecond)
	if got := len(originsIn(a.journal(chatX))); got != 2 {
		t.Fatalf("%d sources came through bob", got)
	}
	if n := a.m.Stats(b.origin).Refused["too many sources via it"]; n != first || n > 3 {
		t.Fatalf("the refused source is asked for again: %d, then %d", first, n)
	}
	if got := alerts.Load(); got != 1 {
		t.Fatalf("the owner was told %d times", got)
	}
	if via, _ := a.db.SourcesVia(b.origin); len(via) != 2 {
		t.Fatalf("%v", via)
	}
}
