package sqlite

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/migrate"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

func size(t *testing.T, path string) int64 {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}

// TestJournalScale measures the layout and the journal on a database with many messages: how big
// the chat file is next to the single database it came from, how long the first start (laying
// out the old database and making the journal) and a later start take. It runs only with
// CCQC_SCALE=1 (CI prints the numbers; N is CCQC_SCALE_N).
func TestJournalScale(t *testing.T) {
	if os.Getenv("CCQC_SCALE") == "" {
		t.Skip("set CCQC_SCALE=1 to measure")
	}
	n := 100000
	if v, err := strconv.Atoi(os.Getenv("CCQC_SCALE_N")); err == nil && v > 0 {
		n = v
	}
	path := filepath.Join(t.TempDir(), "ccqc.db")
	k := newKeys(t)
	old, err := OpenRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	ms, err := LegacyMigrations()
	if err == nil {
		_, err = migrate.Up(context.Background(), old, ms)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`INSERT INTO chats (id, name, created_at, lamport) VALUES (?, 'scale', '2026-01-01T00:00:00Z', ?)`, chatID, n); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`INSERT INTO rooms (chat_id, room) VALUES (?, 'common')`, chatID); err != nil {
		t.Fatal(err)
	}
	tx, err := old.Begin()
	if err != nil {
		t.Fatal(err)
	}
	st, err := tx.Prepare(`INSERT INTO messages (chat_id, id, thread, room, from_nick, from_role, to_nick, text, reply_to, ts, client_msg_id, owner, origin, lamport)
		VALUES (?, ?, 'r:common', 'common', ?, '', '', ?, 0, '2026-01-01T00:00:00Z', ?, 0, '', ?)`)
	if err != nil {
		t.Fatal(err)
	}
	filler := strings.Repeat("lorem ipsum dolor sit amet, ", 6)
	for i := 1; i <= n; i++ {
		if _, err := st.Exec(chatID, i, fmt.Sprintf("agent-%d", i%7), fmt.Sprintf("message %d: %s", i, filler), fmt.Sprintf("c%d", i), i); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	size0 := size(t, path)

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	startNode(t, db, k)
	t0 := time.Now()
	chats, evs, err := db.BackfillEvents()
	first := time.Since(t0)
	if err != nil || chats != 1 || evs < n {
		t.Fatalf("layout: %d chats, %d events, %v", chats, evs, err)
	}
	chatFile := filepath.Join(filepath.Dir(path), "chats", chatID+".db")
	size1 := size(t, chatFile)
	c := chatRaw(t, db, chatID)
	var text, evBytes int64
	if err := c.db.QueryRow(`SELECT SUM(LENGTH(text)) FROM messages`).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if err := c.db.QueryRow(`SELECT SUM(LENGTH(payload) + LENGTH(assigned) + LENGTH(prev) + LENGTH(node_sig) + LENGTH(sig) + LENGTH(author_pub) + LENGTH(kind) + LENGTH(ts) + LENGTH(origin)) FROM events`).Scan(&evBytes); err != nil {
		t.Fatal(err)
	}
	t.Logf("SCALE messages=%d events=%d", n, evs)
	t.Logf("SCALE the single database of earlier versions: %.1f MiB; the chat file with its journal: %.1f MiB (x%.2f)", mib(size0), mib(size1), float64(size1)/float64(size0))
	t.Logf("SCALE message text %.1f MiB; event content %.1f MiB (%.0f bytes per event)", mib(text), mib(evBytes), float64(evBytes)/float64(evs))
	t.Logf("SCALE first start (laying out the old database, moving %d rows into the journal and signing them): %s", n, first.Round(time.Millisecond))

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	t0 = time.Now()
	db2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	db2.SetNode(k.origin, k.nodePriv)
	if _, _, err := db2.BackfillEvents(); err != nil {
		t.Fatal(err)
	}
	start := time.Since(t0)
	t0 = time.Now()
	if recs, err := db2.Load(chatID, "scale"); err != nil || len(recs) < 2 {
		t.Fatalf("load: %d records, %v", len(recs), err)
	}
	t.Logf("SCALE later start (open node.db, check): %s; opening the chat and loading it into memory: %s", start.Round(time.Millisecond), time.Since(t0).Round(time.Millisecond))

	t0 = time.Now()
	rep, err := db2.Verify(chatID)
	if err != nil || len(rep.Issues) != 0 {
		t.Fatalf("verify: %v %v", err, rep.Issues)
	}
	t.Logf("SCALE verify (chains and %d node signatures): %s", rep.Events, time.Since(t0).Round(time.Millisecond))
	t0 = time.Now()
	rb, err := db2.Rebuild(chatID, true)
	if err != nil || rb.Total() != 0 {
		t.Fatalf("rebuild: %+v %v", rb.Diffs, err)
	}
	t.Logf("SCALE rebuild from %d events, compared with the projections: %s", rb.Events, time.Since(t0).Round(time.Millisecond))

	t0 = time.Now()
	const live = 2000
	for i := 0; i < live; i++ {
		m := &proto.Message{ID: int64(n + 1 + i), TS: "2026-01-02T00:00:00Z", Room: proto.RoomCommon, From: "agent-1", Text: "live " + filler}
		if err := db2.Append(chatID, store.Record{T: "msg", Key: "r:common", Msg: m}); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("SCALE a live message (event, signature and projection in one transaction): %s each", (time.Since(t0) / live).Round(time.Microsecond))
}

func mib(b int64) float64 { return float64(b) / (1 << 20) }
