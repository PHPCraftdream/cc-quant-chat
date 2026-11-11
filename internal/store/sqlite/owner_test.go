package sqlite

import (
	"bytes"
	"errors"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

func TestOwnerKeyIsRegisteredOnceAndReplacedOnlyFromTheCurrentOne(t *testing.T) {
	db, path := openTemp(t)
	if _, _, found, err := db.Owner(); err != nil || found {
		t.Fatalf("fresh database: found %v, err %v", found, err)
	}
	k1, k2, k3 := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32), bytes.Repeat([]byte{3}, 32)
	if err := db.SetOwner(k1); err != nil {
		t.Fatal(err)
	}
	if err := db.SetOwner(k2); !errors.Is(err, store.ErrOwnerExists) {
		t.Fatalf("second registration: %v, want ErrOwnerExists", err)
	}
	if err := db.ReplaceOwner(k3, k2); !errors.Is(err, store.ErrOwnerChanged) {
		t.Fatalf("replace from a key that is not the stored one: %v", err)
	}
	if err := db.ReplaceOwner(k1, k2); err != nil {
		t.Fatal(err)
	}
	db.Close()

	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	got, _, found, err := again.Owner()
	if err != nil || !found || !bytes.Equal(got, k2) {
		t.Fatalf("after reopening: %x, found %v, err %v; want the replaced key", got, found, err)
	}
}

func TestOwnerMarkIsPartOfTheStoredMessage(t *testing.T) {
	db, _ := openTemp(t)
	if _, err := db.Load(chatID, "x"); err != nil {
		t.Fatal(err)
	}
	mine := proto.Message{ID: 1, TS: "t", Room: proto.RoomCommon, From: "owner", Text: "from the owner", Owner: true}
	theirs := proto.Message{ID: 2, TS: "t", Room: proto.RoomCommon, From: "alice", Text: "from an agent"}
	mustAppend(t, db,
		store.Record{T: "msg", Key: "r:common", Msg: &mine},
		store.Record{T: "msg", Key: "r:common", Msg: &theirs},
	)
	recs, err := db.Load(chatID, "x")
	if err != nil {
		t.Fatal(err)
	}
	marks := map[int64]bool{}
	for _, r := range recs {
		if r.T == "msg" {
			marks[r.Msg.ID] = r.Msg.Owner
		}
	}
	if !marks[1] || marks[2] || len(marks) != 2 {
		t.Fatalf("owner marks after a reload = %v, want only #1", marks)
	}
}
