package sqlite

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
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
	if err := db.node.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
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
	if recs, err := db.Load(chatID, "vpn-dev"); err != nil || len(recs) != 2 || recs[0].T != "room" || recs[0].Room != proto.RoomCommon || recs[1].T != "maxid" || recs[1].ID != 0 {
		t.Fatalf("fresh chat: recs %+v, err %v; want the common room and maxid 0", recs, err)
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
	order := map[string]int{"room": 1, "nick": 2, "in": 3, "msg": 4, "maxid": 5}
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

func TestNickKeyIsNeverOverwritten(t *testing.T) {
	db, _ := openTemp(t)
	if _, err := db.Load(chatID, "x"); err != nil { // registers the chat
		t.Fatal(err)
	}
	mustAppend(t, db, store.Record{T: "nick", Nick: "alice", Hash: "first", ID: 0})
	if err := db.Append(chatID, store.Record{T: "nick", Nick: "alice", Hash: "second", ID: 0}); err == nil {
		t.Fatal("registering an existing nick again must fail, not replace its key")
	}
	if err := db.Append(chatID, store.Record{T: "nick_key", Nick: "alice", Hash: ""}); err != nil { // explicit reset
		t.Fatal(err)
	}
	mustAppend(t, db, store.Record{T: "nick_key", Nick: "alice", Hash: "third"})
	recs, _ := db.Load(chatID, "x")
	for _, r := range recs {
		if r.T == "nick" && r.Hash != "third" {
			t.Fatalf("nick hash = %q, want third", r.Hash)
		}
	}
}

func TestSeenOnlyMovesForward(t *testing.T) {
	db, _ := openTemp(t)
	if _, err := db.Load(chatID, "x"); err != nil { // registers the chat
		t.Fatal(err)
	}
	mustAppend(t, db,
		store.Record{T: "nick", Nick: "alice", Hash: "h", ID: 0},
		store.Record{T: "seen", Nick: "alice", ID: 7},
		store.Record{T: "seen", Nick: "alice", ID: 3},
	)
	recs, _ := db.Load(chatID, "x")
	for _, r := range recs {
		if r.T == "nick" && r.ID != 7 {
			t.Fatalf("seen = %d, want 7", r.ID)
		}
	}
}

func TestLoadKeepsOnlyTheNewestPerThreadButAllIDs(t *testing.T) {
	db, _ := openTemp(t)
	if _, err := db.Load(chatID, "x"); err != nil {
		t.Fatal(err)
	}
	total := store.ThreadWindow + 5
	c, err := db.chat(chatID, "", false)
	if err != nil || c == nil {
		t.Fatal(c, err)
	}
	tx, err := c.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= total; i++ {
		if _, err := tx.Exec(`INSERT INTO messages (id, thread, room, from_nick, text, ts) VALUES (?, 'r:common', 'common', 'a', 't', 'ts')`, i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO messages (id, thread, room, from_nick, text, ts) VALUES (?, 'd:a|b', 'direct', 'a', 't', 'ts')`, total+1); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	recs, err := db.Load(chatID, "x")
	if err != nil {
		t.Fatal(err)
	}
	var common, dm int
	var firstCommon, maxID int64
	for _, r := range recs {
		switch {
		case r.T == "msg" && r.Key == "r:common":
			if common == 0 {
				firstCommon = r.Msg.ID
			}
			common++
		case r.T == "msg":
			dm++
		case r.T == "maxid":
			maxID = r.ID
		}
	}
	if common != store.ThreadWindow || firstCommon != 6 || dm != 1 {
		t.Errorf("common = %d (first id %d), dm = %d; want %d newest of the room (from id 6) and the dialog", common, firstCommon, dm, store.ThreadWindow)
	}
	if maxID != int64(total+1) {
		t.Errorf("maxid = %d, want %d", maxID, total+1)
	}
}

func TestDatabaseFilesArePrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "ccqc.db") // a directory the store makes itself
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Load(chatID, "x"); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	dir := filepath.Dir(path)
	for _, p := range []string{filepath.Join(dir, "node.db"), filepath.Join(dir, "chats", chatID+".db")} {
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s: mode %v, err %v; want no access for group/others", p, fi.Mode(), err)
		}
	}
	for _, p := range []string{dir, filepath.Join(dir, "chats")} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s: mode %v, err %v; want a private directory", p, fi.Mode(), err)
		}
	}
}

func TestPathWithURICharacters(t *testing.T) {
	name := "a b#c%d"
	if runtime.GOOS != "windows" {
		name += "?e" // not allowed in Windows file names
	}
	dir := filepath.Join(t.TempDir(), name)
	db, err := Open(filepath.Join(dir, "ccqc.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := os.Stat(filepath.Join(dir, "node.db")); err != nil {
		t.Fatalf("database not in the requested directory: %v", err)
	}
}

// A write that is still on its way when the store is closed (a session of a peer, a goroutine of a test) must not open
// the chat file again: it would bring a -wal and a -shm back into a directory that is being removed.
func TestAClosedStoreOpensNoChatFileAndMakesNoNewOne(t *testing.T) {
	db, path := openTemp(t)
	if _, err := db.Load(chatID, "one"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(filepath.Dir(path), "chats")
	names := func() string {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range entries {
			out = append(out, e.Name())
		}
		return strings.Join(out, ",")
	}
	before := names()
	m := &proto.Message{ID: 1, TS: "2026-01-01T00:00:00Z", Room: proto.RoomCommon, From: "a", Text: "late"}
	if err := db.Append(chatID, store.Record{T: "msg", Key: "r:common", Msg: m}); err == nil {
		t.Fatal("a write into the chat of a closed store was taken")
	}
	if _, err := db.Load(chatID2, "two"); err == nil {
		t.Fatal("a chat was made in a closed store")
	}
	if after := names(); after != before {
		t.Fatalf("the chat directory changed after the store was closed: %q, was %q", after, before)
	}
}
