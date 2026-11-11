package sqlite

import (
	"errors"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

func TestClientMessageIDIsIdempotent(t *testing.T) {
	db, _ := openTemp(t)
	if _, err := db.Load(chatID, "x"); err != nil {
		t.Fatal(err)
	}
	rec := func(id int64, from, cid string) store.Record {
		msg := &proto.Message{ID: id, TS: "t", Room: proto.RoomCommon, From: from, Text: "x"}
		return store.Record{T: "msg", Key: "r:common", Msg: msg, ClientID: cid}
	}
	mustAppend(t, db, rec(1, "alice", "c1"))
	err := db.Append(chatID, rec(2, "alice", "c1"))
	var dup *store.DuplicateError
	if !errors.As(err, &dup) || dup.ID != 1 {
		t.Fatalf("repeat: err %v; want DuplicateError with id 1", err)
	}
	mustAppend(t, db, rec(2, "bob", "c1"))                      // another sender may use the same id
	mustAppend(t, db, rec(3, "alice", ""), rec(4, "alice", "")) // no id: no dedupe
	recs, _ := db.Load(chatID, "x")
	n := 0
	for _, r := range recs {
		if r.T == "msg" {
			n++
		}
	}
	if n != 4 {
		t.Fatalf("stored %d messages, want 4", n)
	}
}
