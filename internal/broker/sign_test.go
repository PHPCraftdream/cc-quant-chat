package broker

import (
	"crypto/ed25519"
	"strings"
	"sync"
	"testing"

	cl "github.com/PHPCraftdream/cc-quant-chat/internal/client"
	"github.com/PHPCraftdream/cc-quant-chat/internal/owner"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// signedPeer is a proto 4 client: it joins with its nick key and signs its writes.
type signedPeer struct {
	*client
	priv ed25519.PrivateKey
	sg   *cl.Signer
	w    proto.Welcome
}

// keyedJoin joins the way the MCP client does: challenge, signature, join. secret is the nick's
// old secret, presented to move a legacy nick to the key.
func (b *Broker) keyedJoin(t *testing.T, tok, nick string, priv ed25519.PrivateKey, secret string) (*signedPeer, error) {
	t.Helper()
	c := b.connect(t)
	_, id, _ := proto.ParseToken(tok)
	p := &signedPeer{client: c, priv: priv}
	var err error
	p.sg, err = cl.JoinSigned(c.callE, proto.JoinArgs{Token: tok, Name: nick, Key: secret}, priv, id, &p.w)
	return p, err
}

func (b *Broker) mustJoin(t *testing.T, tok, nick string, priv ed25519.PrivateKey) *signedPeer {
	t.Helper()
	p, err := b.keyedJoin(t, tok, nick, priv, "")
	if err != nil || p.sg == nil {
		t.Fatalf("join @%s: %v (signer %v)", nick, err, p != nil && p.sg != nil)
	}
	return p
}

// do signs a write at the chat's current clock and sends it; it returns the clock value used.
func (p *signedPeer) do(op, kind string, args proto.Stamper, out any) (int64, error) {
	p.t.Helper()
	var r proto.ClockReply
	if err := p.callE("clock", nil, &r); err != nil {
		return 0, err
	}
	if err := p.sg.Sign(kind, args, r.Next); err != nil {
		p.t.Fatal(err)
	}
	return r.Next, p.callE(op, args, out)
}

func codeOf(err error) string {
	if pe, ok := err.(*proto.Error); ok {
		return pe.Code
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

// verifies reports whether sig (base64) by pub (base64) is valid for the event.
func verifies(chatID, origin, kind, pub, sig, payload string, lamport int64) bool {
	pk, err1 := owner.Decode(pub)
	sg, err2 := owner.Decode(sig)
	if err1 != nil || err2 != nil || len(pk) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(pk, proto.EventMessage(chatID, origin, kind, []byte(payload), lamport), sg)
}

// msgRecords: the stored message records of a memory store.
func msgRecords(be *memStore, id string) []store.Record {
	be.mu.Lock()
	defer be.mu.Unlock()
	var out []store.Record
	for _, r := range be.recs[id] {
		if r.T == "msg" {
			out = append(out, r)
		}
	}
	return out
}

func TestClockStartsAtOneAndAdvancesWithEveryWrite(t *testing.T) {
	b := newTestBroker(newMemStore())
	tok, _ := token(t)
	a := b.mustJoin(t, tok, "alice", newKey(t))
	var r proto.ClockReply
	if a.call("clock", nil, &r); r.Next != 1 {
		t.Fatalf("fresh chat: next = %d, want 1", r.Next)
	}
	if _, err := a.do("send", "msg", &proto.SendArgs{Room: "common", Text: "one"}, nil); err != nil {
		t.Fatal(err)
	}
	if a.call("clock", nil, &r); r.Next != 2 {
		t.Fatalf("after a write: next = %d, want 2", r.Next)
	}
}

func TestSignedMessageIsStoredWithTheAuthorsKeyAndSignature(t *testing.T) {
	be := newMemStore()
	b := newTestBroker(be)
	tok, id := token(t)
	k := newKey(t)
	a := b.mustJoin(t, tok, "alice", k)
	if a.w.Origin != b.origin || a.w.Proto != proto.ProtoVersion {
		t.Fatalf("welcome: origin %q proto %d", a.w.Origin, a.w.Proto)
	}
	lam, err := a.do("send", "msg", &proto.SendArgs{Room: "common", Text: "hello"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	recs := msgRecords(be, id)
	if len(recs) != 1 {
		t.Fatalf("%d messages stored", len(recs))
	}
	r := recs[0]
	if r.AuthorPub != pubOf(k) || !verifies(id, b.origin, "msg", r.AuthorPub, r.Sig, r.Payload, lam) {
		t.Fatalf("the stored signature does not verify: %+v", r)
	}
	if !strings.Contains(r.Payload, `"text":"hello"`) {
		t.Fatalf("payload %q", r.Payload)
	}
}

func TestTamperedOrMisaddressedEventsAreRefusedAndNotStored(t *testing.T) {
	be := newMemStore()
	b := newTestBroker(be)
	tok, id := token(t)
	a := b.mustJoin(t, tok, "alice", newKey(t))
	var next proto.ClockReply
	a.call("clock", nil, &next)

	send := func(mut func(*proto.SendArgs, *cl.Signer, *int64)) error {
		args := &proto.SendArgs{Room: "common", Text: "pay"}
		sg := *a.sg
		lam := next.Next
		mut(args, &sg, &lam)
		if err := sg.Sign("msg", args, lam); err != nil {
			t.Fatal(err)
		}
		return a.callE("send", args, nil)
	}
	cases := []struct {
		name, want string
		mut        func(*proto.SendArgs, *cl.Signer, *int64)
	}{
		{"another key", "bad_signature", func(_ *proto.SendArgs, s *cl.Signer, _ *int64) { s.Priv = newKey(t) }},
		{"another chat", "bad_signature", func(_ *proto.SendArgs, s *cl.Signer, _ *int64) { s.ChatID = "00" + s.ChatID }},
		{"another node", "wrong_origin", func(_ *proto.SendArgs, s *cl.Signer, _ *int64) { s.Origin = "feedfacefeedfacefeedfacefeedface" }},
		{"a clock value that is not next", "stale_clock", func(_ *proto.SendArgs, _ *cl.Signer, l *int64) { *l += 3 }},
	}
	for _, c := range cases {
		if got := codeOf(send(c.mut)); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}

	// Changed after signing: the text, the clock value, the key's owner.
	for name, mut := range map[string]func(*proto.SendArgs){
		"text":    func(a *proto.SendArgs) { a.Text = "pay twice" },
		"room":    func(a *proto.SendArgs) { a.Room = "ops" },
		"lamport": func(a *proto.SendArgs) { a.Lamport++ },
		"origin":  func(a *proto.SendArgs) { a.Origin = "" },
		"to":      func(a *proto.SendArgs) { a.To = "alice" },
		"sig":     func(a *proto.SendArgs) { a.Sig = owner.Encode(make([]byte, ed25519.SignatureSize)) },
	} {
		args := &proto.SendArgs{Room: "common", Text: "pay"}
		if err := a.sg.Sign("msg", args, next.Next); err != nil {
			t.Fatal(err)
		}
		mut(args)
		got := codeOf(a.callE("send", args, nil))
		if got != "bad_signature" && got != "wrong_origin" && got != "stale_clock" {
			t.Errorf("tampered %s: %q", name, got)
		}
	}
	// A client at proto 4 that has a key must sign.
	if got := codeOf(a.callE("send", proto.SendArgs{Room: "common", Text: "plain"}, nil)); got != "unsigned" {
		t.Errorf("unsigned send: %q", got)
	}
	if n := len(msgRecords(be, id)); n != 0 {
		t.Fatalf("%d refused events were stored", n)
	}
	var clock int64
	b.inChat(id, func(ch *chat) { clock = ch.memClock })
	if clock != 0 {
		t.Fatalf("a refused event moved the clock to %d", clock)
	}
	if _, err := a.do("send", "msg", &proto.SendArgs{Room: "common", Text: "pay"}, nil); err != nil {
		t.Fatalf("the honest one: %v", err)
	}
}

func TestStaleClockCarriesTheCurrentValue(t *testing.T) {
	b := newTestBroker(newMemStore())
	tok, _ := token(t)
	a := b.mustJoin(t, tok, "alice", newKey(t))
	args := &proto.SendArgs{Room: "common", Text: "late"}
	if err := a.sg.Sign("msg", args, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := a.do("send", "msg", &proto.SendArgs{Room: "common", Text: "first"}, nil); err != nil {
		t.Fatal(err)
	}
	err := a.callE("send", args, nil) // signed for 1, the clock is at 1: next is 2
	next, ok := proto.StaleClock(err)
	if !ok || next != 2 {
		t.Fatalf("err %v: next %d ok %v, want stale_clock with 2", err, next, ok)
	}
}

func TestThreeWritersAtOnceAllLandAndTheClockGrowsStrictly(t *testing.T) {
	b, path := boardBroker(t)
	tok, id := token(t)
	names := []string{"alice", "bob", "carol"}
	var peers []*signedPeer
	for _, n := range names {
		peers = append(peers, b.mustJoin(t, tok, n, newKey(t)))
	}
	var wg sync.WaitGroup
	errs := make([]error, len(peers))
	for i, p := range peers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			args := &proto.SendArgs{Room: "common", Text: "from " + names[i]}
			errs[i] = p.sg.Write(p.callE, "send", "msg", args, nil)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("%s: %v", names[i], err)
		}
	}
	db := reopen(t, path)
	if n, err := db.Lamport(id); err != nil || n != 3 {
		t.Fatalf("clock = %d, %v; want 3", n, err)
	}
	recs, err := db.Load(id, "team")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]string{}
	for _, r := range recs {
		if r.T != "msg" {
			continue
		}
		matched := int64(0)
		for l := int64(1); l <= 3; l++ {
			if verifies(id, b.origin, "msg", r.AuthorPub, r.Sig, r.Payload, l) {
				matched = l
			}
		}
		if matched == 0 || seen[matched] != "" {
			t.Fatalf("message %q: clock value %d (already used by %q)", r.Msg.Text, matched, seen[matched])
		}
		seen[matched] = r.Msg.Text
	}
	if len(seen) != 3 {
		t.Fatalf("%d of 3 messages stored with distinct clock values: %v", len(seen), seen)
	}
}
