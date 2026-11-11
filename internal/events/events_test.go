package events

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

func keyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func sample() Event {
	return Event{ChatID: "chat", Origin: "node", Seq: 3, Prev: "p", Lamport: 7, Kind: "msg",
		Payload: `{"room":"common","text":"hi"}`, Assigned: `{"id":1}`, TS: "2026-01-01T00:00:00Z"}
}

func TestNodeSignatureCoversEveryField(t *testing.T) {
	pub, priv := keyPair(t)
	e := sample()
	e.Sign(priv)
	if !e.VerifyNode(pub) {
		t.Fatal("a fresh signature must verify")
	}
	if len(e.ID()) != 64 {
		t.Fatalf("ID %q", e.ID())
	}
	for name, mut := range map[string]func(*Event){
		"chat": func(e *Event) { e.ChatID = "other" }, "origin": func(e *Event) { e.Origin = "other" },
		"seq": func(e *Event) { e.Seq++ }, "prev": func(e *Event) { e.Prev = "q" }, "lamport": func(e *Event) { e.Lamport++ },
		"kind": func(e *Event) { e.Kind = "nick.pin" }, "payload": func(e *Event) { e.Payload += " " },
		"author": func(e *Event) { e.AuthorPub = "a" }, "sig": func(e *Event) { e.Sig = "s" },
		"assigned": func(e *Event) { e.Assigned = `{"id":2}` }, "ts": func(e *Event) { e.TS = "2026-01-01T00:00:01Z" },
	} {
		c := e
		mut(&c)
		if c.VerifyNode(pub) {
			t.Errorf("the node's signature survives a change of %s", name)
		}
		if c.ID() == e.ID() {
			t.Errorf("the ID survives a change of %s", name)
		}
	}
	other, _ := keyPair(t)
	if e.VerifyNode(other) || (Event{}).VerifyNode(pub) {
		t.Fatal("another key, or no signature, must not verify")
	}
}

// Fields are length-prefixed: moving a byte between neighbours changes the hash.
func TestFieldsCannotBeShiftedBetweenNeighbours(t *testing.T) {
	a, b := sample(), sample()
	a.Kind, a.Payload = "ms", "g"+a.Payload
	if string(a.Rest()) == string(b.Rest()) {
		t.Fatal("kind and payload run together")
	}
}

func TestAuthorSignatureIsCheckedOverTheChatOriginKindPayloadAndClock(t *testing.T) {
	pub, priv := keyPair(t)
	e := sample()
	if ok, signed := e.VerifyAuthor(); !ok || signed {
		t.Fatalf("an unsigned event: ok %v signed %v", ok, signed)
	}
	e.AuthorPub = base64.StdEncoding.EncodeToString(pub)
	e.Sig = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, proto.EventMessage(e.ChatID, e.Origin, e.Kind, []byte(e.Payload), e.Lamport)))
	if ok, signed := e.VerifyAuthor(); !ok || !signed {
		t.Fatalf("a signed event: ok %v signed %v", ok, signed)
	}
	for name, mut := range map[string]func(*Event){
		"chat": func(e *Event) { e.ChatID = "x" }, "origin": func(e *Event) { e.Origin = "x" }, "kind": func(e *Event) { e.Kind = "x" },
		"payload": func(e *Event) { e.Payload = "{}" }, "lamport": func(e *Event) { e.Lamport++ },
		"key": func(e *Event) { p, _ := keyPair(t); e.AuthorPub = base64.StdEncoding.EncodeToString(p) },
	} {
		c := e
		mut(&c)
		if ok, _ := c.VerifyAuthor(); ok {
			t.Errorf("the author's signature survives a change of %s", name)
		}
	}
	// What the node adds (assigned, time) is not the author's to sign.
	c := e
	c.Assigned, c.TS = `{"id":9}`, "2027-01-01T00:00:00Z"
	if ok, _ := c.VerifyAuthor(); !ok {
		t.Error("the author's signature must not depend on what the node assigned")
	}
	if ok, _ := (Event{AuthorPub: "!!", Sig: "!!"}).VerifyAuthor(); ok {
		t.Error("garbage must not verify")
	}
}
