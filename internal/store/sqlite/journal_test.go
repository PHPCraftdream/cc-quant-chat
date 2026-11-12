package sqlite

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
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

// twoChats: a data directory with two chats, "one" and "two", each with a message.
func twoChats(t *testing.T) (*DB, string) {
	t.Helper()
	db, path := openTemp(t)
	startNode(t, db, newKeys(t))
	for _, c := range []struct{ id, name string }{{chatID, "one"}, {chatID2, "two"}} {
		if _, err := db.Load(c.id, c.name); err != nil {
			t.Fatal(err)
		}
		m := &proto.Message{ID: 1, TS: "2026-01-01T00:00:00Z", Room: proto.RoomCommon, From: "a", Text: "hi"}
		if err := db.Append(c.id, store.Record{T: "msg", Key: "r:common", Msg: m}); err != nil {
			t.Fatal(err)
		}
	}
	return db, path
}

func TestAChatFileThatTheIndexDoesNotKnowIsPickedUp(t *testing.T) {
	db, path := twoChats(t)
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
	db, path := twoChats(t)
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
