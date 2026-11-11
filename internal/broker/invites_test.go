package broker

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/invite"
	"github.com/PHPCraftdream/cc-quant-chat/internal/owner"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store/sqlite"
)

// adminCall is adminReq that also returns the text of the reply.
func adminCall(t *testing.T, c *client, k ed25519.PrivateKey, op string, args any) (string, string) {
	t.Helper()
	var ch proto.Challenge
	if code := c.call("challenge", nil, &ch); code != "" {
		t.Fatalf("challenge: %q", code)
	}
	raw, _ := json.Marshal(args)
	canon, _ := proto.CanonicalJSON(raw)
	sig := owner.Encode(ed25519.Sign(k, proto.AdminMessage(op, canon, ch.Nonce)))
	var r proto.BoardReply
	code := c.call("admin", proto.AdminArgs{Op: op, Args: raw, Sig: sig}, &r)
	return r.Text, code
}

func inviteBroker(t *testing.T) (*Broker, *sqlite.DB, string, ed25519.PrivateKey, *client) {
	t.Helper()
	b, db, path, _ := journalBroker(t)
	k := newKey(t)
	register(t, b, k)
	return b, db, path, k, b.connect(t)
}

func TestAChatForInvitationsIsMadeByTheOwnerAndFoundByItsName(t *testing.T) {
	b, _, _, k, adm := inviteBroker(t)
	if _, code := adminCall(t, adm, newKey(t), "chat_new", proto.AdminChatNew{Name: "team"}); code != "admin_only" {
		t.Fatalf("without the owner's signature: %q", code)
	}
	if _, code := adminCall(t, adm, k, "chat_new", proto.AdminChatNew{Name: "Bad Name"}); code != "bad_name" {
		t.Fatalf("a bad name: %q", code)
	}
	for i := 0; i < 2; i++ {
		if _, code := adminCall(t, adm, k, "chat_new", proto.AdminChatNew{Name: "team"}); code != "" {
			t.Fatalf("chat_new: %q", code)
		}
	}
	var found proto.ChatFindReply
	if code := b.connect(t).call("chat_find", proto.ChatFindArgs{Name: "team"}, &found); code != "" { // no signature: an id is no secret
		t.Fatalf("chat_find: %q", code)
	}
	if len(found.Chats) != 2 || found.Chats[0].ID == found.Chats[1].ID {
		t.Fatalf("two chats of one name are two: %+v", found)
	}
	is := b.store.(store.Invites)
	for _, c := range found.Chats {
		if mode, err := is.ChatMode(c.ID); err != nil || mode != store.ModeInvite || len(c.ID) != 64 {
			t.Fatalf("a chat made for invitations: %q %q %v", c.ID, mode, err)
		}
	}
	var none proto.ChatFindReply
	b.connect(t).call("chat_find", proto.ChatFindArgs{Name: "nope"}, &none)
	if len(none.Chats) != 0 {
		t.Fatalf("a chat that is not there: %+v", none)
	}
	// Without an owner there is nothing to make chats with.
	b2 := newTestBroker(newMemStore())
	if _, code := adminCall(t, b2.connect(t), k, "chat_new", proto.AdminChatNew{Name: "team"}); code != "no_owner" {
		t.Fatalf("no owner: %q", code)
	}
}

