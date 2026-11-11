package sqlite

import (
	"strings"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

func TestQuarantineIsListedWithForksAndOnlyTheOwnerLiftsItAndLiftingClearsTheBreachesOfToday(t *testing.T) {
	db, _ := openTemp(t)
	a, f := strings.Repeat("a", 32), strings.Repeat("f", 32)
	if fresh, err := db.Quarantine(store.QuarantineInfo{Origin: a, Reason: "too fast", By: "auto", Via: strings.Repeat("b", 32)}); err != nil || !fresh {
		t.Fatalf("%v %v", fresh, err)
	}
	if fresh, _ := db.Quarantine(store.QuarantineInfo{Origin: a, Reason: "again", By: "owner"}); fresh {
		t.Fatal("a second quarantine of the same source changed it")
	}
	if err := db.FlagOrigin(f, "fork", "{}"); err != nil { // a fork is a quarantine too
		t.Fatal(err)
	}
	list, err := db.Quarantined()
	if err != nil || len(list) != 2 {
		t.Fatalf("%+v %v", list, err)
	}
	got := map[string]store.QuarantineInfo{}
	for _, q := range list {
		got[q.Origin] = q
	}
	if q := got[a]; q.Reason != "too fast" || q.By != "auto" || q.Via == "" || q.Since == "" {
		t.Errorf("%+v", q)
	}
	if q := got[f]; !strings.HasPrefix(q.Reason, "fork") || q.By != "auto" {
		t.Errorf("%+v", q)
	}

	if err := db.AddDayStats(a, "2026-03-01", store.DayStats{Events: 3, Bytes: 100, Violations: 2}); err != nil {
		t.Fatal(err)
	}
	if err := db.AddDayStats(a, "2026-03-01", store.DayStats{Events: 1, Bytes: 50, Violations: 1}); err != nil {
		t.Fatal(err)
	}
	if s, _ := db.DayStats(a, "2026-03-01"); s != (store.DayStats{Events: 4, Bytes: 150, Violations: 3}) {
		t.Fatalf("%+v", s)
	}
	for _, o := range []string{a, f} {
		if was, err := db.ReleaseSource(o); err != nil || !was {
			t.Fatalf("release %.4s: %v %v", o, was, err)
		}
	}
	if was, _ := db.ReleaseSource(a); was {
		t.Fatal("released twice")
	}
	if list, _ := db.Quarantined(); len(list) != 0 {
		t.Fatalf("%+v", list)
	}
	if _, flagged, _ := db.Flagged(f); flagged {
		t.Fatal("the fork flag stayed")
	}
	if s, _ := db.DayStats(a, "2026-03-01"); s.Violations != 0 || s.Events != 4 {
		t.Fatalf("lifting keeps the counts, clears the breaches: %+v", s)
	}
}
