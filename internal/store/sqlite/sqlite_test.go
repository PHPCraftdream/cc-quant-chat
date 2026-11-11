package sqlite

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

const chatID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func openTemp(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ccqc.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

func TestOpenUsesWALAndIsRepeatable(t *testing.T) {
	db, path := openTemp(t)
	var mode string
	if err := db.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, err %v; want wal", mode, err)
	}
	db.Close()
	again, err := Open(path) // migrations already applied: must open cleanly
	if err != nil {
		t.Fatal(err)
	}
	again.Close()
}

func mustAppend(t *testing.T, db *DB, recs ...store.Record) {
	t.Helper()
	for _, r := range recs {
		if err := db.Append(chatID, r); err != nil {
			t.Fatalf("append %s: %v", r.T, err)
		}
	}
}

func TestRecordsSurviveReopen(t *testing.T) {
	db, path := openTemp(t)
	if recs, err := db.Load(chatID, "vpn-dev"); err != nil || len(recs) != 1 || recs[0].T != "room" || recs[0].Room != proto.RoomCommon {
		t.Fatalf("fresh chat: recs %+v, err %v; want only the common room", recs, err)
	}
	roomMsg := proto.Message{ID: 1, TS: "2026-01-01T00:00:00Z", Room: "api", From: "alice", FromRole: "lead", To: "bob", Text: "hi", ReplyTo: 0}
	dm := proto.Message{ID: 2, TS: "2026-01-01T00:00:01Z", Room: proto.RoomDirect, From: "bob", To: "alice", Text: "secret", ReplyTo: 1}
	mustAppend(t, db,
		store.Record{T: "nick", Nick: "alice", Hash: "ha", ID: 0},
		store.Record{T: "nick", Nick: "bob", Hash: "hb", ID: 0},
		store.Record{T: "in", Room: proto.RoomCommon, Nick: "alice"},
		store.Record{T: "in", Room: proto.RoomCommon, Nick: "bob"},
		store.Record{T: "room", Room: "api", Topic: "API", Nick: "alice"},
		store.Record{T: "in", Room: "api", Nick: "alice"},
		store.Record{T: "in", Room: "api", Nick: "bob"},
		store.Record{T: "msg", Key: "r:api", Msg: &roomMsg},
		store.Record{T: "msg", Key: "d:alice|bob", Msg: &dm},
		store.Record{T: "seen", Nick: "bob", ID: 2},
		store.Record{T: "out", Room: "api", Nick: "bob"},
	)
	db.Close()

	db2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	recs, err := db2.Load(chatID, "vpn-dev")
	if err != nil {
		t.Fatal(err)
	}

	rooms := map[string]store.Record{}
	nicks := map[string]store.Record{}
	members := map[string]bool{}
	var msgs []store.Record
	lastKind := 0 // replay order: room < nick < in < msg
	order := map[string]int{"room": 1, "nick": 2, "in": 3, "msg": 4}
	for _, r := range recs {
		if order[r.T] < lastKind {
			t.Fatalf("record %s out of replay order", r.T)
		}
		lastKind = order[r.T]
		switch r.T {
		case "room":
			rooms[r.Room] = r
		case "nick":
			nicks[r.Nick] = r
		case "in":
			members[r.Room+"/"+r.Nick] = true
		case "msg":
			msgs = append(msgs, r)
		}
	}
	if rooms["api"].Topic != "API" || rooms["api"].Nick != "alice" || len(rooms) != 2 {
		t.Errorf("rooms = %+v", rooms)
	}
	if nicks["alice"].Hash != "ha" || nicks["bob"].ID != 2 {
		t.Errorf("nicks = %+v; want bob seen = 2", nicks)
	}
	want := map[string]bool{"common/alice": true, "common/bob": true, "api/alice": true}
	if !reflect.DeepEqual(members, want) {
		t.Errorf("members = %v, want %v (bob left api)", members, want)
	}
	if len(msgs) != 2 || msgs[0].Key != "r:api" || msgs[1].Key != "d:alice|bob" {
		t.Fatalf("msgs = %+v", msgs)
	}
	if !reflect.DeepEqual(*msgs[0].Msg, roomMsg) || !reflect.DeepEqual(*msgs[1].Msg, dm) {
		t.Errorf("messages changed on the way: %+v / %+v", *msgs[0].Msg, *msgs[1].Msg)
	}
}

func TestChatsAreIsolated(t *testing.T) {
	db, _ := openTemp(t)
	other := "f" + chatID[1:]
	if _, err := db.Load(chatID, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Load(other, "b"); err != nil {
		t.Fatal(err)
	}
	msg := proto.Message{ID: 1, TS: "t", Room: proto.RoomCommon, From: "x", Text: "only in a"}
	mustAppend(t, db, store.Record{T: "msg", Key: "r:common", Msg: &msg})
	recs, err := db.Load(other, "b")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.T == "msg" {
			t.Fatalf("chat b sees a message of chat a: %+v", r.Msg)
		}
	}
}
