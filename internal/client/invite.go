package client

import (
	"crypto/ed25519"

	"github.com/PHPCraftdream/cc-quant-chat/internal/invite"
	"github.com/PHPCraftdream/cc-quant-chat/internal/owner"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// JoinInvited enters a chat with a one-time invitation: it registers the key of priv for the nick the
// invitation is for (or replaces the key of that nick). It asks for a challenge, signs it as the nick
// and, with the key the invitation holds, binds the use to this connection, this chat, this nick and
// this key. The invitation is not sent: only the proof. The signer is nil if the broker does not sign.
func JoinInvited(call Callf, name string, priv ed25519.PrivateKey, in invite.Invitation, w *proto.Welcome) (*Signer, error) {
	var ch proto.Challenge
	if err := call("challenge", nil, &ch); err != nil {
		return nil, err
	}
	pub := owner.Encode(priv.Public().(ed25519.PublicKey))
	args := proto.JoinArgs{
		Chat: in.ChatID, Name: name, Pub: pub, Proto: proto.ProtoVersion,
		NickSig: owner.Encode(ed25519.Sign(priv, proto.NickJoinMessage(in.ChatID, name, pub, ch.Nonce))),
		Invite:  &proto.JoinInvite{ID: in.ID(), Sig: owner.Encode(in.Proof([]byte(ch.Nonce), proto.InviteSubject(in.ChatID, name, pub)))},
	}
	if err := call("join", args, w); err != nil {
		return nil, err
	}
	if w.Proto < proto.SigningProto || w.Origin == "" {
		return nil, nil
	}
	return &Signer{Priv: priv, ChatID: in.ChatID, Origin: w.Origin}, nil
}
