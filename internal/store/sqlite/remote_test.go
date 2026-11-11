package sqlite

import (
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/events"
)

// fakeEvent is the next event of a chain of an origin that is not a real node: the store does not
// check signatures (the exchange does), only chains and the rules of application.
func fakeEvent(origin string, seq int64, prev, kind, payload, assigned string) events.Event {
	return events.Event{ChatID: chatID, Origin: origin, Seq: seq, Prev: prev, Lamport: seq, Kind: kind, Payload: payload,
		Assigned: assigned, TS: "2026-01-01T00:00:00Z"}
}

func accept(t *testing.T, db *DB, e events.Event) error {
	t.Helper()
	_, err := db.AcceptEvent(chatID, e)
	return err
}

func TestTwoOriginsWithOneShortPrefixAreTwoDifferentNodesInTheChat(t *testing.T) {
	db, _ := openTemp(t)
	startNode(t, db, newKeys(t))
	if _, err := db.Load(chatID, "x"); err != nil {
		t.Fatal(err)
	}
	a := "1e44" + strings.Repeat("0", 28)
	b := "1e44" + strings.Repeat("1", 28)
	for i, o := range []string{a, b} {
		pub := []string{"key-of-a", "key-of-b"}[i]
		if err := accept(t, db, fakeEvent(o, 1, "", "nick.register", `{"nick":"bob","pub":"`+pub+`"}`, "{}")); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := chatRaw(t, db, chatID).db.Query(`SELECT nick, pubkey FROM nicks WHERE nick LIKE 'bob@%' ORDER BY nick`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var n, p string
		if err := rows.Scan(&n, &p); err != nil {
			t.Fatal(err)
		}
		got[n] = p
	}
	if len(got) != 2 || got["bob@1e44"] != "key-of-a" || got["bob@"+b[:8]] != "key-of-b" {
		t.Fatalf("two nodes with one prefix became %v", got)
	}
	// What the second origin said about itself again keeps its name.
	if err := accept(t, db, fakeEvent(b, 2, mustID(t, db, b, 1), "nick.key", `{"nick":"bob","pub":"new-key-of-b"}`, "{}")); err != nil {
		t.Fatal(err)
	}
	var p string
	if err := chatRaw(t, db, chatID).db.QueryRow(`SELECT pubkey FROM nicks WHERE nick = ?`, "bob@"+b[:8]).Scan(&p); err != nil || p != "new-key-of-b" {
		t.Fatalf("%q %v", p, err)
	}
}

func mustID(t *testing.T, db *DB, origin string, seq int64) string {
	t.Helper()
	evs, err := db.EventsAfter(chatID, origin, seq-1, 1)
	if err != nil || len(evs) != 1 {
		t.Fatalf("%v %v", evs, err)
	}
	return evs[0].ID()
}

func TestAMessageNeedsItsRoomAndAMemberOfIt(t *testing.T) {
	db, _ := openTemp(t)
	startNode(t, db, newKeys(t))
	if _, err := db.Load(chatID, "x"); err != nil {
		t.Fatal(err)
	}
	o := "ab12" + strings.Repeat("0", 28)
	msg := func(seq int64, prev, room string) events.Event {
		return fakeEvent(o, seq, prev, "msg", `{"room":"`+room+`","text":"hi"}`, `{"from":"carol","id":1,"thread":"r:`+room+`"}`)
	}
	reg := fakeEvent(o, 1, "", "nick.register", `{"nick":"carol","pub":""}`, "{}")
	if err := accept(t, db, reg); err != nil {
		t.Fatal(err)
	}
	// A room that is not here, and a room the registered nick has not joined.
	if err := accept(t, db, msg(2, reg.ID(), "ops")); err == nil || !strings.Contains(err.Error(), "no room") {
		t.Fatalf("a message to a room that is not here: %v", err)
	}
	create := fakeEvent(o, 2, reg.ID(), "room.create", `{"room":"ops","topic":"","created_by":"carol"}`, "{}")
	if err := accept(t, db, create); err != nil {
		t.Fatal(err)
	}
	if err := accept(t, db, msg(3, create.ID(), "ops")); err == nil || !strings.Contains(err.Error(), "not in @ops") {
		t.Fatalf("a message of one who is not in the room: %v", err)
	}
	join := fakeEvent(o, 3, create.ID(), "room.join", `{"room":"ops","nick":"carol"}`, "{}")
	if err := accept(t, db, join); err != nil {
		t.Fatal(err)
	}
	if err := accept(t, db, msg(4, join.ID(), "ops")); err != nil {
		t.Fatalf("a message of a member: %v", err)
	}
}

func TestReleaseDoesNotWaitForAReaderOfTheChat(t *testing.T) {
	db, _ := openTemp(t)
	if _, err := db.Load(chatID, "x"); err != nil {
		t.Fatal(err)
	}
	c, done, err := db.acquire(chatID, "", false) // a session reading the journal
	if err != nil || c == nil {
		t.Fatalf("%v %v", c, err)
	}
	start := time.Now()
	db.Release(chatID) // the broker unloads the chat, holding its locks
	if time.Since(start) > time.Second {
		t.Fatal("Release waited for the reader")
	}
	if _, err := db.Load(chatID2, "y"); err != nil { // another chat goes on at once
		t.Fatal(err)
	}
	if err := c.db.Ping(); err != nil {
		t.Fatalf("the reader's database was closed under it: %v", err)
	}
	done()
	for i := 0; ; i++ {
		c.life.RLock()
		closed := c.closed
		c.life.RUnlock()
		if closed {
			break
		}
		if i > 200 {
			t.Fatal("the database was never closed after the reader was done")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := db.Load(chatID, ""); err != nil { // and the chat opens again
		t.Fatal(err)
	}
}
