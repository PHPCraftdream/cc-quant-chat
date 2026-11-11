package sqlite

import (
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

func TestMessagesCarryTheNodeAndAChatClock(t *testing.T) {
	db, _ := openTemp(t)
	if _, err := db.Load(chatID, "x"); err != nil {
		t.Fatal(err)
	}
	add := func(id int64, origin string) {
		m := proto.Message{ID: id, TS: "t", Room: proto.RoomCommon, From: "a", Text: "x"}
		mustAppend(t, db, store.Record{T: "msg", Key: "r:common", Msg: &m, Origin: origin})
	}
	add(1, "node-one")
	add(2, "node-one")

	c, err := db.chat(chatID, "", false)
	if err != nil || c == nil {
		t.Fatal(c, err)
	}
	row := func(id int64) (origin string, lamport int64) {
		if err := c.db.QueryRow(`SELECT origin, lamport FROM messages WHERE id = ?`, id).Scan(&origin, &lamport); err != nil {
			t.Fatal(err)
		}
		return
	}
	if o, l := row(1); o != "node-one" || l != 1 {
		t.Fatalf("message 1: origin %q lamport %d", o, l)
	}
	if _, l := row(2); l != 2 {
		t.Fatalf("the chat's clock must advance with every write, got %d", l)
	}
}

func TestOriginIsRecordedOnceWithItsKey(t *testing.T) {
	db, _ := openTemp(t)
	pub := []byte("0123456789abcdef0123456789abcdef")
	for i := 0; i < 2; i++ {
		if err := db.RegisterOrigin("node-one", pub); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := db.node.QueryRow(`SELECT count(*) FROM origins`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("origins rows: %d, err %v", n, err)
	}
	keys, err := db.originKeys()
	if err != nil || string(keys["node-one"]) != string(pub) {
		t.Fatalf("keys %v, %v", keys, err)
	}
}

func TestJournalClockIsSharedWithMessages(t *testing.T) {
	db, _ := openTemp(t)
	if _, err := db.Load(chatID, "x"); err != nil {
		t.Fatal(err)
	}
	m := proto.Message{ID: 1, TS: "t", Room: proto.RoomCommon, From: "a", Text: "x"}
	mustAppend(t, db, store.Record{T: "msg", Key: "r:common", Msg: &m})
	var first, second int64
	if err := db.Do(chatID, func(tx store.BoardTx) error {
		var err error
		if first, err = tx.NextLamport(); err != nil {
			return err
		}
		second, err = tx.NextLamport()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if first != 2 || second != 3 {
		t.Fatalf("clock = %d, %d after one message; want 2, 3", first, second)
	}
}
