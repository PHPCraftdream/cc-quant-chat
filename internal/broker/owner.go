package broker

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"log"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/local"
	"github.com/PHPCraftdream/cc-quant-chat/internal/node"
	"github.com/PHPCraftdream/cc-quant-chat/internal/owner"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// The owner is the human who owns the broker on this machine. The broker holds only the public
// key. A connection becomes the owner's by signing a one-time challenge at join; the mark on the
// member and on its messages is set here and nowhere else: no field a client sends carries it.

const challengeTTL = 30 * time.Second

// loadOwner reads the registered owner key at start.
func (b *Broker) loadOwner() error {
	os, ok := b.store.(store.OwnerStore)
	if !ok {
		return nil
	}
	pub, since, found, err := os.Owner()
	if err != nil {
		return err
	}
	if found {
		b.ownerPub, b.ownerAt = ed25519.PublicKey(pub), since
	}
	return nil
}

// initNode loads (or, on the first run, creates) the node key and records the origin.
func (b *Broker) initNode() error {
	priv, err := node.Load(local.NodeKeyPath())
	if err != nil {
		return err
	}
	pub := priv.Public().(ed25519.PublicKey)
	b.origin = node.Origin(pub)
	if ns, ok := b.store.(store.NodeStore); ok {
		return ns.RegisterOrigin(b.origin, pub)
	}
	return nil
}

// announce tells every present member of every loaded chat, loudly: a change of who the owner
// is must not pass quietly.
func (b *Broker) announce(text string) {
	for _, ch := range b.chats {
		for name, m := range ch.members {
			if m.conn != nil {
				b.deliver(ch, name, proto.Event{Kind: "notice", Text: text, Wake: true})
			}
		}
	}
}

// challenge issues a one-time nonce bound to the connection.
func (b *Broker) challenge(c *conn) (any, *proto.Error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, proto.Errf("internal", "%v", err)
	}
	c.nonce, c.nonceAt = owner.Encode(raw), time.Now()
	return proto.Challenge{Nonce: c.nonce}, nil
}

// verifyOwner checks the signature a join carries. The nonce is spent whatever the outcome.
func (b *Broker) verifyOwner(c *conn, chatID, nick, sigB64 string) *proto.Error {
	nonce, valid := c.takeNonce()
	bad := func(why string) *proto.Error { return proto.Errf("bad_owner_sig", "%s", why) }
	if b.ownerPub == nil {
		return bad("no owner is registered on this broker")
	}
	if !valid {
		return bad("no valid challenge on this connection (ask for a new one)")
	}
	sig, err := owner.Decode(sigB64)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return bad("the signature is malformed")
	}
	if !ed25519.Verify(b.ownerPub, proto.OwnerJoinMessage(chatID, nick, nonce), sig) {
		return bad("the signature does not verify for this chat, nick and challenge")
	}
	return nil
}

func decodeKey(pubB64 string) (ed25519.PublicKey, *proto.Error) {
	pub, err := owner.Decode(pubB64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, proto.Errf("bad_args", "pub is not a base64 Ed25519 public key")
	}
	return ed25519.PublicKey(pub), nil
}

func decodeSig(s string) []byte {
	sig, err := owner.Decode(s)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil
	}
	return sig
}

// ownerRegister records the first owner key. The signature proves the caller holds the private
// key; it does not prove who the caller is, which is why the human registers right after the
// broker is set up (README).
func (b *Broker) ownerRegister(a proto.OwnerRegisterArgs) (any, *proto.Error) {
	pub, e := decodeKey(a.Pub)
	if e != nil {
		return nil, e
	}
	if sig := decodeSig(a.Sig); sig == nil || !ed25519.Verify(pub, proto.OwnerRegisterMessage(a.Pub), sig) {
		return nil, proto.Errf("bad_owner_sig", "the registration is not signed with the key it registers")
	}
	if b.ownerPub != nil {
		return nil, proto.Errf("owner_exists", "an owner key is already registered (%s)", owner.Fingerprint(b.ownerPub))
	}
	if os, ok := b.store.(store.OwnerStore); ok {
		if err := os.SetOwner(pub); err != nil {
			if errors.Is(err, store.ErrOwnerExists) {
				return nil, proto.Errf("owner_exists", "an owner key is already registered")
			}
			return nil, proto.Errf("store_failed", "%v", err)
		}
	}
	b.ownerPub, b.ownerAt = pub, time.Now().UTC().Format(time.RFC3339)
	log.Printf("broker: owner key registered (%s)", owner.Fingerprint(pub))
	b.announce("owner key registered: " + owner.Fingerprint(pub))
	return proto.OwnerInfo{Registered: true, Fingerprint: owner.Fingerprint(pub), Since: b.ownerAt}, nil
}

// ownerRotate replaces the key. Both keys sign the request: the old one authorizes, the new one
// proves it is held.
func (b *Broker) ownerRotate(a proto.OwnerRotateArgs) (any, *proto.Error) {
	if b.ownerPub == nil {
		return nil, proto.Errf("no_owner", "no owner is registered yet; use owner init")
	}
	pub, e := decodeKey(a.Pub)
	if e != nil {
		return nil, e
	}
	msg := proto.OwnerRotateMessage(owner.Encode(b.ownerPub), a.Pub)
	oldSig, newSig := decodeSig(a.OldSig), decodeSig(a.NewSig)
	if oldSig == nil || !ed25519.Verify(b.ownerPub, msg, oldSig) {
		return nil, proto.Errf("bad_owner_sig", "the request is not signed with the current owner key")
	}
	if newSig == nil || !ed25519.Verify(pub, msg, newSig) {
		return nil, proto.Errf("bad_owner_sig", "the request is not signed with the new key")
	}
	if os, ok := b.store.(store.OwnerStore); ok {
		if err := os.ReplaceOwner(b.ownerPub, pub); err != nil {
			return nil, proto.Errf("store_failed", "%v", err)
		}
	}
	b.ownerPub, b.ownerAt = pub, time.Now().UTC().Format(time.RFC3339)
	for _, ch := range b.chats { // connections verified with the old key no longer count
		for _, m := range ch.members {
			m.owner = false
		}
	}
	log.Printf("broker: owner key replaced (%s)", owner.Fingerprint(pub))
	b.announce("owner key replaced: " + owner.Fingerprint(pub))
	return proto.OwnerInfo{Registered: true, Fingerprint: owner.Fingerprint(pub), Since: b.ownerAt}, nil
}

func (b *Broker) ownerInfo() proto.OwnerInfo {
	if b.ownerPub == nil {
		return proto.OwnerInfo{}
	}
	return proto.OwnerInfo{Registered: true, Fingerprint: owner.Fingerprint(b.ownerPub), Since: b.ownerAt}
}
