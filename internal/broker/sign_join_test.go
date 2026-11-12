package broker

import (
	"crypto/ed25519"
	"testing"

	cl "github.com/PHPCraftdream/cc-quant-chat/internal/client"
	"github.com/PHPCraftdream/cc-quant-chat/internal/owner"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// leaveChat takes the nick out of the chat (the reply comes after it is done), so a join under
// it is not refused as "online".
func (p *signedPeer) leaveChat() {
	p.t.Helper()
	if code := p.call("leave", nil, nil); code != "" {
		p.t.Fatalf("leave: %q", code)
	}
}

func TestANickIsRegisteredWithItsKeyAndOnlyItsKeyJoinsAgain(t *testing.T) {
	be := newMemStore()
	b := newTestBroker(be)
	tok, id := token(t)
	k := newKey(t)
	a := b.mustJoin(t, tok, "alice", k)
	var nk *nick
	b.inChat(id, func(ch *chat) { nk = ch.nicks["alice"] })
	if nk == nil || nk.pub != pubOf(k) {
		t.Fatalf("stored nick: %+v", nk)
	}
	a.leaveChat()

	// The same key joins again, also on a restarted broker (the key came back from the store).
	b2 := newTestBroker(be)
	if _, err := b2.keyedJoin(t, tok, "alice", k); err != nil {
		t.Fatalf("rejoin with the key: %v", err)
	}
	// Another key cannot take the nick over (it cannot prove the auth key), and the secret alone is not a key.
	b3 := newTestBroker(be)
	if _, err := b3.keyedJoin(t, tok, "alice", newKey(t)); codeOf(err) != "unlock_denied" {
		t.Fatalf("another key: %v", err)
	}
}

func TestJoinNeedsAFreshChallengeSignedByTheKeyItPresents(t *testing.T) {
	b := newTestBroker(newMemStore())
	tok, id := token(t)
	k := newKey(t)
	pub := pubOf(k)
	join := func(c *client, sig string) string {
		return c.call("join", proto.JoinArgs{Chat: tok, Name: "alice", Proto: proto.ProtoVersion, Pub: pub, NickSig: sig}, nil)
	}
	sign := func(key ed25519.PrivateKey, nonce string) string {
		return owner.Encode(ed25519.Sign(key, proto.NickJoinMessage(id, "alice", pub, nonce)))
	}

	b.seedProtected(t, tok, "alice", k)
	b.seedProtected(t, tok, "bob", k)
	c := b.connect(t)
	if code := unlockAs(t, c, id, "alice", k); code != "" {
		t.Fatalf("unlock: %q", code)
	}
	if code := join(c, sign(k, "nonce")); code != "bad_nick_sig" { // no challenge was asked for
		t.Fatalf("no challenge: %q", code)
	}
	var ch proto.Challenge
	c.call("challenge", nil, &ch)
	if code := join(c, sign(newKey(t), ch.Nonce)); code != "bad_nick_sig" { // signed by a key that is not the one presented
		t.Fatalf("someone else's signature: %q", code)
	}
	if code := join(c, sign(k, ch.Nonce)); code != "bad_nick_sig" { // the nonce was spent by the failed attempt
		t.Fatalf("a spent challenge: %q", code)
	}
	c.call("challenge", nil, &ch)
	if code := join(c, sign(k, ch.Nonce)); code != "" {
		t.Fatalf("the honest join: %q", code)
	}

	// A challenge belongs to its connection.
	c1, c2 := b.connect(t), b.connect(t)
	if code := unlockAs(t, c2, id, "bob", k); code != "" {
		t.Fatalf("unlock: %q", code)
	}
	c1.call("challenge", nil, &ch)
	if code := c2.call("join", proto.JoinArgs{Chat: tok, Name: "bob", Proto: proto.ProtoVersion, Pub: pub,
		NickSig: owner.Encode(ed25519.Sign(k, proto.NickJoinMessage(id, "bob", pub, ch.Nonce)))}, nil); code != "bad_nick_sig" {
		t.Fatalf("another connection's challenge: %q", code)
	}
}

// Old clients work against this broker, new clients against an old one: the pieces of the
// compatibility the levels promise.
func TestOwnerSignsWithTheOwnerKey(t *testing.T) {
	be := newMemStore()
	b := newTestBroker(be)
	tok, id := token(t)
	k := newKey(t)
	register(t, b, k)
	b.makeChat(t, tok)

	c := b.connect(t)
	var ch proto.Challenge
	c.call("challenge", nil, &ch)
	sig := owner.Encode(ed25519.Sign(k, proto.OwnerJoinMessage(id, "owner", ch.Nonce)))
	var w proto.Welcome
	if code := c.call("join", proto.JoinArgs{Chat: tok, Name: "owner", Proto: proto.ProtoVersion, OwnerSig: sig}, &w); code != "" {
		t.Fatalf("owner join: %q", code)
	}
	if got := codeOf(c.callE("send", proto.SendArgs{Room: "common", Text: "unsigned"}, nil)); got != "unsigned" {
		t.Fatalf("an owner must sign: %q", got)
	}
	sg := &cl.Signer{Priv: k, ChatID: id, Origin: w.Origin}
	if err := sg.Write(c.callE, "send", "msg", &proto.SendArgs{Room: "common", Text: "by the owner"}, nil); err != nil {
		t.Fatal(err)
	}
	recs := msgRecords(be, id)
	if len(recs) != 1 {
		t.Fatalf("%d messages", len(recs))
	}
	r := recs[0]
	if !r.Msg.Owner || r.AuthorPub != pubOf(k) || !verifies(id, b.origin, "msg", r.AuthorPub, r.Sig, r.Payload, 1) {
		t.Fatalf("the owner's message: %+v", r)
	}
}
