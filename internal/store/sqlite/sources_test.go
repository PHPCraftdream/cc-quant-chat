package sqlite

import (
	"strings"
	"testing"
)

func TestTheSourcesOfANeighborAreCountedAndLimitedPerNeighborAndForgottenWithIt(t *testing.T) {
	db, _ := openTemp(t)
	via, other := strings.Repeat("a", 32), strings.Repeat("b", 32)
	src := func(c byte) string { return strings.Repeat(string(c), 32) }
	for i, c := range []byte("123") {
		ok, fresh, err := db.NoteSource(src(c), via, 3)
		if err != nil || !ok || !fresh {
			t.Fatalf("%d: %v %v %v", i, ok, fresh, err)
		}
	}
	if ok, fresh, _ := db.NoteSource(src('1'), via, 3); !ok || fresh {
		t.Fatalf("a source seen before: %v %v", ok, fresh)
	}
	if ok, _, _ := db.NoteSource(src('4'), via, 3); ok {
		t.Fatal("a fourth source through one neighbor")
	}
	if ok, fresh, _ := db.NoteSource(src('4'), other, 3); !ok || !fresh {
		t.Fatal("the limit is per neighbor")
	}
	if got, _ := db.SourcesVia(via); len(got) != 3 {
		t.Fatalf("%v", got)
	}
	if err := db.AddPeer(via, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.RemovePeer(via); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.SourcesVia(via); len(got) != 0 {
		t.Fatalf("a removed node's sources stayed: %v", got)
	}
}
