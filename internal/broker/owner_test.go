package broker

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/owner"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func pubOf(k ed25519.PrivateKey) string { return owner.Encode(k.Public().(ed25519.PublicKey)) }

// register makes k the owner key through the protocol, as `ccqc owner init` does.
func register(t *testing.T, b *Broker, k ed25519.PrivateKey) string {
	t.Helper()
	c := b.connect(t)
	pub := pubOf(k)
	sig := owner.Encode(ed25519.Sign(k, proto.OwnerRegisterMessage(pub)))
	if code := c.call("owner_register", proto.OwnerRegisterArgs{Pub: pub, Sig: sig}, nil); code != "" {
		t.Fatalf("owner_register: %q", code)
	}
	return pub
}

// ownerJoin joins under nick the way `ccqc owner say` does: challenge, sign, join.
func ownerJoin(t *testing.T, b *Broker, k ed25519.PrivateKey, tok, nick string, key ...string) (*client, string) {
	t.Helper()
	c := b.connect(t)
	var ch proto.Challenge
	if code := c.call("challenge", nil, &ch); code != "" {
		t.Fatalf("challenge: %q", code)
	}
	_, id, _ := proto.ParseToken(tok)
	sig := owner.Encode(ed25519.Sign(k, proto.OwnerJoinMessage(id, nick, ch.Nonce)))
	if len(key) > 0 {
		c.key = key[0]
	}
	var w proto.Welcome
	code := c.call("join", proto.JoinArgs{Token: tok, Name: nick, Key: c.key, Proto: proto.ProtoVersion, OwnerSig: sig}, &w)
	if code == "" && w.Key != "" {
		c.key = w.Key
	}
	return c, code
}

