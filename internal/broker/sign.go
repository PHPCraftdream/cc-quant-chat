package broker

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/owner"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// Authors sign their events. A nick is an Ed25519 key (proven at join by signing a challenge);
// a message, a board change (with its journal entry, comment or revert) and a document version
// are signed by their author over the chat, this broker's origin, the kind, the canonical JSON of
// the request and the chat's logical clock. The broker checks the signature before it writes and
// keeps it with the row; an event that does not verify is not stored.

// takeNonce spends the challenge of the connection: its nonce, and whether it is still valid.
func (c *conn) takeNonce() (string, bool) {
	nonce, at := c.nonce, c.nonceAt
	c.nonce = ""
	return nonce, nonce != "" && time.Since(at) <= challengeTTL
}

// verifyNick checks that a join holds the private key of the public key it presents.
func (b *Broker) verifyNick(c *conn, chatID string, a proto.JoinArgs) (ed25519.PublicKey, *proto.Error) {
	pub, e := decodeKey(a.Pub)
	if e != nil {
		return nil, e
	}
	bad := func(why string) *proto.Error { return proto.Errf("bad_nick_sig", "%s", why) }
	nonce, ok := c.takeNonce()
	if !ok {
		return nil, bad("no valid challenge on this connection (ask for a new one)")
	}
	sig := decodeSig(a.NickSig)
	if sig == nil || !ed25519.Verify(pub, proto.NickJoinMessage(chatID, a.Name, a.Pub, nonce), sig) {
		return nil, bad("the signature does not verify for this chat, nick, key and challenge")
	}
	return pub, nil
}

// authorKey is the key a member's writes are signed with: its nick key, or the owner key for the
// owner; nil for a client that has none (an older one).
func (b *Broker) authorKey(m *member) ed25519.PublicKey {
	if m.pub != nil {
		return m.pub
	}
	if m.owner {
		return b.ownerKey()
	}
	return nil
}

// clockOf is the chat's logical clock: the value of its last write. A signed write carries the
// next one.
func (b *Broker) clockOf(ch *chat) (int64, *proto.Error) {
	if cs, ok := b.store.(store.Clock); ok {
		n, err := cs.Lamport(ch.id)
		if err != nil {
			return 0, proto.Errf("store_failed", "the chat's clock could not be read: %v", err)
		}
		return n, nil
	}
	return ch.memClock, nil
}

// wrote tells the chat that a write advanced its clock (only the memory store needs it: the
// database advances the clock itself).
func (ch *chat) wrote() {
	if _, ok := ch.store.(store.Clock); !ok {
		ch.memClock++
	}
}

func (b *Broker) clock(ch *chat) (any, *proto.Error) {
	n, e := b.clockOf(ch)
	if e != nil {
		return nil, e
	}
	return proto.ClockReply{Next: n + 1}, nil
}

func staleClock(next int64) *proto.Error {
	data, _ := json.Marshal(proto.ClockReply{Next: next})
	return &proto.Error{Code: "stale_clock", Data: data,
		Msg: fmt.Sprintf("the chat's clock moved on: sign again with lamport=%d", next)}
}

// verifyWrite checks the signature of a write request (raw: its arguments as they came) and
// returns what is stored with the rows it creates. A client without a key (an older one, proto
// below 4) is not signed: its events are stored with an empty signature. A client that has a key
// and speaks proto 4 must sign.
func (b *Broker) verifyWrite(ch *chat, m *member, kind string, raw json.RawMessage) (store.EventSig, *proto.Error) {
	s, err := proto.SplitSigned(raw)
	if err != nil {
		return store.EventSig{}, proto.Errf("bad_args", "%v", err)
	}
	canon := store.EventSig{Canon: string(s.Canon)} // what an unsigned write is stored with
	key := b.authorKey(m)
	if key == nil {
		return canon, nil
	}
	if s.Sig == "" {
		if m.proto >= proto.SigningProto {
			return store.EventSig{}, proto.Errf("unsigned", "this client has a signing key: sign the %s (origin, lamport, sig)", kind)
		}
		return canon, nil
	}
	if s.Origin != b.origin {
		return store.EventSig{}, proto.Errf("wrong_origin", "the request is addressed to the node %q, this is %q", s.Origin, b.origin)
	}
	sig := decodeSig(s.Sig)
	if sig == nil || !ed25519.Verify(key, proto.EventMessage(ch.id, b.origin, kind, s.Canon, s.Lamport), sig) {
		return store.EventSig{}, proto.Errf("bad_signature", "the signature does not verify for this chat, node, kind, request and clock; the event was not stored")
	}
	cur, e := b.clockOf(ch)
	if e != nil {
		return store.EventSig{}, e
	}
	if s.Lamport != cur+1 {
		return store.EventSig{}, staleClock(cur + 1)
	}
	return store.EventSig{Pub: owner.Encode(key), Sig: s.Sig, Payload: string(s.Canon), Canon: string(s.Canon)}, nil
}