func TestInvitationsAreIssuedListedAndRevokedAndTheSecretStaysInTheOneReply(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	b, db, path, k, adm := inviteBroker(t)
	if _, code := adminCall(t, adm, k, "chat_new", proto.AdminChatNew{Name: "team"}); code != "" {
		t.Fatal(code)
	}
	var found proto.ChatFindReply
	adm.call("chat_find", proto.ChatFindArgs{Name: "team"}, &found)
	chat := found.Chats[0].ID

	line, code := adminCall(t, adm, k, "invite_new", proto.AdminInviteNew{Chat: chat, Nick: "bob", TTLSeconds: 600})
	if code != "" {
		t.Fatalf("invite_new: %q", code)
	}
	in, err := invite.Parse(line)
	if err != nil || in.ChatID != chat || in.Chat != "team" || in.Nick != "bob" || in.Kind != invite.KindChat {
		t.Fatalf("the line: %+v %v", in, err)
	}
	if d := time.Until(time.Unix(in.Exp, 0)); d < 9*time.Minute || d > 10*time.Minute+time.Second {
		t.Fatalf("it lives %v, want ten minutes", d)
	}
	for name, a := range map[string]proto.AdminInviteNew{
		"a second":       {Chat: chat, Nick: "bob", TTLSeconds: 30},
		"eight days":     {Chat: chat, Nick: "bob", TTLSeconds: 8 * 86400},
		"the owner nick": {Chat: chat, Nick: proto.OwnerNick},
		"a bad nick":     {Chat: chat, Nick: "Bob!"},
	} {
		if _, code := adminCall(t, adm, k, "invite_new", a); code != "bad_args" && code != "bad_name" {
			t.Errorf("%s: %q", name, code)
		}
	}
	ids, _ := b.store.(interface{ ChatIDs() ([]string, error) }).ChatIDs()
	if _, code := adminCall(t, adm, k, "invite_new", proto.AdminInviteNew{Chat: strings.Repeat("c", 64), Nick: "bob"}); code != "no_such_chat" {
		t.Fatalf("an unknown chat: %q", code)
	}
	if after, _ := b.store.(interface{ ChatIDs() ([]string, error) }).ChatIDs(); len(after) != len(ids) {
		t.Fatal("issuing an invitation for an unknown id made a chat of it")
	}

	list, _ := adminCall(t, adm, k, "invite_list", proto.AdminInviteID{Chat: chat})
	if !strings.Contains(list, invite.Short(in.ID())) || !strings.Contains(list, "@bob") || !strings.Contains(list, "open") || strings.Contains(list, in.ID()) {
		t.Fatalf("the list shows the short id, the nick, the time and the state, nothing more:\n%s", list)
	}
	if _, code := adminCall(t, adm, k, "invite_revoke", proto.AdminInviteID{Chat: chat, ID: in.ID()[:4]}); code != "bad_args" {
		t.Fatalf("an id that is too short: %q", code)
	}
	if text, code := adminCall(t, adm, k, "invite_revoke", proto.AdminInviteID{Chat: chat, ID: invite.Short(in.ID())}); code != "" || !strings.Contains(text, "revoked") {
		t.Fatalf("revoke: %q %q", text, code)
	}
	if _, code := adminCall(t, adm, k, "invite_revoke", proto.AdminInviteID{Chat: chat, ID: invite.Short(in.ID())}); code != "not_open" {
		t.Fatalf("revoked twice: %q", code)
	}
	if list, _ = adminCall(t, adm, k, "invite_list", proto.AdminInviteID{Chat: chat}); !strings.Contains(list, "revoked") {
		t.Fatalf("after the revoke:\n%s", list)
	}

	// The journal has both, signed by the owner, and verifies; the projections still rebuild.
	kinds := map[string]bool{}
	evs, err := db.EventsAfter(chat, b.origin, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		kinds[e.Kind] = true
		if strings.HasPrefix(e.Kind, "invite.") && (e.Sig == "" || e.AuthorPub == "") {
			t.Errorf("%s is not signed by the owner", e.Kind)
		}
	}
	if !kinds["invite.issue"] || !kinds["invite.revoke"] {
		t.Fatalf("the journal of the chat has %v", kinds)
	}
	consistent(t, db, "invitations")

	// The secret is in the one reply and nowhere else: not in a database, not in a log, not in the admin log.
	seed := in.Seed
	needles := [][]byte{seed, []byte(base64.RawURLEncoding.EncodeToString(seed)), []byte(base64.StdEncoding.EncodeToString(seed)), []byte(line), []byte(strings.TrimPrefix(line, "ccqc1."))}
	files := 0
	err = filepath.WalkDir(filepath.Dir(path), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		files++
		for _, n := range needles {
			if bytes.Contains(raw, n) {
				t.Errorf("the secret of an invitation is in %s", p)
			}
		}
		return nil
	})
	if err != nil || files < 3 {
		t.Fatalf("%d files read: %v", files, err)
	}
	for _, n := range needles {
		if bytes.Contains(logged.Bytes(), n) {
			t.Error("the secret of an invitation is in the broker's log")
		}
	}
}
