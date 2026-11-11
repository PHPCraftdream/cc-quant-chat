package client

import (
	"crypto/ed25519"
	"encoding/json"

	"github.com/PHPCraftdream/cc-quant-chat/internal/owner"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// maxSignTries: how often a write is signed again after another write took its clock value.
const maxSignTries = 5

// Signer signs the writes of one nick in one chat, addressed to one broker (origin).
type Signer struct {
	Priv   ed25519.PrivateKey
	ChatID string
	Origin string
}

// Callf is Conn.Call, or a wrapper of it that survives reconnects.
type Callf func(op string, args, out any) error

// JoinSigned joins with the nick's key: it asks for a challenge, signs it and joins. A broker
// older than proto 4 has no use for the key; the join then goes by the secret in args.Key and the
// returned signer is nil (such a broker stores events unsigned).
func JoinSigned(call Callf, args proto.JoinArgs, priv ed25519.PrivateKey, chatID string, w *proto.Welcome) (*Signer, error) {
	args.Proto = proto.ProtoVersion
	var ch proto.Challenge
	if err := call("challenge", nil, &ch); err == nil {
		args.Pub = owner.Encode(priv.Public().(ed25519.PublicKey))
		args.NickSig = owner.Encode(ed25519.Sign(priv, proto.NickJoinMessage(chatID, args.Name, args.Pub, ch.Nonce)))
	} else if !proto.IsCode(err, "unknown_op") && !proto.IsCode(err, "not_joined") {
		return nil, err
	}
	if err := call("join", args, w); err != nil {
		return nil, err
	}
	if args.Pub == "" || w.Proto < proto.ProtoVersion || w.Origin == "" {
		return nil, nil
	}
	return &Signer{Priv: priv, ChatID: chatID, Origin: w.Origin}, nil
}

// Sign signs args as the author of a write of this kind (msg, a board op such as task.create, doc.<op>) at the given
// clock value and stamps them.
func (s *Signer) Sign(kind string, args proto.Stamper, lamport int64) error {
	args.Stamp(s.Origin, 0, "")
	raw, err := json.Marshal(args)
	if err != nil {
		return err
	}
	sp, err := proto.SplitSigned(raw)
	if err != nil {
		return err
	}
	sig := ed25519.Sign(s.Priv, proto.EventMessage(s.ChatID, s.Origin, kind, sp.Canon, lamport))
	args.Stamp(s.Origin, lamport, owner.Encode(sig))
	return nil
}

// Write signs and sends a write. The clock value comes from the broker; when another write took
// it first, the broker's answer carries the current one and the write is signed again, up to
// maxSignTries times.
func (s *Signer) Write(call Callf, op, kind string, args proto.Stamper, out any) error {
	var next int64
	for try := 1; ; try++ {
		if next == 0 {
			var r proto.ClockReply
			if err := call("clock", nil, &r); err != nil {
				return err
			}
			next = r.Next
		}
		if err := s.Sign(kind, args, next); err != nil {
			return err
		}
		err := call(op, args, out)
		n, stale := proto.StaleClock(err)
		if !stale || try >= maxSignTries {
			return err
		}
		next = n
	}
}
