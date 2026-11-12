package broker

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/owner"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store/sqlite"
)

// B is the broker's secret for a nick and must not leave node.db's table of secrets: not into a chat
// database (those are replicated, exported and verified), not into the journal that other nodes get,
// not into the other tables of node.db (the administrator log among them).
func TestTheBrokersSecretStaysInItsOwnTableOfNodeDB(t *testing.T) {
	ic := newInvitedChat(t)
	a := secretOf(t)
	c, bsec, _ := ic.enroll(t, "bob", a)
	ic.leave(t, c, "bob")
	cc, _, code := ic.login(t, "bob", a)
	if code != "" {
		t.Fatal(code)
	}
	ic.leave(t, cc, "bob")
	// Wrong proofs go to the administrator log too.
	ic.unlock(t, ic.b.connect(t), "bob", secretOf(t))
	needles := [][]byte{bsec, []byte(owner.Encode(bsec))}

	_ = filepath.WalkDir(filepath.Dir(ic.path), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.Contains(p, ".db") {
			return err
		}
		if filepath.Base(p) == "node.db" || strings.HasPrefix(filepath.Base(p), "node.db-") {
			return nil // the table of secrets lives here: looked at below, table by table
		}
		raw, _ := os.ReadFile(p)
		for _, n := range needles {
			if bytes.Contains(raw, n) {
				t.Errorf("B is in %s", p)
			}
		}
		return nil
	})

	raw, err := sqlite.OpenRaw(filepath.Join(filepath.Dir(ic.path), "node.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	tables, err := raw.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name != 'nick_secrets'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var n string
		tables.Scan(&n)
		names = append(names, n)
	}
	tables.Close()
	if len(names) < 5 {
		t.Fatalf("looked at too few tables: %v", names)
	}
	for _, n := range names {
		rows, err := raw.Query(`SELECT * FROM "` + n + `"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			rows.Scan(ptrs...)
			text, _ := json.Marshal(vals)
			for _, nd := range needles {
				if bytes.Contains(text, nd) || strings.Contains(string(text), string(nd)) {
					t.Errorf("B is in the table %s of node.db", n)
				}
			}
		}
		rows.Close()
	}

	// What other nodes get: the events of the journal, as they are sent.
	evs, err := ic.db.EventsAfter(ic.chat, ic.b.origin, 0, 1000)
	if err != nil || len(evs) == 0 {
		t.Fatalf("%d events, %v", len(evs), err)
	}
	wire, _ := json.Marshal(evs)
	for _, n := range needles {
		if bytes.Contains(wire, n) {
			t.Error("B is in the events that go to other nodes")
		}
	}
}

// A broker without the SQLite store has nowhere to keep the secrets: it says so and enrolls nobody.
func TestABrokerWithoutTheDatabaseRefusesProtectedKeysAndSaysWhy(t *testing.T) {
	b := newTestBroker(newMemStore())
	c := b.connect(t)
	chat := strings.Repeat("a", 64)
	if code := c.call("key_prepare", proto.KeyPrepareArgs{Chat: chat, Nick: "bob"}, nil); code != "keys_unavailable" {
		t.Fatalf("key_prepare: %q", code)
	}
	if code := c.call("key_unlock", proto.KeyUnlockArgs{Chat: chat, Nick: "bob", AuthSig: "x"}, nil); code != "keys_unavailable" {
		t.Fatalf("key_unlock: %q", code)
	}
}

// Nothing changes for a nick whose key is not protected, whatever the level of the client.
func TestANickThatIsNotProtectedJoinsAsBeforeAtTheOldAndTheNewLevel(t *testing.T) {
	ic := newInvitedChat(t)
	in := ic.issue(t, "bob")
	k := newKey(t)
	c := ic.b.connect(t)
	if _, code := ic.join(t, c, "bob", k, &in); code != "" {
		t.Fatalf("invited: %q", code)
	}
	ic.leave(t, c, "bob")
	for _, level := range []int{proto.InviteProto, proto.KeysProto} {
		cl := ic.b.connect(t)
		var ch proto.Challenge
		cl.call("challenge", nil, &ch)
		pub := owner.Encode(k.Public().(ed25519.PublicKey))
		var w proto.Welcome
		code := cl.call("join", proto.JoinArgs{Chat: ic.chat, Name: "bob", Pub: pub, Proto: level,
			NickSig: owner.Encode(ed25519.Sign(k, proto.NickJoinMessage(ic.chat, "bob", pub, ch.Nonce)))}, &w)
		if code != "" || w.State.Me != "bob" {
			t.Fatalf("level %d: %q", level, code)
		}
		ic.leave(t, cl, "bob")
	}
}
