package broker

import (
	"strings"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store/sqlite"
)

// pageOf is a page as a node holds it: its text, its version and the heads of its versions.
func pageOf(t *testing.T, db *sqlite.DB, chatID, path string) (string, int64, []int64) {
	t.Helper()
	var body string
	var ver int64
	var heads []int64
	err := db.ViewDocs(chatID, func(tx store.DocsTx) error {
		d, ok, err := tx.GetDoc(path)
		if err != nil || !ok {
			return err
		}
		body, ver = d.Body, d.Version
		heads, err = tx.Heads(path)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return body, ver, heads
}

func putPage(t *testing.T, c *client, path, body string, base int64) {
	t.Helper()
	a := proto.DocArgs{Op: "put", Path: path, Body: body}
	if base > 0 {
		a.Base = &base
	}
	if _, code := c.doc(a); code != "" {
		t.Fatalf("put %s: %q", path, code)
	}
}

func TestThePagesOfTwoNodesMeetAndAConcurrentEditIsAConflictOnBoth(t *testing.T) {
	p := newTwoBrokers(t)
	alice, bob := p.a.connect(t), p.b.connect(t)
	if _, code := alice.join(p.tok, "alice", proto.ProtoVersion); code != "" {
		t.Fatalf("alice joins: %q", code)
	}
	if _, code := bob.join(p.tok, "bob", proto.ProtoVersion); code != "" {
		t.Fatalf("bob joins: %q", code)
	}
	const page = "/arch/plan"

	putPage(t, alice, page, "# Plan\nfirst, from a", 0)
	eventually(t, "the page reaches b", func() bool { b, _, _ := pageOf(t, p.bdb, p.id, page); return strings.Contains(b, "first, from a") })
	_, vB, _ := pageOf(t, p.bdb, p.id, page)
	putPage(t, bob, page, "# Plan\nsecond, from b", vB)
	eventually(t, "the edit of b reaches a", func() bool { b, _, _ := pageOf(t, p.adb, p.id, page); return strings.Contains(b, "second, from b") })
	for _, db := range []*sqlite.DB{p.adb, p.bdb} {
		if _, _, heads := pageOf(t, db, p.id, page); len(heads) != 1 {
			t.Fatalf("a page edited in turn has one head, got %v", heads)
		}
	}

	// Cut off from each other, both write over the same version.
	setShared := func(share bool) {
		t.Helper()
		for _, n := range []struct {
			db *sqlite.DB
			to string
			b  *Broker
		}{{p.adb, p.b.origin, p.a}, {p.bdb, p.a.origin, p.b}} {
			var err error
			if share {
				err = n.db.SharePeerChat(p.id, n.to)
			} else {
				err = n.db.UnsharePeerChat(p.id, n.to)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := n.b.peerMgr.Refresh(); err != nil {
				t.Fatal(err)
			}
		}
	}
	setShared(false)
	eventually(t, "the nodes are apart", func() bool { return len(p.a.peerMgr.Sessions()) == 0 && len(p.b.peerMgr.Sessions()) == 0 })
	_, vA, _ := pageOf(t, p.adb, p.id, page)
	_, vB, _ = pageOf(t, p.bdb, p.id, page)
	putPage(t, alice, page, "# Plan\nalice's way", vA)
	putPage(t, bob, page, "# Plan\nbob's way", vB)
	setShared(true)

	both := func(db *sqlite.DB) bool {
		_, _, heads := pageOf(t, db, p.id, page)
		return len(heads) == 2
	}
	eventually(t, "both nodes hold both versions as a conflict", func() bool { return both(p.adb) && both(p.bdb) })
	bodyA, verA, _ := pageOf(t, p.adb, p.id, page)
	bodyB, verB, _ := pageOf(t, p.bdb, p.id, page)
	if bodyA != bodyB || (!strings.Contains(bodyA, "alice's way") && !strings.Contains(bodyA, "bob's way")) {
		t.Fatalf("the nodes show different pages in a conflict:\n%q\n%q", bodyA, bodyB)
	}
	for _, c := range []*client{alice, bob} {
		r, code := c.doc(proto.DocArgs{Op: "get", Path: page})
		if code != "" || !strings.Contains(r.Text, "CONFLICT") {
			t.Fatalf("a page in conflict says so: %q %q", r.Text, code)
		}
	}
	// Both versions can be read.
	for _, db := range []*sqlite.DB{p.adb, p.bdb} {
		seen := map[string]bool{}
		err := db.ViewDocs(p.id, func(tx store.DocsTx) error {
			vs, err := tx.ListVersions(page, 0, 10)
			for _, v := range vs {
				seen[v.Body] = true
			}
			return err
		})
		if err != nil || !seen["# Plan\nalice's way"] || !seen["# Plan\nbob's way"] {
			t.Fatalf("both versions are kept: %v %v", seen, err)
		}
	}

	// The next write is over both heads and settles it, on both nodes.
	putPage(t, alice, page, "# Plan\nsettled", verA)
	eventually(t, "the conflict is settled everywhere", func() bool {
		ba, _, ha := pageOf(t, p.adb, p.id, page)
		bb, _, hb := pageOf(t, p.bdb, p.id, page)
		return ba == "# Plan\nsettled" && bb == ba && len(ha) == 1 && len(hb) == 1
	})
	_ = verB
	consistent(t, p.adb, "the pages meet (node a)")
	consistent(t, p.bdb, "the pages meet (node b)")
}
