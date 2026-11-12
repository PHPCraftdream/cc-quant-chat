package broker

import (
	"crypto/ed25519"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/keys"
	"github.com/PHPCraftdream/cc-quant-chat/internal/owner"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// plainNick registers a nick with a plain key by an invitation (what every nick was before protected
// keys) and leaves the chat; it returns the private key, as nick-keys.json would hold it.
func (ic *invitedChat) plainNick(t *testing.T, nick string) ed25519.PrivateKey {
	t.Helper()
	in := ic.issue(t, nick)
	k := newKey(t)
	c := ic.b.connect(t)
	if _, code := ic.join(t, c, nick, k, &in); code != "" {
		t.Fatalf("plain nick: %q", code)
	}
	if code := c.call("leave", nil, nil); code != "" {
		t.Fatalf("leave: %q", code)
	}
	return k
}

// move is the join of a client that moves its nick to a protected key: key_prepare, the keys from A and
// B, the proof of the old key over the same challenge.
func (ic *invitedChat) move(t *testing.T, c *client, nick string, a []byte, old ed25519.PrivateKey) (proto.Welcome, string) {
	t.Helper()
	var rep proto.KeyPrepareReply
	if code := c.call("key_prepare", proto.KeyPrepareArgs{Chat: ic.chat, Nick: nick}, &rep); code != "" {
		t.Fatalf("key_prepare: %q", code)
	}
	sign, _ := keys.SignKey(a, b64(t, rep.B), ic.chat, nick)
	auth, _ := keys.AuthKey(a, ic.chat, nick)
	var ch proto.Challenge
	c.call("challenge", nil, &ch)
	pub := owner.Encode(sign.Public().(ed25519.PublicKey))
	authPub := owner.Encode(auth.Public().(ed25519.PublicKey))
	en := &proto.JoinEnroll{AuthPub: authPub, SID: ic.sid}
	if old != nil {
		en.OldSig = owner.Encode(ed25519.Sign(old, proto.KeyMoveMessage(ic.chat, nick, pub, authPub, ch.Nonce)))
	}
	var w proto.Welcome
	return w, c.call("join", proto.JoinArgs{Chat: ic.chat, Name: nick, Pub: pub, Proto: proto.KeysProto,
		NickSig: owner.Encode(ed25519.Sign(sign, proto.NickJoinMessage(ic.chat, nick, pub, ch.Nonce))), Enroll: en}, &w)
}

func TestANickMovesToAProtectedKeyWithTheProofOfItsOldKeyAndTheOldKeyAndItsCopiesAreDeadAfterwards(t *testing.T) {
	ic := newInvitedChat(t)
	old := ic.plainNick(t, "bob")
	watch := ic.watcher(t)

	// Before the move the key (and a copy of it, a backup) joins as ever.
	copyC := ic.b.connect(t)
	if w, code := ic.join(t, copyC, "bob", old, nil); code != "" || w.State.Me != "bob" {
		t.Fatalf("the old key before the move: %q", code)
	}
	copyC.call("leave", nil, nil)

	a := secretOf(t)
	c := ic.b.connect(t)
	if w, code := ic.move(t, c, "bob", a, old); code != "" || w.State.Me != "bob" {
		t.Fatalf("the move: %q", code)
	}
	c.call("leave", nil, nil)
	heard := watch.heard(t, "moved to a protected key", 3*time.Second)
	if !strings.Contains(heard, "@bob") || !strings.Contains(heard, "old key") {
		t.Fatalf("the chat was not told: %q", heard)
	}
	if l := lastOf(adminLog(t, ic), "key.move"); !strings.Contains(l, `"via":"old key"`) || !strings.Contains(l, ": ok") {
		t.Fatalf("log: %q", l)
	}
	row, _ := ic.db.ActiveNickSecret(ic.chat, "bob")
	if row == nil || row.SignPub == owner.Encode(old.Public().(ed25519.PublicKey)) {
		t.Fatalf("the secret of the nick: %+v", row)
	}
	// The old key and every copy of it: no way in, at the old level or at this one.
	for _, level := range []int{proto.InviteProto, proto.KeysProto} {
		cl := ic.b.connect(t)
		var ch proto.Challenge
		cl.call("challenge", nil, &ch)
		pub := owner.Encode(old.Public().(ed25519.PublicKey))
		if code := cl.call("join", proto.JoinArgs{Chat: ic.chat, Name: "bob", Pub: pub, Proto: level,
			NickSig: owner.Encode(ed25519.Sign(old, proto.NickJoinMessage(ic.chat, "bob", pub, ch.Nonce)))}, nil); code == "" {
			t.Fatalf("the old key joined after the move (level %d)", level)
		}
	}
	// And the one who moved gets in through the proof of A.
	if _, w, code := ic.login(t, "bob", a); code != "" || w.State.Me != "bob" {
		t.Fatalf("login after the move: %q", code)
	}
}

func TestOfTwoWithTheSameOldKeyOnlyTheFirstMovesAndTheLoserSeesARefusal(t *testing.T) {
	ic := newInvitedChat(t)
	old := ic.plainNick(t, "bob")
	watch := ic.watcher(t)
	thiefA, ownerA := secretOf(t), secretOf(t)
	first := ic.b.connect(t)
	if _, code := ic.move(t, first, "bob", thiefA, old); code != "" {
		t.Fatalf("the first: %q", code)
	}
	first.call("leave", nil, nil)
	// The real client's proof (the same old key) is no longer the nick's key.
	if _, code := ic.move(t, ic.b.connect(t), "bob", ownerA, old); code == "" {
		t.Fatal("the second move with the same old key succeeded")
	}
	if heard := watch.heard(t, "moved to a protected key", 3*time.Second); heard == "" {
		t.Fatal("the chat was not told of the first move")
	}
	// The real client's A does not open the nick; nothing is left half done.
	if _, _, code := ic.login(t, "bob", ownerA); code == "" {
		t.Fatal("the loser's A opens the nick")
	}
	if pend, _ := ic.db.PendingNickSecrets(); len(pend) != 0 {
		t.Fatalf("a pending row was left: %+v", pend)
	}
}

func TestAMoveWithoutTheOldKeysProofOrWithAnotherKeyOrOnAnOnlineNickIsRefusedAndChangesNothing(t *testing.T) {
	ic := newInvitedChat(t)
	old := ic.plainNick(t, "bob")
	a := secretOf(t)
	for name, key := range map[string]ed25519.PrivateKey{"no proof": nil, "another key": newKey(t)} {
		if _, code := ic.move(t, ic.b.connect(t), "bob", a, key); code != "name_taken" {
			t.Fatalf("%s: %q", name, code)
		}
	}
	if row, _ := ic.db.ActiveNickSecret(ic.chat, "bob"); row != nil {
		t.Fatalf("a refused move left a secret: %+v", row)
	}
	if pend, _ := ic.db.PendingNickSecrets(); len(pend) != 0 {
		t.Fatalf("a pending row: %+v", pend)
	}
	// A new nick is made by an invitation only, not by a move.
	if _, code := ic.move(t, ic.b.connect(t), "stranger", a, old); code != "invite_required" {
		t.Fatalf("a nick that does not exist: %q", code)
	}
	// Without the B of key_prepare there is nothing to enroll.
	bare := ic.b.connect(t)
	sign, _ := keys.SignKey(a, secretOf(t), ic.chat, "bob")
	auth, _ := keys.AuthKey(a, ic.chat, "bob")
	var ch proto.Challenge
	bare.call("challenge", nil, &ch)
	pub := owner.Encode(sign.Public().(ed25519.PublicKey))
	authPub := owner.Encode(auth.Public().(ed25519.PublicKey))
	if code := bare.call("join", proto.JoinArgs{Chat: ic.chat, Name: "bob", Pub: pub, Proto: proto.KeysProto,
		NickSig: owner.Encode(ed25519.Sign(sign, proto.NickJoinMessage(ic.chat, "bob", pub, ch.Nonce))),
		Enroll:  &proto.JoinEnroll{AuthPub: authPub, OldSig: owner.Encode(ed25519.Sign(old, proto.KeyMoveMessage(ic.chat, "bob", pub, authPub, ch.Nonce)))}}, nil); code != "key_not_prepared" {
		t.Fatalf("without key_prepare: %q", code)
	}
	// A nick that is present is not moved from under its session.
	in := ic.b.connect(t)
	if _, code := ic.join(t, in, "bob", old, nil); code != "" {
		t.Fatalf("join: %q", code)
	}
	if _, code := ic.move(t, ic.b.connect(t), "bob", a, old); code != "name_online" {
		t.Fatalf("an online nick: %q", code)
	}
}

func TestANickOfTheTimeOfSecretsMovesWithItsOldSecret(t *testing.T) {
	b, db, _, _ := journalBroker(t)
	tok, id := token(t)
	legacy := b.connect(t)
	w, code := legacy.join(tok, "bob", proto.LegacyProto)
	if code != "" || w.Key == "" {
		t.Fatalf("legacy join: %q", code)
	}
	secret := w.Key
	legacy.call("leave", nil, nil)

	a := secretOf(t)
	attempt := func(c *client, key string) (string, string) {
		var rep proto.KeyPrepareReply
		if code := c.call("key_prepare", proto.KeyPrepareArgs{Chat: id, Nick: "bob"}, &rep); code != "" {
			t.Fatal(code)
		}
		sign, _ := keys.SignKey(a, b64(t, rep.B), id, "bob")
		auth, _ := keys.AuthKey(a, id, "bob")
		var ch proto.Challenge
		c.call("challenge", nil, &ch)
		pub := owner.Encode(sign.Public().(ed25519.PublicKey))
		var joined proto.Welcome
		code := c.call("join", proto.JoinArgs{Token: tok, Name: "bob", Key: key, Pub: pub, Proto: proto.KeysProto,
			NickSig: owner.Encode(ed25519.Sign(sign, proto.NickJoinMessage(id, "bob", pub, ch.Nonce))),
			Enroll:  &proto.JoinEnroll{AuthPub: owner.Encode(auth.Public().(ed25519.PublicKey))}}, &joined)
		return pub, code
	}
	if _, code := attempt(b.connect(t), "not the secret"); code != "name_taken" {
		t.Fatalf("a wrong secret: %q", code)
	}
	pub, code := attempt(b.connect(t), secret)
	if code != "" {
		t.Fatalf("the move by the old secret: %q", code)
	}
	if row, _ := db.ActiveNickSecret(id, "bob"); row == nil || row.SignPub != pub {
		t.Fatalf("the secret of the nick: %+v", row)
	}
	// The old secret alone opens nothing now.
	if _, code := b.connect(t).join(tok, "bob", proto.LegacyProto); code == "" {
		t.Fatal("the old secret still opens the nick")
	}
}
