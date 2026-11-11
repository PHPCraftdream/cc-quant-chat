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

func TestAJournalOfTheFirstVersionIsBroughtToTheSecondWhenTheChatIsOpened(t *testing.T) {
	db, _ := openTemp(t)
	startNode(t, db, newKeys(t))
	if _, err := db.Load(chatID, "x"); err != nil {
		t.Fatal(err)
	}
	m := &proto.Message{ID: 7, TS: "2026-01-01T00:00:00Z", Room: proto.RoomCommon, From: "a", Text: "hi"}
	for _, r := range []store.Record{
		{T: "nick", Nick: "a", Hash: "secret-hash", Pub: "a-key"}, {T: "msg", Key: "r:common", Msg: m},
		{T: "nick_key", Nick: "a", Hash: "newer-hash", Pub: "a-key-2"},
	} {
		if err := db.Append(chatID, r); err != nil {
			t.Fatal(err)
		}
	}
	// Make the chat what the first version left: the hash and the cursor in the events, no mark of
	// the version, signatures and links that the second version does not recognise.
	c := chatRaw(t, db, chatID)
	for _, q := range []string{
		`UPDATE events SET assigned = '{"hash":"secret-hash","seen":3}' WHERE kind = 'nick.register'`,
		`UPDATE events SET assigned = '{"hash":"newer-hash"}' WHERE kind = 'nick.key'`,
		`UPDATE events SET node_sig = 'AAAA', prev = 'stale'`,
		`DELETE FROM meta WHERE key = 'journal_version'`,
	} {
		if _, err := c.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Release(chatID)

	if _, err := db.Load(chatID, "x"); err != nil { // opens the chat: the journal is brought up
		t.Fatal(err)
	}
	if rep, err := db.Verify(chatID); err != nil || len(rep.Issues) != 0 || rep.Events != 3 {
		t.Fatalf("verify after the upgrade: %+v %v", rep, err)
	}
	c = chatRaw(t, db, chatID)
	var v string
	if err := c.db.QueryRow(`SELECT value FROM meta WHERE key = 'journal_version'`).Scan(&v); err != nil || v != journalVersion {
		t.Fatalf("version %q %v", v, err)
	}
	var bad int
	if err := c.db.QueryRow(`SELECT COUNT(*) FROM events WHERE assigned LIKE '%hash%' OR assigned LIKE '%seen%'`).Scan(&bad); err != nil || bad != 0 {
		t.Fatalf("%d events still carry node state: %v", bad, err)
	}
	if hash, _ := nickRow(t, db, "a"); hash != "newer-hash" {
		t.Fatalf("the secret's hash is node state and stays in the projection: %q", hash)
	}
	// And it is done once: opening again changes nothing.
	var tail1, tail2 string
	c.db.QueryRow(`SELECT node_sig FROM events ORDER BY seq DESC LIMIT 1`).Scan(&tail1)
	db.Release(chatID)
	if _, err := db.Load(chatID, "x"); err != nil {
		t.Fatal(err)
	}
	chatRaw(t, db, chatID).db.QueryRow(`SELECT node_sig FROM events ORDER BY seq DESC LIMIT 1`).Scan(&tail2)
	if tail1 == "" || tail1 != tail2 {
		t.Fatalf("a second opening re-signed the journal: %q %q", tail1, tail2)
	}
}
