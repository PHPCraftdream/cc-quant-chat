package client

import (
	"crypto/ed25519"
	"errors"

	"github.com/PHPCraftdream/cc-quant-chat/internal/invite"
	"github.com/PHPCraftdream/cc-quant-chat/internal/keys"
	"github.com/PHPCraftdream/cc-quant-chat/internal/owner"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// The client's side of protected nick keys (docs/design-keys.md): the key of a nick is derived from
// the client secret A and the broker's B. A join proves A first (key_unlock) and then joins as usual
// with the derived key; the first time, a key is enrolled by an invitation or moved from the nick's
// old key.

// ErrNotProtected: this broker does not keep a protected key for the nick (or does not speak the
// protocol at all); the caller goes on the old way, or enrolls.
var ErrNotProtected = errors.New("the nick has no protected key at this broker")

// SessionIDs is what a session says of itself: its Claude Code session id ("" for a terminal) and,
// after /clear or a compaction, the id the broker has it bound to and why it changed.
type SessionIDs struct {
	SID, PrevSID, Source string
}

func notProtected(err error) bool {
	return proto.IsCode(err, "unlock_unknown") || proto.IsCode(err, "unknown_op") || proto.IsCode(err, "keys_unavailable")
}

// Unlock proves A for the nick on this connection and returns B. ErrNotProtected: no protected key.
func Unlock(call Callf, auth ed25519.PrivateKey, chatID, nick string, ids SessionIDs) ([]byte, error) {
	var ch proto.Challenge
	if err := call("challenge", nil, &ch); err != nil {
		if proto.IsCode(err, "unknown_op") || proto.IsCode(err, "not_joined") {
			return nil, ErrNotProtected
		}
		return nil, err
	}
	var rep proto.KeyUnlockReply
	err := call("key_unlock", proto.KeyUnlockArgs{Chat: chatID, Nick: nick, SID: ids.SID, PrevSID: ids.PrevSID, Source: ids.Source,
		AuthSig: owner.Encode(ed25519.Sign(auth, proto.KeyUnlockMessage(chatID, nick, ch.Nonce)))}, &rep)
	if err != nil {
		if notProtected(err) {
			return nil, ErrNotProtected
		}
		return nil, err
	}
	return owner.Decode(rep.B)
}

// JoinProtected joins under the protected key of a nick: the proof of A, the key from A and B, the
// join at the level that knows protected keys. ErrNotProtected: the nick has none. It returns the nick's
// private key, which the session keeps for the writes and for the next connection.
func JoinProtected(call Callf, a []byte, args proto.JoinArgs, chatID string, ids SessionIDs, w *proto.Welcome) (*Signer, ed25519.PrivateKey, error) {
	auth, err := keys.AuthKey(a, chatID, args.Name)
	if err != nil {
		return nil, nil, err
	}
	b, err := Unlock(call, auth, chatID, args.Name, ids)
	if err != nil {
		return nil, nil, err
	}
	defer keys.Wipe(b)
	priv, err := keys.SignKey(a, b, chatID, args.Name)
	if err != nil {
		return nil, nil, err
	}
	signer, err := JoinSigned(call, args, priv, chatID, w)
	if err != nil {
		return nil, nil, err
	}
	return signer, priv, nil
}

// Enroll is a protected key about to be registered: for the nick of a chat, with the secret A, from the
// session SID ("" for a terminal), authorized by one of the proofs: an invitation, the old private key of
// the nick, or (for a nick of the time of secrets) its old secret.
type Enroll struct {
	ChatID, Nick, SID string
	A                 func() ([]byte, error) // the client secret; asked for only once the broker has shown it keeps protected keys
	Invite            *invite.Invitation
	Token             string // a chat of tokens is joined by its token
}

// JoinEnrolled registers a protected key and joins with it. ErrNotProtected: this broker can't keep
// protected keys (the caller keeps the plain key and says so). Nothing is stored on the client side: the
// caller does that, after this succeeded, in the order that cannot lose the nick.
func JoinEnrolled(call Callf, e Enroll, w *proto.Welcome) (*Signer, ed25519.PrivateKey, error) {
	var rep proto.KeyPrepareReply
	if err := call("key_prepare", proto.KeyPrepareArgs{Chat: e.ChatID, Nick: e.Nick}, &rep); err != nil {
		if notProtected(err) {
			return nil, nil, ErrNotProtected
		}
		return nil, nil, err
	}
	b, err := owner.Decode(rep.B)
	if err != nil {
		return nil, nil, err
	}
	defer keys.Wipe(b)
	a, err := e.A()
	if err != nil {
		return nil, nil, err
	}
	auth, err := keys.AuthKey(a, e.ChatID, e.Nick)
	if err != nil {
		return nil, nil, err
	}
	priv, err := keys.SignKey(a, b, e.ChatID, e.Nick)
	if err != nil {
		return nil, nil, err
	}
	var ch proto.Challenge
	if err := call("challenge", nil, &ch); err != nil {
		return nil, nil, err
	}
	pub := owner.Encode(priv.Public().(ed25519.PublicKey))
	authPub := owner.Encode(auth.Public().(ed25519.PublicKey))
	args := proto.JoinArgs{Name: e.Nick, Pub: pub, Proto: proto.KeysProto,
		NickSig: owner.Encode(ed25519.Sign(priv, proto.NickJoinMessage(e.ChatID, e.Nick, pub, ch.Nonce))),
		Enroll:  &proto.JoinEnroll{AuthPub: authPub, SID: e.SID}}
	if e.Token != "" {
		args.Token = e.Token
	} else {
		args.Chat = e.ChatID
	}
	switch {
	case e.Invite != nil:
		args.Invite = &proto.JoinInvite{ID: e.Invite.ID(), Sig: owner.Encode(e.Invite.Proof([]byte(ch.Nonce), proto.InviteSubject6(e.ChatID, e.Nick, pub, authPub)))}
	}
	if err := call("join", args, w); err != nil {
		return nil, nil, err
	}
	if w.Origin == "" {
		return nil, priv, nil
	}
	return &Signer{Priv: priv, ChatID: e.ChatID, Origin: w.Origin}, priv, nil
}
