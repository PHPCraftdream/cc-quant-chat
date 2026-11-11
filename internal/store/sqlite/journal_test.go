package sqlite

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/node"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

const chatID2 = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210" // sorts after chatID

type keys struct {
	nodePub    ed25519.PublicKey
	nodePriv   ed25519.PrivateKey
	origin     string
	authorPriv ed25519.PrivateKey
}

func newKeys(t *testing.T) keys {
	t.Helper()
	var k keys
	var err error
	if k.nodePub, k.nodePriv, err = ed25519.GenerateKey(rand.Reader); err != nil {
		t.Fatal(err)
	}
	if _, k.authorPriv, err = ed25519.GenerateKey(rand.Reader); err != nil {
		t.Fatal(err)
	}
	k.origin = node.Origin(k.nodePub)
	return k
}

// sign: the author's columns for an event as a signing client writes them.
func (k keys) sign(chat, kind, payload string, lamport int64) (pub, sig string) {
	pub = base64.StdEncoding.EncodeToString(k.authorPriv.Public().(ed25519.PublicKey))
	sig = base64.StdEncoding.EncodeToString(ed25519.Sign(k.authorPriv, proto.EventMessage(chat, k.origin, kind, []byte(payload), lamport)))
	return
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

// legacyChat writes a chat the way versions before the journal did: rows in the projection tables
// and nothing else. (It uses the current code and then forgets the events.)
func legacyChat(t *testing.T, db *DB, k keys, chat, name string) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Load(chat, name); err != nil {
		t.Fatal(err)
	}
	recs := []store.Record{
		{T: "nick", Nick: "alice", Hash: "ha"}, {T: "nick", Nick: "bob", Hash: "hb", Pub: "cHVi"},
		{T: "pin", Nick: "alice", Role: "lead"}, {T: "room", Room: "ops", Topic: "operations", Nick: "alice"},
		{T: "in", Room: "ops", Nick: "alice"}, {T: "in", Room: "ops", Nick: "bob"},
	}
	for i := int64(1); i <= 5; i++ {
		m := &proto.Message{ID: i, TS: "2026-01-01T00:00:00Z", Room: proto.RoomCommon, From: "alice", Text: fmt.Sprintf("m%d <&> \"q\"", i)}
		recs = append(recs, store.Record{T: "msg", Key: "r:common", Msg: m, ClientID: fmt.Sprintf("c%d", i)})
	}
	for _, r := range recs {
		must(db.Append(chat, r))
	}
	lam, err := db.Lamport(chat)
	must(err)
	payload := `{"room":"common","text":"signed"}`
	pub, sig := k.sign(chat, "msg", payload, lam+1)
	must(db.Append(chat, store.Record{T: "msg", Key: "r:common", Origin: k.origin, AuthorPub: pub, Sig: sig, Payload: payload,
		Msg: &proto.Message{ID: 6, TS: "2026-01-01T00:00:01Z", Room: proto.RoomCommon, From: "bob", Text: "signed"}}))
	must(db.SetSetting(chat, "board.manager_role", "lead"))

	must(db.Do(chat, func(tx store.BoardTx) error {
		ts := "2026-01-02T00:00:00Z"
		l, err := tx.NextLamport()
		if err != nil {
			return err
		}
		must(tx.PutSprint(store.Sprint{ID: 1, Name: "one", Goal: "ship", State: store.SprintActive, CreatedAt: ts, StartedAt: ts}))
		must(tx.AppendLog(store.LogEntry{ID: 1, TS: ts, Actor: "alice", Kind: "sprint.create", Entity: "sprint", EntityID: 1,
			After: map[string]json.RawMessage{"name": raw(`"one"`)}, Lamport: l}))
		for id := int64(1); id <= 2; id++ {
			l, err = tx.NextLamport()
			if err != nil {
				return err
			}
			must(tx.PutTask(store.Task{ID: id, Title: fmt.Sprintf("task %d", id), Status: "todo", Sprint: 1, CreatedBy: "bob", CreatedAt: ts, UpdatedAt: ts}))
			must(tx.AppendLog(store.LogEntry{ID: id + 1, TS: ts, Actor: "bob", Kind: "task.create", Entity: "task", EntityID: id,
				After: map[string]json.RawMessage{"title": raw(fmt.Sprintf(`"task %d"`, id))}, Lamport: l}))
		}
		l, err = tx.NextLamport()
		if err != nil {
			return err
		}
		upd := `{"id":1,"op":"task.update","title":"task one"}`
		p, s := k.sign(chat, "task.update", upd, l)
		must(tx.PutTask(store.Task{ID: 1, Title: "task one", Status: "todo", Sprint: 1, CreatedBy: "bob", CreatedAt: ts, UpdatedAt: ts}))
		must(tx.AppendLog(store.LogEntry{ID: 4, TS: ts, Actor: "alice", Kind: "task.update", Entity: "task", EntityID: 1,
			Before: map[string]json.RawMessage{"title": raw(`"task 1"`)}, After: map[string]json.RawMessage{"title": raw(`"task one"`)},
			Origin: k.origin, Lamport: l, AuthorPub: p, Sig: s, Payload: upd}))
		l, err = tx.NextLamport()
		if err != nil {
			return err
		}
		must(tx.PutComment(store.Comment{ID: 1, TaskID: 1, TS: ts, Author: "bob", Text: "on it", Origin: k.origin, Lamport: l}))
		must(tx.AppendLog(store.LogEntry{ID: 5, TS: ts, Actor: "bob", Kind: "task.comment", Entity: "task", EntityID: 1,
			After: map[string]json.RawMessage{"comment": raw("1"), "text": raw(`"on it"`), "hidden": raw("false")}, Lamport: l}))
		return tx.PutWorkflow(store.Workflow{Statuses: []string{"todo", "done"}, Initial: "todo", Terminal: []string{"done"},
			Transitions: map[string][]string{"todo": {"done"}}}, ts)
	}))

	must(db.DoDocs(chat, func(tx store.DocsTx) error {
		ts := "2026-01-03T00:00:00Z"
		put := func(path, body string, version int64, deleted bool, note string, signedAs string, lamport int64, payload string) {
			v := store.DocVersion{Path: path, Version: version, TS: ts, Author: "alice", Title: path, Body: body, Note: note, Deleted: deleted, Origin: k.origin, Lamport: lamport}
			if signedAs != "" {
				v.AuthorPub, v.Sig = k.sign(chat, signedAs, payload, lamport)
				v.Payload = payload
			}
			must(tx.AddVersion(v))
			must(tx.PutDoc(store.Doc{Path: path, Title: path, Body: body, Version: version, Author: "alice", TS: ts, Deleted: deleted}))
		}
		l, _ := tx.NextLamport()
		put("/a", "# A", 1, false, "", "", l, "")
		l, _ = tx.NextLamport()
		put("/c", "# C", 1, false, "", "", l, "")
		l, _ = tx.NextLamport()
		put("/e", "# E", 1, false, "", "doc.put", l, `{"body":"# E","op":"put","path":"/e"}`)
		l, _ = tx.NextLamport() // a signed move: two versions of one event
		mv := `{"op":"move","path":"/c","to":"/d"}`
		put("/d", "# C", 1, false, "moved from /c", "doc.move", l, mv)
		put("/c", "# C", 2, true, "moved to /d", "doc.move", l, mv)
		return nil
	}))

	for _, q := range []string{`DELETE FROM events`, `DELETE FROM meta`} { // a database from before the journal
		if _, err := db.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func legacyDB(t *testing.T) (*DB, string, keys) {
	t.Helper()
	db, path := openTemp(t)
	k := newKeys(t)
	legacyChat(t, db, k, chatID, "one")
	legacyChat(t, db, k, chatID2, "two")
	return db, path, k
}

func startNode(t *testing.T, db *DB, k keys) {
	t.Helper()
	if err := db.RegisterOrigin(k.origin, k.nodePub); err != nil {
		t.Fatal(err)
	}
	db.SetNode(k.origin, k.nodePriv)
}

func countEvents(t *testing.T, db *DB, chat string) int {
	t.Helper()
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM events WHERE chat_id = ?`, chat).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestBackfillMovesOldRowsIntoAJournalThatVerifiesAndRebuildsThem(t *testing.T) {
	db, _, k := legacyDB(t)
	if done, _ := db.BackfillDone(); done {
		t.Fatal("a database with history must not start as moved")
	}
	if _, err := db.Rebuild("", true); err == nil {
		t.Fatal("a rebuild before the old rows are in the journal would erase them")
	}
	startNode(t, db, k)
	chats, evs, err := db.BackfillEvents()
	if err != nil || chats != 2 || evs == 0 {
		t.Fatalf("backfill: %d chats, %d events, %v", chats, evs, err)
	}
	rep, err := db.Verify("")
	if err != nil || len(rep.Issues) != 0 {
		t.Fatalf("verify: %v %v", err, rep.Issues)
	}
	if rep.Signed != 8 { // per chat: a message, a task update, a page, a move (two versions, one event)
		t.Fatalf("%d author signatures verified, want 8 (the kind each was signed with must be found again)", rep.Signed)
	}
	rb, err := db.Rebuild("", true)
	if err != nil || rb.Total() != 0 {
		t.Fatalf("the journal does not give back the old rows: %+v, %v", rb.Diffs, err)
	}
	var moves int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM events WHERE chat_id = ? AND kind = 'doc.move'`, chatID).Scan(&moves); err != nil || moves != 1 {
		t.Fatalf("the two versions of a signed move are %d events, want 1 (%v)", moves, err)
	}
	// The clock never goes back along the chain.
	rows, err := db.db.Query(`SELECT lamport FROM events WHERE chat_id = ? ORDER BY seq`, chatID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var last int64
	for rows.Next() {
		var l int64
		if err := rows.Scan(&l); err != nil {
			t.Fatal(err)
		}
		if l < last {
			t.Fatalf("lamport %d follows %d", l, last)
		}
		last = l
	}
}

func TestBackfillIsIdempotent(t *testing.T) {
	db, _, k := legacyDB(t)
	startNode(t, db, k)
	if _, _, err := db.BackfillEvents(); err != nil {
		t.Fatal(err)
	}
	n1, n2 := countEvents(t, db, chatID), countEvents(t, db, chatID2)
	chats, evs, err := db.BackfillEvents()
	if err != nil || chats != 0 || evs != 0 {
		t.Fatalf("second run: %d chats, %d events, %v", chats, evs, err)
	}
	// Even with the global mark lost, a chat that has events is not moved again.
	if _, err := db.db.Exec(`DELETE FROM meta WHERE key = ?`, metaBackfill); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.BackfillEvents(); err != nil {
		t.Fatal(err)
	}
	if countEvents(t, db, chatID) != n1 || countEvents(t, db, chatID2) != n2 {
		t.Fatal("events were written twice")
	}
}

func TestBackfillSurvivesBeingInterrupted(t *testing.T) {
	ref, _, kr := legacyDB(t)
	startNode(t, ref, kr)
	if _, _, err := ref.BackfillEvents(); err != nil {
		t.Fatal(err)
	}

	db, _, k := legacyDB(t)
	startNode(t, db, k)
	powerCut := errors.New("power cut")
	db.afterBackfillChat = func(i int) error {
		if i == 0 {
			return powerCut
		}
		return nil
	}
	if _, _, err := db.BackfillEvents(); !errors.Is(err, powerCut) {
		t.Fatalf("first run: %v", err)
	}
	if done, _ := db.BackfillDone(); done || countEvents(t, db, chatID) == 0 || countEvents(t, db, chatID2) != 0 {
		t.Fatal("after the cut: the first chat is moved (whole), the second is not, and nothing says all is done")
	}
	db.afterBackfillChat = nil
	chats, _, err := db.BackfillEvents()
	if err != nil || chats != 1 {
		t.Fatalf("second run moved %d chats, %v; want just the second", chats, err)
	}
	if done, _ := db.BackfillDone(); !done {
		t.Fatal("not marked done")
	}
	for _, c := range []string{chatID, chatID2} {
		if got, want := countEvents(t, db, c), countEvents(t, ref, c); got != want {
			t.Fatalf("chat %.8s: %d events, an uninterrupted run gives %d", c, got, want)
		}
	}
	if rep, err := db.Verify(""); err != nil || len(rep.Issues) != 0 {
		t.Fatalf("verify: %v %v", err, rep.Issues)
	}
	if rb, err := db.Rebuild("", true); err != nil || rb.Total() != 0 {
		t.Fatalf("rebuild: %+v %v", rb.Diffs, err)
	}
}

func TestEventsOfRecordsChainInOrderAndKeepTheClockWhereItIs(t *testing.T) {
	db, _ := openTemp(t)
	k := newKeys(t)
	startNode(t, db, k)
	if _, err := db.Load(chatID, "x"); err != nil {
		t.Fatal(err)
	}
	m := &proto.Message{ID: 1, TS: "2026-01-01T00:00:00Z", Room: proto.RoomCommon, From: "a", Text: "hi"}
	for _, r := range []store.Record{
		{T: "nick", Nick: "a", Hash: "h"}, {T: "msg", Key: "r:common", Msg: m}, {T: "nick_key", Nick: "a", Hash: "h2"},
		{T: "seen", Nick: "a", ID: 1}, {T: "nick_key", Nick: "a"},
	} {
		if err := db.Append(chatID, r); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.db.Query(`SELECT seq, kind, lamport FROM events WHERE chat_id = ? ORDER BY seq`, chatID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := []struct {
		kind string
		lam  int64
	}{{"nick.register", 0}, {"msg", 1}, {"nick.key", 1}, {"nick.reset", 1}} // the read cursor is not an event
	i := 0
	for rows.Next() {
		var seq, lam int64
		var kind string
		if err := rows.Scan(&seq, &kind, &lam); err != nil {
			t.Fatal(err)
		}
		if i >= len(want) || seq != int64(i+1) || kind != want[i].kind || lam != want[i].lam {
			t.Fatalf("event %d: seq %d %s lamport %d", i, seq, kind, lam)
		}
		i++
	}
	if i != len(want) {
		t.Fatalf("%d events, want %d", i, len(want))
	}
	if rep, err := db.Verify(""); err != nil || len(rep.Issues) != 0 || rep.Events != len(want) {
		t.Fatalf("verify: %+v %v", rep, err)
	}
}
