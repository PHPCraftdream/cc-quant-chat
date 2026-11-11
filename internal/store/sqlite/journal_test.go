package sqlite

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/migrate"
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

// legacyFile writes a single database of earlier versions: its schema and rows for two chats, the
// owner key, the origin, an administrator log entry and a broker-wide setting.
func legacyFile(t *testing.T, path string, k keys) {
	t.Helper()
	old, err := OpenRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	ms, err := LegacyMigrations()
	if err == nil {
		_, err = migrate.Up(context.Background(), old, ms)
	}
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := old.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	const ts = "2026-01-02T00:00:00Z"
	exec(`INSERT INTO owner (id, pubkey, created_at) VALUES (1, ?, ?)`, []byte("0123456789abcdef0123456789abcdef"), ts)
	exec(`INSERT INTO origins (origin, pubkey, created_at) VALUES (?, ?, ?)`, k.origin, []byte(k.nodePub), ts)
	exec(`INSERT INTO settings (scope, key, value) VALUES ('', 'broker.idle', '10m')`)
	exec(`INSERT INTO admin_log (ts, op, args, result, origin) VALUES (?, 'role_give', '{}', 'ok', ?)`, ts, k.origin)
	for _, c := range [][2]string{{chatID, "one"}, {chatID2, "two"}} {
		chat, name := c[0], c[1]
		exec(`INSERT INTO chats (id, name, created_at, lamport) VALUES (?, ?, ?, 15)`, chat, name, ts)
		exec(`INSERT INTO rooms (chat_id, room, topic, created_by) VALUES (?, 'common', '', ''), (?, 'ops', 'operations', 'alice')`, chat, chat)
		exec(`INSERT INTO room_members (chat_id, room, nick) VALUES (?, 'common', 'alice'), (?, 'common', 'bob'), (?, 'ops', 'alice'), (?, 'ops', 'bob')`, chat, chat, chat, chat)
		exec(`INSERT INTO nicks (chat_id, nick, key_hash, seen, pinned_role, pubkey) VALUES (?, 'alice', 'ha', 3, 'lead', ''), (?, 'bob', 'hb', 0, '', 'cHVi')`, chat, chat)
		for i := 1; i <= 5; i++ {
			exec(`INSERT INTO messages (chat_id, id, thread, room, from_nick, text, ts, client_msg_id, lamport) VALUES (?, ?, 'r:common', 'common', 'alice', ?, ?, ?, ?)`,
				chat, i, fmt.Sprintf("m%d <&> \"q\"", i), ts, fmt.Sprintf("c%d", i), i)
		}
		payload := `{"room":"common","text":"signed"}`
		pub, sig := k.sign(chat, "msg", payload, 6)
		exec(`INSERT INTO messages (chat_id, id, thread, room, from_nick, text, ts, origin, lamport, author_pub, sig, payload) VALUES (?, 6, 'r:common', 'common', 'bob', 'signed', ?, ?, 6, ?, ?, ?)`,
			chat, ts, k.origin, pub, sig, payload)

		exec(`INSERT INTO board_sprints (chat_id, id, name, goal, state, created_at, started_at) VALUES (?, 1, 'one', 'ship', 'active', ?, ?)`, chat, ts, ts)
		exec(`INSERT INTO board_tasks (chat_id, id, title, status, sprint, created_by, created_at, updated_at) VALUES (?, 1, 'task one', 'todo', 1, 'bob', ?, ?), (?, 2, 'task 2', 'todo', 1, 'bob', ?, ?)`, chat, ts, ts, chat, ts, ts)
		exec(`INSERT INTO board_workflow (chat_id, json, updated_at) VALUES (?, '{"statuses":["todo","done"],"initial":"todo","terminal":["done"],"transitions":{"todo":["done"]}}', ?)`, chat, ts)
		upd := `{"id":1,"op":"task.update","title":"task one"}`
		upub, usig := k.sign(chat, "task.update", upd, 10)
		exec(`INSERT INTO board_log (chat_id, id, ts, actor, kind, entity, entity_id, after_json, lamport) VALUES
			(?, 1, ?, 'alice', 'sprint.create', 'sprint', 1, '{"name":"one"}', 7),
			(?, 2, ?, 'bob', 'task.create', 'task', 1, '{"title":"task 1"}', 8),
			(?, 3, ?, 'bob', 'task.create', 'task', 2, '{"title":"task 2"}', 9),
			(?, 5, ?, 'bob', 'task.comment', 'task', 1, '{"comment":1,"text":"on it","hidden":false}', 11)`, chat, ts, chat, ts, chat, ts, chat, ts)
		exec(`INSERT INTO board_log (chat_id, id, ts, actor, kind, entity, entity_id, before_json, after_json, origin, lamport, author_pub, sig, payload) VALUES
			(?, 4, ?, 'alice', 'task.update', 'task', 1, '{"title":"task 1"}', '{"title":"task one"}', ?, 10, ?, ?, ?)`, chat, ts, k.origin, upub, usig, upd)
		exec(`INSERT INTO board_comments (chat_id, id, task_id, ts, author, text, origin, lamport) VALUES (?, 1, 1, ?, 'bob', 'on it', '', 11)`, chat, ts)

		put := func(path string, version int64, deleted int, note, signedAs, payload string, lamport int64, body string) {
			pub, sig := "", ""
			if signedAs != "" {
				pub, sig = k.sign(chat, signedAs, payload, lamport)
			} else {
				payload = ""
			}
			exec(`INSERT INTO doc_versions (chat_id, path, version, ts, author, title, body, note, deleted, origin, lamport, author_pub, sig, payload) VALUES (?, ?, ?, ?, 'alice', ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				chat, path, version, ts, path, body, note, deleted, map[bool]string{true: k.origin, false: ""}[signedAs != ""], lamport, pub, sig, payload)
			exec(`INSERT OR REPLACE INTO docs (chat_id, path, title, body, version, author, ts, deleted) VALUES (?, ?, ?, ?, ?, 'alice', ?, ?)`, chat, path, path, body, version, ts, deleted)
		}
		put("/a", 1, 0, "", "", "", 12, "# A")
		put("/c", 1, 0, "", "", "", 13, "# C")
		put("/e", 1, 0, "", "doc.put", `{"body":"# E","op":"put","path":"/e"}`, 14, "# E")
		mv := `{"op":"move","path":"/c","to":"/d"}` // a signed move: two versions of one event
		put("/d", 1, 0, "moved from /c", "doc.move", mv, 15, "# C")
		put("/c", 2, 1, "moved to /d", "doc.move", mv, 15, "# C")
		exec(`INSERT INTO settings (scope, key, value) VALUES (?, 'board.manager_role', 'lead')`, chat)
	}
}

// legacyDB: a data directory with a single database of earlier versions that has not been laid
// out, and a store opened on it with the node key set.
func legacyDB(t *testing.T) (*DB, string, keys) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ccqc.db")
	k := newKeys(t)
	legacyFile(t, path, k)
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	startNode(t, db, k)
	return db, path, k
}

func startNode(t *testing.T, db *DB, k keys) {
	t.Helper()
	if err := db.RegisterOrigin(k.origin, k.nodePub); err != nil {
		t.Fatal(err)
	}
	db.SetNode(k.origin, k.nodePriv)
}

func chatRaw(t *testing.T, db *DB, id string) *chatDB {
	t.Helper()
	c, err := db.chat(id, "", false)
	if err != nil || c == nil {
		t.Fatalf("chat %.8s: %v %v", id, c, err)
	}
	return c
}

func countEvents(t *testing.T, db *DB, id string) int {
	t.Helper()
	var n int
	if err := chatRaw(t, db, id).db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestLayoutMakesChatFilesWithAVerifiedJournalThatRebuildsThem(t *testing.T) {
	db, path, _ := legacyDB(t)
	if _, err := db.Load(chatID, "one"); err == nil {
		t.Fatal("a chat must not be opened while the old database is not laid out: a new file would hide its rows")
	}
	chats, evs, err := db.BackfillEvents()
	if err != nil || chats != 2 || evs == 0 {
		t.Fatalf("layout: %d chats, %d events, %v", chats, evs, err)
	}
	dir := filepath.Dir(path)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the old database must have been renamed")
	}
	if _, err := os.Stat(path + ".migrated"); err != nil {
		t.Fatalf("the old database is not kept as a reserve: %v", err)
	}
	for _, id := range []string{chatID, chatID2} {
		if _, err := os.Stat(filepath.Join(dir, "chats", id+".db")); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, "chats", id+".db-wal")); err == nil {
			t.Fatalf("%.8s: a -wal file was left beside the renamed chat file", id)
		}
		rep, err := db.Verify(id)
		if err != nil || len(rep.Issues) != 0 {
			t.Fatalf("verify: %v %v", err, rep.Issues)
		}
		if rep.Signed != 4 { // a message, a task update, a page, a move (two versions, one event)
			t.Fatalf("%d author signatures verified, want 4 (the kind each was signed with must be found again)", rep.Signed)
		}
		rb, err := db.Rebuild(id, true)
		if err != nil || rb.Total() != 0 {
			t.Fatalf("the journal does not give back the old rows: %+v, %v", rb.Diffs, err)
		}
		var moves, origins int
		c := chatRaw(t, db, id)
		if err := c.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind = 'doc.move'`).Scan(&moves); err != nil || moves != 1 {
			t.Fatalf("the two versions of a signed move are %d events, want 1 (%v)", moves, err)
		}
		if err := c.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE origin = ''`).Scan(&origins); err != nil || origins != 0 {
			t.Fatalf("%d messages without an origin: rows that predate the node get its origin (%v)", origins, err)
		}
	}
	// What belongs to the node moved into node.db.
	if pub, _, found, err := db.Owner(); err != nil || !found || len(pub) != 32 {
		t.Fatalf("owner: %x %v %v", pub, found, err)
	}
	if all, err := db.Settings(); err != nil || all[""]["broker.idle"] != "10m" {
		t.Fatalf("broker-wide settings: %v %v", all, err)
	}
	if cs, err := db.ChatSettings(chatID); err != nil || cs["board.manager_role"] != "lead" {
		t.Fatalf("chat settings: %v %v", cs, err)
	}
	var logged int
	if err := db.node.QueryRow(`SELECT COUNT(*) FROM admin_log`).Scan(&logged); err != nil || logged != 1 {
		t.Fatalf("administrator log: %d %v", logged, err)
	}
	ids, err := db.ChatIDs()
	if err != nil || len(ids) != 2 {
		t.Fatalf("the index of chats: %v %v", ids, err)
	}
	// The cursor and the nick keys came across, and the messages load as they were.
	recs, err := db.Load(chatID, "one")
	if err != nil {
		t.Fatal(err)
	}
	msgs, seen := 0, int64(-1)
	for _, r := range recs {
		if r.T == "msg" {
			msgs++
		}
		if r.T == "nick" && r.Nick == "alice" {
			seen = r.ID
		}
	}
	if msgs != 6 || seen != 3 {
		t.Fatalf("after the layout: %d messages, alice's cursor %d; want 6 and 3", msgs, seen)
	}
}

func TestLayoutIsIdempotent(t *testing.T) {
	db, path, _ := legacyDB(t)
	if _, _, err := db.BackfillEvents(); err != nil {
		t.Fatal(err)
	}
	n1, n2 := countEvents(t, db, chatID), countEvents(t, db, chatID2)
	chats, evs, err := db.BackfillEvents()
	if err != nil || chats != 0 || evs != 0 {
		t.Fatalf("second run: %d chats, %d events, %v", chats, evs, err)
	}
	// Even with every mark lost, nothing is written twice: the files are there and complete.
	if _, err := db.node.Exec(`DELETE FROM meta`); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".migrated", path); err != nil { // and the old database is back
		t.Fatal(err)
	}
	db.Release(chatID)
	db.Release(chatID2)
	chats, evs, err = db.BackfillEvents()
	if err != nil || evs != 0 {
		t.Fatalf("with the marks lost: %d chats, %d events, %v", chats, evs, err)
	}
	if countEvents(t, db, chatID) != n1 || countEvents(t, db, chatID2) != n2 {
		t.Fatal("events were written twice")
	}
}

func TestLayoutSurvivesBeingInterrupted(t *testing.T) {
	ref, _, _ := legacyDB(t)
	if _, _, err := ref.BackfillEvents(); err != nil {
		t.Fatal(err)
	}

	db, path, _ := legacyDB(t)
	dir := filepath.Dir(path)
	// A leftover of an earlier cut: a half-written file for the second chat.
	junk := filepath.Join(dir, "chats", chatID2+".db.tmp")
	if err := os.WriteFile(junk, []byte("half a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	powerCut := errors.New("power cut")
	db.afterLayoutChat = func(i int) error {
		if i == 0 {
			return powerCut
		}
		return nil
	}
	if _, _, err := db.BackfillEvents(); !errors.Is(err, powerCut) {
		t.Fatalf("first run: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("the old database must stay until every chat is laid out")
	}
	if done, _ := db.metaDone(metaLayout); done {
		t.Fatal("nothing may say the layout is complete")
	}
	if _, err := os.Stat(filepath.Join(dir, "chats", chatID+".db")); err != nil {
		t.Fatal("the first chat is whole and in place")
	}
	if _, err := os.Stat(filepath.Join(dir, "chats", chatID2+".db")); err == nil {
		t.Fatal("the second chat must not be in place yet")
	}
	// The cut that comes between the rename and the mark: the file is there, the node does not know.
	if _, err := db.node.Exec(`DELETE FROM meta WHERE key = ?`, metaLayoutChat(chatID)); err != nil {
		t.Fatal(err)
	}
	db.afterLayoutChat = nil
	chats, _, err := db.BackfillEvents()
	if err != nil || chats != 2 {
		t.Fatalf("second run: %d chats, %v; want both (the first one is only marked)", chats, err)
	}
	if done, _ := db.metaDone(metaLayout); !done {
		t.Fatal("not marked complete")
	}
	if _, err := os.Stat(junk); err == nil {
		t.Fatal("the leftover temporary file was not removed")
	}
	for _, c := range []string{chatID, chatID2} {
		if got, want := countEvents(t, db, c), countEvents(t, ref, c); got != want {
			t.Fatalf("chat %.8s: %d events, an uninterrupted run gives %d", c, got, want)
		}
		if rep, err := db.Verify(c); err != nil || len(rep.Issues) != 0 {
			t.Fatalf("verify: %v %v", err, rep.Issues)
		}
		if rb, err := db.Rebuild(c, true); err != nil || rb.Total() != 0 {
			t.Fatalf("rebuild: %+v %v", rb.Diffs, err)
		}
	}
}

func TestAChatFileThatTheIndexDoesNotKnowIsPickedUp(t *testing.T) {
	db, path, _ := legacyDB(t)
	if _, _, err := db.BackfillEvents(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.node.Exec(`DELETE FROM chats`); err != nil { // the index is lost
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	ids, err := again.ChatIDs()
	if err != nil || len(ids) != 2 {
		t.Fatalf("chats after the index was lost: %v %v", ids, err)
	}
	if id, err := again.ResolveChat("two"); err != nil || id != chatID2 {
		t.Fatalf("the name comes from the file itself: %q %v", id, err)
	}
}

func TestAnUnreadableOrNewerChatFileSpoilsOnlyThatChat(t *testing.T) {
	db, path, _ := legacyDB(t)
	if _, _, err := db.BackfillEvents(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// One chat file is damaged ...
	if err := os.WriteFile(filepath.Join(dir, "chats", chatID+".db"), []byte("this is not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"-wal", "-shm"} {
		_ = os.Remove(filepath.Join(dir, "chats", chatID+".db"+p))
	}
	again, err := Open(path)
	if err != nil {
		t.Fatalf("one damaged chat must not stop the node: %v", err)
	}
	defer again.Close()
	if _, err := again.Load(chatID, "one"); err == nil {
		t.Fatal("a damaged chat file must be refused")
	}
	if _, err := again.Load(chatID2, "two"); err != nil {
		t.Fatalf("the other chat: %v", err)
	}
	// ... another was migrated by a newer binary.
	again.Release(chatID2)
	raw, err := OpenRaw(filepath.Join(dir, "chats", chatID2+".db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (9999, 'future', 'x', 'now')`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	if _, err := again.Load(chatID2, "two"); err == nil || !strings.Contains(err.Error(), "newer version") {
		t.Fatalf("a chat file of a newer version: %v", err)
	}
	if _, err := again.Load(chatID, "one"); err == nil {
		t.Fatal("still damaged")
	}
}

func TestADataDirectoryWithoutAnOldDatabaseHasNothingToLayOut(t *testing.T) {
	db, _ := openTemp(t)
	if db.layoutPending() {
		t.Fatal("a fresh directory has nothing to lay out")
	}
	if chats, evs, err := db.BackfillEvents(); err != nil || chats != 0 || evs != 0 {
		t.Fatalf("%d %d %v", chats, evs, err)
	}
	if _, err := db.Load(chatID, "x"); err != nil {
		t.Fatal(err)
	}
	if done, err := chatRaw(t, db, chatID).complete(); err != nil || !done {
		t.Fatalf("a new chat is complete from the start: %v %v", done, err)
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
	rows, err := chatRaw(t, db, chatID).db.Query(`SELECT seq, kind, lamport FROM events ORDER BY seq`)
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
	if rep, err := db.Verify(chatID); err != nil || len(rep.Issues) != 0 || rep.Events != len(want) {
		t.Fatalf("verify: %+v %v", rep, err)
	}
}