func TestOwnerJoinWithAValidSignatureIsMarked(t *testing.T) {
	be := newMemStore()
	b := newTestBroker(be)
	tok, _ := token(t)
	k := newKey(t)
	register(t, b, k)

	a := b.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)
	o, code := ownerJoin(t, b, k, tok, "owner")
	if code != "" {
		t.Fatalf("owner join: %q", code)
	}
	if m := memberInfo(t, a, "owner"); !m.Owner {
		t.Fatalf("the owner's member is not marked: %+v", m)
	}
	if m := memberInfo(t, a, "alice"); m.Owner {
		t.Fatal("an ordinary member is marked")
	}
	if _, code := o.send(proto.RoomCommon, "alice", "stop the deploy"); code != "" {
		t.Fatalf("owner send: %q", code)
	}
	var got *proto.Message
	for i := 0; i < 20 && got == nil; i++ {
		select {
		case ev := <-a.events:
			if ev.Kind == "msg" {
				got = ev.Msg
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	if got == nil || !got.Owner {
		t.Fatalf("alice received %+v; want a message marked as the owner's", got)
	}
	if _, code := a.send(proto.RoomCommon, "", "an agent speaks"); code != "" {
		t.Fatal(code)
	}
	if got := lastOwnerFlag(t, a); got {
		t.Fatal("an agent's message carries the owner mark")
	}
}

// lastOwnerFlag: the mark on the newest message of @common in the history.
func lastOwnerFlag(t *testing.T, c *client) bool {
	t.Helper()
	var h proto.HistoryResult
	c.call("history", proto.HistoryArgs{Room: proto.RoomCommon, Limit: 1}, &h)
	if len(h.Messages) != 1 {
		t.Fatalf("history: %+v", h)
	}
	return h.Messages[0].Owner
}

func TestOwnerMarkNeedsAValidSignature(t *testing.T) {
	b := newTestBroker(newMemStore())
	tok, id := token(t)
	k, other := newKey(t), newKey(t)
	register(t, b, k)

	// No challenge at all, a signature by another key, a signature for another chat or nick.
	c := b.connect(t)
	if code := c.call("join", proto.JoinArgs{Token: tok, Name: "owner", OwnerSig: owner.Encode(make([]byte, 64))}, nil); code != "bad_owner_sig" {
		t.Fatalf("no challenge: %q", code)
	}
	if _, code := ownerJoin(t, b, other, tok, "owner"); code != "bad_owner_sig" {
		t.Fatalf("another key: %q", code)
	}
	signFor := func(chatID, nick string) string {
		c := b.connect(t)
		var ch proto.Challenge
		c.call("challenge", nil, &ch)
		sig := owner.Encode(ed25519.Sign(k, proto.OwnerJoinMessage(chatID, nick, ch.Nonce)))
		return c.try(tok, "owner", sig)
	}
	otherChat := "0" + id[1:]
	if id[0] == '0' {
		otherChat = "1" + id[1:]
	}
	if got := signFor(otherChat, "owner"); got != "bad_owner_sig" {
		t.Fatalf("a signature for another chat: %q", got)
	}
	if got := signFor(id, "somebody-else"); got != "bad_owner_sig" {
		t.Fatalf("a signature for another nick: %q", got)
	}
	// A failed attempt leaves the connection outside the chat.
	if code := c.call("state", nil, nil); code != "not_joined" {
		t.Fatalf("after a refused join: %q, want not_joined", code)
	}
}

// try joins with a prepared signature on this connection and returns the error code.
func (c *client) try(tok, nick, sig string) string {
	return c.call("join", proto.JoinArgs{Token: tok, Name: nick, Proto: proto.ProtoVersion, OwnerSig: sig}, nil)
}

func TestChallengeIsOneTimeBoundToTheConnectionAndExpires(t *testing.T) {
	b := newTestBroker(newMemStore())
	tok, id := token(t)
	k := newKey(t)
	register(t, b, k)

	// Replay on another connection.
	c1 := b.connect(t)
	var ch proto.Challenge
	c1.call("challenge", nil, &ch)
	sig := owner.Encode(ed25519.Sign(k, proto.OwnerJoinMessage(id, "owner", ch.Nonce)))
	c2 := b.connect(t)
	if code := c2.try(tok, "owner", sig); code != "bad_owner_sig" {
		t.Fatalf("a nonce used on another connection: %q", code)
	}
	// The real one works once; the same signature again on that connection is spent.
	if code := c1.try(tok, "owner", sig); code != "" {
		t.Fatalf("first use: %q", code)
	}
	c1.call("leave", nil, nil)
	c3 := b.connect(t)
	if code := c3.try(tok, "owner", sig); code != "bad_owner_sig" {
		t.Fatalf("replayed signature: %q", code)
	}

	// A refused attempt spends the challenge too.
	c4 := b.connect(t)
	c4.call("challenge", nil, &ch)
	if code := c4.try(tok, "owner", owner.Encode(make([]byte, 64))); code != "bad_owner_sig" {
		t.Fatalf("garbage: %q", code)
	}
	sig = owner.Encode(ed25519.Sign(k, proto.OwnerJoinMessage(id, "owner", ch.Nonce)))
	if code := c4.try(tok, "owner", sig); code != "bad_owner_sig" {
		t.Fatalf("the challenge survived a failed attempt: %q", code)
	}

	// Thirty seconds and no more.
	c5 := b.connect(t)
	c5.call("challenge", nil, &ch)
	b.mu.Lock()
	for c := range b.conns {
		if c.nonce == ch.Nonce {
			c.nonceAt = time.Now().Add(-2 * challengeTTL)
		}
	}
	b.mu.Unlock()
	sig = owner.Encode(ed25519.Sign(k, proto.OwnerJoinMessage(id, "owner", ch.Nonce)))
	if code := c5.try(tok, "owner", sig); code != "bad_owner_sig" {
		t.Fatalf("an expired challenge: %q", code)
	}
}

func TestClientsCannotClaimTheMarkThemselves(t *testing.T) {
	be := newMemStore()
	b := newTestBroker(be)
	tok, _ := token(t)
	register(t, b, newKey(t))

	c := b.connect(t)
	var w proto.Welcome
	code := c.call("join", map[string]any{"token": tok, "name": "mallory", "owner": true, "owner_sig": "", "proto": 3}, &w)
	if code != "" {
		t.Fatalf("join: %q", code)
	}
	if code := c.call("send", map[string]any{"room": "common", "text": "I am the owner", "owner": true}, nil); code != "" {
		t.Fatalf("send: %q", code)
	}
	if lastOwnerFlag(t, c) {
		t.Fatal("a client set the owner mark with a field of its own")
	}
	if memberInfo(t, c, "mallory").Owner {
		t.Fatal("a client became the owner by saying so")
	}
	if _, code := ownerJoinNoOwner(t, tok); code != "bad_owner_sig" {
		t.Fatalf("a signature when no owner is registered: %q", code)
	}
}

// ownerJoinNoOwner: a broker with no owner refuses any owner signature.
func ownerJoinNoOwner(t *testing.T, tok string) (*client, string) {
	b := newTestBroker(newMemStore())
	return ownerJoin(t, b, newKey(t), tok, "owner")
}

func TestRegistrationRotationAndTheReservedNick(t *testing.T) {
	be := newMemStore()
	b := newTestBroker(be)
	tok, _ := token(t)
	k1, k2 := newKey(t), newKey(t)

	register(t, b, k1)
	// A second registration is refused, even with a valid proof of the new key.
	c := b.connect(t)
	pub2 := pubOf(k2)
	if code := c.call("owner_register", proto.OwnerRegisterArgs{Pub: pub2, Sig: owner.Encode(ed25519.Sign(k2, proto.OwnerRegisterMessage(pub2)))}, nil); code != "owner_exists" {
		t.Fatalf("second registration: %q", code)
	}
	// A registration not signed with the key it names is refused.
	if code := c.call("owner_register", proto.OwnerRegisterArgs{Pub: pub2, Sig: owner.Encode(make([]byte, 64))}, nil); code != "bad_owner_sig" {
		t.Fatalf("forged proof: %q", code)
	}

	// The nick is reserved.
	u := b.connect(t)
	if _, code := u.join(tok, "owner", proto.ProtoVersion); code != "reserved" {
		t.Fatalf("unsigned join as owner: %q, want reserved", code)
	}

	// Rotation: signed by the old key and the new one.
	rot := func(oldK, newK ed25519.PrivateKey, signedBy ed25519.PrivateKey) string {
		msg := proto.OwnerRotateMessage(pubOf(oldK), pubOf(newK))
		return c.call("owner_rotate", proto.OwnerRotateArgs{
			Pub:    pubOf(newK),
			OldSig: owner.Encode(ed25519.Sign(signedBy, msg)),
			NewSig: owner.Encode(ed25519.Sign(newK, msg)),
		}, nil)
	}
	if code := rot(k1, k2, k2); code != "bad_owner_sig" {
		t.Fatalf("rotation signed by the new key only: %q", code)
	}
	if code := rot(k1, k2, k1); code != "" {
		t.Fatalf("rotation: %q", code)
	}
	if _, code := ownerJoin(t, b, k1, tok, "owner"); code != "bad_owner_sig" {
		t.Fatalf("the old key after a rotation: %q", code)
	}
	if _, code := ownerJoin(t, b, k2, tok, "owner"); code != "" {
		t.Fatalf("the new key: %q", code)
	}
	var info proto.OwnerInfo
	c.call("owner_info", nil, &info)
	if !info.Registered || info.Fingerprint != owner.Fingerprint(k2.Public().(ed25519.PublicKey)) {
		t.Fatalf("owner info after rotation: %+v", info)
	}
}

func TestOwnerAndTheMarkSurviveABrokerRestart(t *testing.T) {
	be := newMemStore()
	b1 := newTestBroker(be)
	tok, _ := token(t)
	k := newKey(t)
	register(t, b1, k)
	o, code := ownerJoin(t, b1, k, tok, "owner")
	if code != "" {
		t.Fatal(code)
	}
	if _, code := o.send(proto.RoomCommon, "", "from the owner"); code != "" {
		t.Fatal(code)
	}

	b2 := newTestBroker(be)
	if err := b2.loadOwner(); err != nil {
		t.Fatal(err)
	}
	a := b2.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)
	if !lastOwnerFlag(t, a) {
		t.Fatal("the owner mark did not survive the restart")
	}
	if _, code := ownerJoin(t, b2, k, tok, "owner", o.key); code != "" {
		t.Fatalf("the owner key after a restart: %q", code)
	}
}

func TestRegisteringOrReplacingTheOwnerKeyIsAnnouncedToEveryPresentMember(t *testing.T) {
	b := newTestBroker(newMemStore())
	tok, _ := token(t)
	a := b.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)
	a.msgs()

	k1, k2 := newKey(t), newKey(t)
	register(t, b, k1)
	got := noticeEvents(a)
	if len(got) != 1 || !got[0].Wake || got[0].Text != "owner key registered: "+owner.Fingerprint(k1.Public().(ed25519.PublicKey)) {
		t.Fatalf("after the registration: %+v", got)
	}

	c := b.connect(t)
	msg := proto.OwnerRotateMessage(pubOf(k1), pubOf(k2))
	if code := c.call("owner_rotate", proto.OwnerRotateArgs{Pub: pubOf(k2),
		OldSig: owner.Encode(ed25519.Sign(k1, msg)), NewSig: owner.Encode(ed25519.Sign(k2, msg))}, nil); code != "" {
		t.Fatalf("rotate: %q", code)
	}
	got = noticeEvents(a)
	if len(got) != 1 || !strings.HasPrefix(got[0].Text, "owner key replaced: SHA256:") {
		t.Fatalf("after the rotation: %+v", got)
	}
}

func noticeEvents(c *client) []proto.Event {
	var out []proto.Event
	for {
		select {
		case ev := <-c.events:
			if ev.Kind == "notice" {
				out = append(out, ev)
			}
		case <-time.After(150 * time.Millisecond):
			return out
		}
	}
}

func TestBrokerInfoShowsTheOwnerKeyAndTheNode(t *testing.T) {
	b := newTestBroker(newMemStore())
	b.mu.Lock()
	info := b.info()
	b.mu.Unlock()
	if info.Owner != "" || info.Node != b.origin || len(info.Node) != 32 {
		t.Fatalf("before an owner exists: %+v", info)
	}
	k := newKey(t)
	register(t, b, k)
	b.mu.Lock()
	info = b.info()
	b.mu.Unlock()
	if info.Owner != owner.Fingerprint(k.Public().(ed25519.PublicKey)) || info.OwnerSince == "" {
		t.Fatalf("after: %+v", info)
	}
}
