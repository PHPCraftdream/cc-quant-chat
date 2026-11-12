package broker

import (
	"crypto/ed25519"

	"github.com/PHPCraftdream/cc-quant-chat/internal/owner"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// The move of a nick that exists to a protected key, without a new invitation (docs/design-keys.md
// section 9): whoever holds its current private key (or, for a nick of the time of secrets, its old
// secret) already is the nick, so that proof is enough. The old key and every copy of it stop opening
// the nick at once, because the key registered in the chat is replaced.

// joinMove checks the proof, writes the pending row, replaces the nick's key in the chat and makes the
// row the active one. The caller holds the chat's lock. It returns the nick and the new key.
func (b *Broker) joinMove(ch *chat, c *conn, a proto.JoinArgs, nk *nick) (*nick, ed25519.PublicKey, *proto.Error) {
	if nk == nil {
		return nil, nil, inviteRequired(ch, a.Name) // a new nick is made by an invitation only
	}
	ss, ok := b.nickSecrets()
	if !ok {
		return nil, nil, keysUnavailable()
	}
	if a.Pub == "" || a.NickSig == "" {
		return nil, nil, proto.Errf("bad_args", "a move is made with the new key: pub and nick_sig")
	}
	if row, err := ss.ActiveNickSecret(ch.id, a.Name); err != nil {
		return nil, nil, proto.Errf("store_failed", "%v", err)
	} else if row != nil {
		return nil, nil, proto.Errf("already_protected", "the key of @%s is protected already; the owner replaces it with an invitation", a.Name)
	}
	if !c.inviteTry() {
		return nil, nil, proto.Errf("rate_limited", "too many moves tried on this connection; wait a minute")
	}
	if m := ch.members[a.Name]; m != nil && m.conn != nil {
		return nil, nil, proto.Errf("name_online", "nick @%s is already online in this chat", a.Name)
	}
	pub, e := decodeKey(a.Pub)
	if e != nil {
		return nil, nil, e
	}
	nonce, valid := c.takeNonce()
	if !valid {
		return nil, nil, proto.Errf("bad_nick_sig", "no valid challenge on this connection (ask for a new one)")
	}
	if sig := decodeSig(a.NickSig); sig == nil || !ed25519.Verify(pub, proto.NickJoinMessage(ch.id, a.Name, a.Pub, nonce), sig) {
		return nil, nil, proto.Errf("bad_nick_sig", "the signature does not verify for this chat, nick, key and challenge")
	}
	via := ""
	switch {
	case a.Enroll.OldSig != "" && nk.pub != "":
		if sig := decodeSig(a.Enroll.OldSig); sig != nil && ed25519.Verify(ed25519.PublicKey(mustPub(nk.pub)), proto.KeyMoveMessage(ch.id, a.Name, a.Pub, a.Enroll.AuthPub, nonce), sig) {
			via = "old key"
		}
	case a.Enroll.OldSig == "" && nk.pub == "" && nk.hash != "" && nk.check(a.Key):
		via = "old secret"
	}
	if via == "" {
		b.logAdminMap("key.move", map[string]string{"chat": ch.id[:8], "nick": a.Name, "peer": c.peer.String()}, "refused: no proof of the old key")
		return nil, nil, proto.Errf("name_taken", "nick @%s is registered in this chat under another key: a move needs the signature of its current key (or, for a nick without one, its old secret)", a.Name)
	}
	if _, e := b.prepareEnroll(c, ch, a); e != nil {
		return nil, nil, e
	}
	if e := ch.save(store.Record{T: "nick_key", Nick: a.Name, Hash: nk.hash, Pub: a.Pub}); e != nil {
		logIf("drop the pending secret", ss.DropPendingNickSecret(ch.id, a.Name))
		return nil, nil, e
	}
	nk.pub = a.Pub
	_, err := ss.ActivateNickSecret(ch.id, a.Name)
	logIf("activate the secret", err) // a pending row is settled at the next start
	b.logAdminMap("key.move", map[string]string{"chat": ch.id[:8], "nick": a.Name, "via": via, "peer": c.peer.String()}, "ok")
	b.announceChat(ch, "the key of @"+a.Name+" was moved to a protected key ("+via+"): the old key no longer works")
	return nk, pub, nil
}

// mustPub decodes a public key the broker stored itself.
func mustPub(s string) []byte {
	raw, err := owner.Decode(s)
	if err != nil {
		return nil
	}
	return raw
}
