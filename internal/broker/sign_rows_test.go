package broker

import (
	"path/filepath"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store/sqlite"
)

// reopen opens the database of a stopped broker anew, as the next broker would.
func reopen(t *testing.T, path string) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestSignedHistoryOfAllFourKindsSurvivesARestart: a message, board changes (with a revert and a
// comment) and a document version are signed by their author; after the broker restarts every
// row still carries a key, a signature and the payload that verify on their own.
func TestSignedHistoryOfAllFourKindsSurvivesARestart(t *testing.T) {
	b, path := boardBroker(t)
	tok, id := token(t)
	k := newKey(t)
	a := b.mustJoin(t, tok, "alice", k)

	must := func(lam int64, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return lam
	}
	lMsg := must(a.do("send", "msg", &proto.SendArgs{Room: "common", Text: "hello"}, nil))
	lCreate := must(a.do("board", "task.create", &proto.BoardArgs{Op: "task.create", Title: sp("ship it")}, nil))
	lUpdate := must(a.do("board", "task.update", &proto.BoardArgs{Op: "task.update", ID: 1, Title: sp("ship it now")}, nil))
	lRevert := must(a.do("board", "board.revert", &proto.BoardArgs{Op: "board.revert", LogID: 2}, nil))
	lComment := must(a.do("board", "task.comment", &proto.BoardArgs{Op: "task.comment", ID: 1, Text: "on it"}, nil))
	lDoc := must(a.do("doc", "doc.put", &proto.DocArgs{Op: "put", Path: "/arch", Body: "# Architecture\n"}, nil))

	db := reopen(t, path)
	if n, err := db.Lamport(id); err != nil || n != lDoc {
		t.Fatalf("clock after restart = %d, %v; want %d", n, err, lDoc)
	}
	pub := pubOf(k)

	recs, err := db.Load(id, "team")
	if err != nil {
		t.Fatal(err)
	}
	var msgs int
	for _, r := range recs {
		if r.T != "msg" {
			continue
		}
		msgs++
		if r.AuthorPub != pub || !verifies(id, b.origin, "msg", r.AuthorPub, r.Sig, r.Payload, lMsg) {
			t.Fatalf("message row does not verify: %+v", r)
		}
	}
	if msgs != 1 {
		t.Fatalf("%d messages", msgs)
	}

	want := map[int64]struct {
		kind string
		lam  int64
	}{1: {"task.create", lCreate}, 2: {"task.update", lUpdate}, 3: {"board.revert", lRevert}, 4: {"task.comment", lComment}}
	var commentSig string
	err = db.View(id, func(tx store.BoardTx) error {
		log, err := tx.LogFor("", 0, 0, 100)
		if err != nil || len(log) != len(want) {
			t.Fatalf("journal: %d entries, %v", len(log), err)
		}
		for _, e := range log {
			w := want[e.ID]
			if e.Lamport != w.lam || e.AuthorPub != pub || !verifies(id, b.origin, w.kind, e.AuthorPub, e.Sig, e.Payload, e.Lamport) {
				t.Errorf("journal #%d (%s): lamport %d (want %d), does not verify: %+v", e.ID, w.kind, e.Lamport, w.lam, e)
			}
			if e.ID == 4 {
				commentSig = e.Sig
			}
		}
		cs, err := tx.ListComments(1, 0, 10)
		if err != nil || len(cs) != 1 {
			t.Fatalf("comments: %d, %v", len(cs), err)
		}
		if cs[0].AuthorPub != pub || cs[0].Sig != commentSig || cs[0].Lamport != lComment {
			t.Errorf("comment row: %+v, want the signature of its journal entry", cs[0])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	err = db.ViewDocs(id, func(tx store.DocsTx) error {
		vs, err := tx.AllVersions()
		if err != nil || len(vs) != 1 {
			t.Fatalf("versions: %d, %v", len(vs), err)
		}
		v := vs[0]
		if v.Lamport != lDoc || v.AuthorPub != pub || !verifies(id, b.origin, "doc.put", v.AuthorPub, v.Sig, v.Payload, v.Lamport) {
			t.Errorf("version row does not verify: %+v", v)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// The restarted broker takes the same nick and its writes continue from the stored clock.
	b2 := newTestBroker(db)
	a2 := b2.mustJoin(t, tok, "alice", k)
	if _, err := a2.do("send", "msg", &proto.SendArgs{Room: "common", Text: "after"}, nil); err != nil {
		t.Fatalf("after the restart: %v", err)
	}
}

// A move writes two versions of one event: both carry its signature and its clock value.
func TestAMoveSharesOneSignedEventBetweenItsTwoVersions(t *testing.T) {
	b, path := boardBroker(t)
	tok, id := token(t)
	a := b.mustJoin(t, tok, "alice", newKey(t))
	if _, err := a.do("doc", "doc.put", &proto.DocArgs{Op: "put", Path: "/a", Body: "# A\n"}, nil); err != nil {
		t.Fatal(err)
	}
	lam, err := a.do("doc", "doc.move", &proto.DocArgs{Op: "move", Path: "/a", To: "/b"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = reopen(t, path).ViewDocs(id, func(tx store.DocsTx) error {
		vs, err := tx.AllVersions()
		if err != nil {
			return err
		}
		n := 0
		for _, v := range vs {
			if v.Lamport == lam {
				n++
				if !verifies(id, b.origin, "doc.move", v.AuthorPub, v.Sig, v.Payload, lam) {
					t.Errorf("%s v%d does not verify", v.Path, v.Version)
				}
			}
		}
		if n != 2 {
			t.Errorf("%d versions carry the move's clock value, want 2", n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRefusedBoardAndDocWritesAreNotStored(t *testing.T) {
	b, path := boardBroker(t)
	tok, id := token(t)
	a := b.mustJoin(t, tok, "alice", newKey(t))
	args := &proto.BoardArgs{Op: "task.create", Title: sp("t")}
	if err := a.sg.Sign("task.create", args, 1); err != nil {
		t.Fatal(err)
	}
	args.Title = sp("changed after signing")
	if got := codeOf(a.callE("board", args, nil)); got != "bad_signature" {
		t.Fatalf("board: %q", got)
	}
	d := &proto.DocArgs{Op: "put", Path: "/x", Body: "body"}
	if err := a.sg.Sign("doc.put", d, 1); err != nil {
		t.Fatal(err)
	}
	d.Body = "other"
	if got := codeOf(a.callE("doc", d, nil)); got != "bad_signature" {
		t.Fatalf("doc: %q", got)
	}
	if got := codeOf(a.callE("board", proto.BoardArgs{Op: "task.create", Title: sp("plain")}, nil)); got != "unsigned" {
		t.Fatalf("unsigned board write: %q", got)
	}
	db := reopen(t, filepath.Clean(path))
	if n, _ := db.Lamport(id); n != 0 {
		t.Fatalf("clock = %d after refused writes", n)
	}
}
