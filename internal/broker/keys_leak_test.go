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
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/owner"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
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
	b := newTestBroker(&noSecrets{newMemStore()})
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
// The protection is never dropped silently: when the chat holds another key for a protected nick than
// the one the broker has the secret for (a way that is not an invitation changed it), a login is
// refused, the administrator log has it and the chat is told. An invitation of the owner is the way out.
func TestAProtectedNickWhoseKeyInTheChatChangedBehindItsBackIsRefusedAndTheChatIsToldAndAnInvitationHeals(t *testing.T) {
	ic := newInvitedChat(t)
	a := ic.session(t, "bob", sidA)
	watch := ic.watcher(t)
	// A change of the chat's key that is no invitation (a failure that left the two databases apart, a
	// hand-edited projection, a path of the code that forgot the secret).
	ic.b.inChat(ic.chat, func(ch *chat) { ch.nicks["bob"].pub = owner.Encode(newKey(t).Public().(ed25519.PublicKey)) })

	for i := 0; i < 2; i++ { // every time, not only the first
		if _, _, code := ic.login(t, "bob", a); code != "key_mismatch" {
			t.Fatalf("login %d: %q", i, code)
		}
	}
	heard := watch.heard(t, "not the protected one", 3*time.Second)
	if !strings.Contains(heard, "@bob") || !strings.Contains(heard, "invitation") {
		t.Fatalf("the chat was not told: %q", heard)
	}
	if more := watch.heard(t, "not the protected one", 300*time.Millisecond); more != "" {
		t.Fatalf("told again at once: %q", more)
	}
	if l := lastOf(adminLog(t, ic), "key.mismatch"); !strings.Contains(l, `"nick":"bob"`) || !strings.Contains(l, "refused") {
		t.Fatalf("admin log: %q", l)
	}
	// An older client with the changed key does not get in by "it is not protected any more" either.
	if _, code := ic.joinWith(t, ic.b.connect(t), "bob", a, secretOf(t), proto.InviteProto); code == "" {
		t.Fatal("an older client got in under the nick")
	}
	// The owner's invitation replaces the key and the nick works again.
	a2 := secretOf(t)
	c, _, _ := ic.enroll(t, "bob", a2)
	ic.leave(t, c, "bob")
	if _, w, code := ic.login(t, "bob", a2); code != "" || w.State.Me != "bob" {
		t.Fatalf("after the invitation: %q", code)
	}
}

// The owner's reset frees a nick and with it the broker's half of its key: a nick that is freed is not
// "protected but mismatched".
func TestTheOwnersResetOfAProtectedNickTakesItsSecretAwayToo(t *testing.T) {
	ic := newInvitedChat(t)
	a := secretOf(t)
	c, _, _ := ic.enroll(t, "bob", a)
	if code := c.call("leave", nil, nil); code != "" {
		t.Fatalf("leave: %q", code)
	}
	if _, code := adminCall(t, ic.adm, ic.ownk, "nick_reset", proto.AdminRole{Chat: ic.chat, Nick: "bob"}); code != "" {
		t.Fatalf("nick_reset: %q", code)
	}
	if row, _ := ic.db.ActiveNickSecret(ic.chat, "bob"); row != nil {
		t.Fatalf("the secret stayed after the reset: %+v", row)
	}
	if _, code := ic.unlock(t, ic.b.connect(t), "bob", a); code != "unlock_unknown" {
		t.Fatalf("unlock after the reset: %q", code)
	}
}

// noSecrets is a store that keeps no secrets of protected keys (the interface hides what a memory store has).
type noSecrets struct{ store.Backend }
