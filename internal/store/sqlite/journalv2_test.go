package sqlite

import (
	"strings"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

func nickRow(t *testing.T, db *DB, nick string) (hash string, seen int64) {
	t.Helper()
	if err := chatRaw(t, db, chatID).db.QueryRow(`SELECT key_hash, seen FROM nicks WHERE nick = ?`, nick).Scan(&hash, &seen); err != nil {
		t.Fatal(err)
	}
	return
}

func TestWhoANickIsIsInTheJournalAndTheHashOfItsSecretIsNot(t *testing.T) {
	db, _ := openTemp(t)
	startNode(t, db, newKeys(t))
	if _, err := db.Load(chatID, "x"); err != nil {
		t.Fatal(err)
	}
	m := &proto.Message{ID: 7, TS: "2026-01-01T00:00:00Z", Room: proto.RoomCommon, From: "a", Text: "hi"}
	for _, r := range []store.Record{
		{T: "nick", Nick: "a", Hash: "secret-hash", Pub: "a-key", ID: 5}, {T: "msg", Key: "r:common", Msg: m},
		{T: "nick_key", Nick: "a", Hash: "other-secret-hash", Pub: "a-key-2"},
	} {
		if err := db.Append(chatID, r); err != nil {
			t.Fatal(err)
		}
	}
	// The projection keeps this node's own state ...
	if hash, seen := nickRow(t, db, "a"); hash != "other-secret-hash" || seen != 5 {
		t.Fatalf("node state: %q, seen %d", hash, seen)
	}
	// ... and the journal does not.
	rows, err := chatRaw(t, db, chatID).db.Query(`SELECT kind, payload, assigned FROM events WHERE kind LIKE 'nick.%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var kind, payload, assigned string
		if err := rows.Scan(&kind, &payload, &assigned); err != nil {
			t.Fatal(err)
		}
		for _, s := range []string{payload, assigned} {
			if strings.Contains(s, "hash") || strings.Contains(s, "seen") {
				t.Errorf("%s leaks node state: %s %s", kind, payload, assigned)
			}
		}
		n++
	}
	if n != 2 {
		t.Fatalf("%d nick events", n)
	}
	if rep, err := db.Verify(chatID); err != nil || len(rep.Issues) != 0 {
		t.Fatalf("verify: %+v %v", rep, err)
	}
	// A rebuild gives the same projections and does not lose the node's own state.
	if _, err := db.Rebuild(chatID, false); err != nil {
		t.Fatal(err)
	}
	if hash, seen := nickRow(t, db, "a"); hash != "other-secret-hash" || seen != 5 {
		t.Fatalf("after a rebuild: %q, seen %d", hash, seen)
	}
}
