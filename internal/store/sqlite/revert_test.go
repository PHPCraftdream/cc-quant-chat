package sqlite

import (
	"testing"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/events"
)

func TestTheNamesAnEventMentionsAreFoundWhereverTheyAreInWhatTheNodeAssigned(t *testing.T) {
	got := refsIn(`{"rows":[],"refs":{"task":{"4":{"o":"aa","s":7}},"comment":{"2":{"o":"bb","s":9}}},"dparents":{"p":[{"o":"cc","s":1},{"o":"dd","s":2}]},"reply_ref":{"o":"ee","s":3},"x":{"o":"no"}}`)
	seen := map[string]bool{}
	for _, r := range got {
		seen[key(r.Origin, r.Seq)] = true
	}
	for _, want := range []string{"aa#7", "bb#9", "cc#1", "dd#2", "ee#3"} {
		if !seen[want] {
			t.Errorf("%s was not found in %v", want, got)
		}
	}
	if len(got) != 5 {
		t.Errorf("%v", got)
	}
	if refsIn("not json") != nil || refsIn("") != nil {
		t.Error("names found where there is no JSON")
	}
}

func TestARevertFromATimeLeavesOutTheEventsOfThatNodeFromThenAndWhatNamesThem(t *testing.T) {
	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	rules := []revertRule{{id: "r1", origin: "bob", since: at}, {id: "r2", origin: "carol", undone: true}}
	sk := newSkipper(rules, "me")
	ev := func(origin string, seq int64, ts time.Time, assigned string) events.Event {
		return events.Event{Origin: origin, Seq: seq, TS: ts.Format(time.RFC3339), Kind: "task.create", Assigned: assigned}
	}
	if sk.skip(ev("bob", 1, at.Add(-time.Second), "{}")) {
		t.Error("an event before the time was left out")
	}
	if !sk.skip(ev("bob", 2, at, "{}")) {
		t.Error("an event at the time was kept")
	}
	if sk.skip(ev("carol", 1, at, "{}")) {
		t.Error("an undone revert still applies")
	}
	// An event of this node that names what bob made at seq 2 is left out, and counted as its own.
	if !sk.skip(ev("me", 5, at, `{"refs":{"task":{"1":{"o":"bob","s":2}}}}`)) {
		t.Error("a dependent event was kept")
	}
	// ... and what names that one: the dependency goes on.
	if !sk.skip(ev("dave", 1, at, `{"refs":{"comment":{"1":{"o":"me","s":5}}}}`)) {
		t.Error("an event that names a dependent event was kept")
	}
	// A message that replies to a left-out one stays.
	if sk.skip(events.Event{Origin: "me", Seq: 6, TS: at.Format(time.RFC3339), Kind: "msg", Assigned: `{"reply_ref":{"o":"bob","s":2}}`}) {
		t.Error("a reply was left out")
	}
	if c := sk.count["bob"]; c == nil || c.Skipped != 3 || c.SkippedOwn != 1 {
		t.Errorf("%+v", c)
	}
}
