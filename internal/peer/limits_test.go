package peer

import (
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/events"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

var t0 = time.Date(2026, 3, 1, 12, 0, 30, 0, time.UTC)

func ev(origin string, lamport int64) events.Event {
	return events.Event{Origin: origin, Lamport: lamport, Payload: "{}"}
}

// feed offers an event to the limits and, if it passes, counts it as taken, as a session does.
func feed(m *Manager, e events.Event, clock int64, now time.Time) (why, q string) {
	if why, q = m.overLimit(e, clock, now); why == "" {
		m.took(e, clock, now)
	}
	return
}

func TestASourceAtThreeHundredAMinuteIsNotInBreachAndTheNextIsRefused(t *testing.T) {
	n := newTnode(t, "alice")
	for i := 0; i < 300; i++ {
		if why, _ := feed(n.m, ev("src", 1), 5, t0); why != "" {
			t.Fatalf("event %d: %s", i+1, why)
		}
	}
	if why, _ := feed(n.m, ev("src", 1), 5, t0); !strings.Contains(why, "300 events a minute") {
		t.Fatalf("the 301st: %q", why)
	}
	// The next minute begins afresh.
	if why, _ := feed(n.m, ev("src", 1), 5, t0.Add(time.Minute)); why != "" {
		t.Fatalf("a minute later: %q", why)
	}
}

func TestOnlyTheExcessOfTheClockOverOursCountsAndHonestSourcesDoNotBreachItButOneThatJumpsDoes(t *testing.T) {
	n := newTnode(t, "alice")
	// Honest: 300 events whose clock follows ours by one: no excess at all.
	clock := int64(1000)
	for i := 0; i < 300; i++ {
		e := ev("honest", clock+1)
		if why, _ := feed(n.m, e, clock, t0.Add(time.Duration(i)*100*time.Millisecond)); why != "" {
			t.Fatalf("honest %d: %s", i, why)
		}
		clock = e.Lamport
	}
	// A source whose every event is 100 above our clock: ten fit in an hour, the eleventh breaks it.
	clock = 5000
	for i := 1; i <= 11; i++ {
		e := ev("jumper", clock+101) // the excess is 100: the clock plus one is what an honest event has
		why, _ := feed(n.m, e, clock, t0.Add(time.Duration(i)*time.Second))
		if (i <= 10) != (why == "") {
			t.Fatalf("event %d: %q", i, why)
		}
		if why != "" && !strings.Contains(why, "runs ahead") {
			t.Fatalf("%q", why)
		}
		clock = e.Lamport
	}
	// An hour later it may jump again.
	if why, _ := feed(n.m, ev("jumper", clock+101), clock, t0.Add(time.Hour+time.Minute)); why != "" {
		t.Fatalf("an hour later: %q", why)
	}
}

func TestBytesOfADayAreLimitedAndCountedInTheStoreSoARestartDoesNotForgetThem(t *testing.T) {
	n := newTnode(t, "alice")
	l := DefaultLimits
	l.BytesPerDay = 1000
	n.m.SetLimits(l)
	big := events.Event{Origin: "src", Lamport: 1, Payload: strings.Repeat("x", 300)} // counts 556
	if why, _ := feed(n.m, big, 0, t0); why != "" {
		t.Fatal(why)
	}
	n.m.flushLimits()
	m2 := New(n.db, n.origin, n.priv, nil) // the broker restarts
	defer m2.Close()
	m2.SetLimits(l)
	if got := m2.DayStatsAt("src", t0); got.Events != 1 || got.Bytes != 556 {
		t.Fatalf("what the store remembers: %+v", got)
	}
	if why, _ := feed(m2, big, 0, t0.Add(time.Second)); !strings.Contains(why, "a day") {
		t.Fatalf("a second one after the restart: %q", why)
	}
	// Tomorrow it is clean.
	if why, _ := feed(m2, big, 0, t0.Add(24*time.Hour)); why != "" {
		t.Fatalf("the next day: %q", why)
	}
}

func TestThreeBreachesInADayMakeAQuarantineEvenAcrossARestartAndASustainedOneToo(t *testing.T) {
	n := newTnode(t, "alice")
	l := DefaultLimits
	l.EventsPerMin = 1
	n.m.SetLimits(l)
	breach := func(m *Manager, src string, at time.Time) string {
		feed(m, ev(src, 1), 0, at) // the one that fits
		_, q := feed(m, ev(src, 1), 0, at)
		return q
	}
	if q := breach(n.m, "flaky", t0); q != "" {
		t.Fatalf("the first: %q", q)
	}
	if q := breach(n.m, "flaky", t0.Add(3*time.Minute)); q != "" {
		t.Fatalf("the second: %q", q)
	}
	m2 := New(n.db, n.origin, n.priv, nil) // a restart between the second and the third
	defer m2.Close()
	m2.SetLimits(l)
	if q := breach(m2, "flaky", t0.Add(6*time.Minute)); !strings.Contains(q, "3 times today") {
		t.Fatalf("the third: %q", q)
	}

	// One breach that does not stop: the pauses are shorter than a minute and it lasts five.
	var q string
	for i := 0; i < 12 && q == ""; i++ {
		q = breach(n.m, "steady", t0.Add(time.Duration(i)*30*time.Second))
	}
	if !strings.Contains(q, "goes on") {
		t.Fatalf("sustained: %q", q)
	}
}

func TestAnOverLimitSourceIsRefusedWithoutEndingTheSessionAndAnHonestOneGoesOn(t *testing.T) {
	a, b, c := newTnode(t, "alice"), newTnode(t, "bob"), newTnode(t, "carol")
	b.listen()
	for _, n := range []*tnode{a, b, c} {
		n.chat(chatX)
	}
	l := DefaultLimits
	l.EventsPerMin = 5
	a.m.SetLimits(l)
	befriendAt(t, c, b, "trusted", chatX)
	befriendAt(t, a, b, "trusted", chatX)
	eventually(t, wait, "a session", func() bool { return len(a.m.Sessions()) == 1 })
	// The limit counts per calendar minute, so a flood that straddles a minute boundary is split in two: with
	// 11 events one minute always gets 6 or more, and at most 2*5 are taken.
	for i := 0; i < 11; i++ { // bob, over the limit that alice set for a source
		b.say(chatX, "flood")
	}
	eventually(t, wait, "alice refuses some", func() bool { return a.m.Stats(b.origin).Refused["over a limit"] > 0 })
	if got := len(a.journal(chatX)); got > 10 {
		t.Fatalf("alice took %d events of a source with a limit of 5 a minute", got)
	}
	if len(a.m.Sessions()) != 1 {
		t.Fatal("the session ended")
	}
	// Carol's events are counted apart from bob's: she goes through.
	c.say(chatX, "honest")
	eventually(t, wait, "carol's event reaches alice", func() bool { return originsIn(a.journal(chatX))[c.origin] })
}

func TestQuarantineStopsNewEventsKeepsWhatWasTakenAndTheOwnerLiftsIt(t *testing.T) {
	a, b, c := newTnode(t, "alice"), newTnode(t, "bob"), newTnode(t, "carol")
	b.listen()
	for _, n := range []*tnode{a, b, c} {
		n.chat(chatX)
	}
	var said []string
	a.m.OnQuarantine = func(q store.QuarantineInfo, released bool) {
		said = append(said, q.Origin[:4]+map[bool]string{false: " in", true: " out"}[released])
	}
	befriendAt(t, c, b, "trusted", chatX)
	befriendAt(t, a, b, "trusted", chatX)
	b.say(chatX, "bob one")
	c.say(chatX, "carol one")
	eventually(t, wait, "alice holds both", func() bool { o := originsIn(a.journal(chatX)); return o[b.origin] && o[c.origin] })

	// A source that is not a neighbor: its new events do not come through bob, the old ones stay.
	if err := a.m.Quarantine(store.QuarantineInfo{Origin: c.origin, Reason: "the owner says so", By: "owner", Via: b.origin}); err != nil {
		t.Fatal(err)
	}
	c.say(chatX, "carol two")
	b.say(chatX, "bob two")
	eventually(t, wait, "bob's second", func() bool { return len(a.journal(chatX)) >= 3 })
	time.Sleep(500 * time.Millisecond)
	if got := a.journal(chatX); len(got) != 3 || !originsIn(got)[c.origin] {
		t.Fatalf("%v", got)
	}
	if a.m.Stats(b.origin).Refused["source in quarantine"] == 0 {
		t.Fatalf("not counted: %+v", a.m.Stats(b.origin))
	}

	// A neighbor in quarantine: the session is closed and not opened again.
	if err := a.m.Quarantine(store.QuarantineInfo{Origin: b.origin, Reason: "the owner says so", By: "owner"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, wait, "the session is closed", func() bool { return len(a.m.Sessions()) == 0 })
	b.say(chatX, "bob three")
	time.Sleep(700 * time.Millisecond)
	if len(a.m.Sessions()) != 0 || len(a.journal(chatX)) != 3 {
		t.Fatalf("a quarantined node got through: %d sessions, %d events", len(a.m.Sessions()), len(a.journal(chatX)))
	}

	// Lifted: the exchange comes back, and carol's later events too.
	for _, o := range []string{b.origin, c.origin} {
		if was, err := a.m.Release(o); err != nil || !was {
			t.Fatalf("release %.4s: %v %v", o, was, err)
		}
	}
	eventually(t, wait, "everything arrives", func() bool { return len(a.journal(chatX)) == len(b.journal(chatX)) && len(a.journal(chatX)) == 5 })
	if len(said) != 4 {
		t.Fatalf("the owner was told: %v", said)
	}
}
