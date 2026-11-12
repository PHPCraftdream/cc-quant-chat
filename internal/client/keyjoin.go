package client

import (
	"crypto/ed25519"
	"errors"
	"fmt"

	"github.com/PHPCraftdream/cc-quant-chat/internal/invite"
	"github.com/PHPCraftdream/cc-quant-chat/internal/keys"
	"github.com/PHPCraftdream/cc-quant-chat/internal/local"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// How a client gets into a chat under a nick (docs/design-keys.md): the key of a nick is derived from the
// client secret A and the broker's B. A nick enters by an invitation, which enrolls its protected key; after
// that the way in is the proof of A, then the join with the derived key. The order can't lose a nick: A is made
// and read back before the broker is asked.

// KeyJoinInput says what to join and who is joining.
type KeyJoinInput struct {
	ChatID string
	Nick   string
	Invite *invite.Invitation
	SID    string     // the Claude Code session id ("" for a terminal)
	IDs    SessionIDs // what the session knows of its id (the chain after /clear or a compaction)
}

// KeyJoin is what getting in brought back.
type KeyJoin struct {
	Signer    *Signer
	Priv      ed25519.PrivateKey // the key of the nick, for the writes and for the next connection
	Auth      ed25519.PrivateKey // for a protected nick: proves A on every connection
	Protected bool
	Note      string // for the person: what happened to the keys, or a warning
}

// ErrNoSession: the client has no Claude Code session id. A nick belongs to a session (the broker refuses an enrolment
// or a key without one); a terminal has none and enters as the owner, with the owner's key.
var ErrNoSession = errors.New("this client has no Claude Code session id, and a nick belongs to a session: in a session the SessionStart hook hands the id over (restart the session if the hook is not set up), and a terminal enters as the owner, with the owner's key")

// JoinWithKeys gets the connection into the chat.
func JoinWithKeys(call Callf, in KeyJoinInput, w *proto.Welcome) (KeyJoin, error) {
	if in.SID == "" && in.IDs.SID == "" {
		return KeyJoin{}, ErrNoSession
	}
	if in.Invite != nil {
		return enrollWithInvitation(call, in, w)
	}
	args := proto.JoinArgs{Name: in.Nick, Chat: in.ChatID}
	a, _, err := local.ClientSecretIfAny()
	if err != nil {
		return KeyJoin{}, keyStoreError(err)
	}
	if a != nil {
		signer, priv, err := JoinProtected(call, a, args, in.ChatID, in.IDs, w)
		if err == nil {
			return protectedIn(in, a, signer, priv, ""), nil
		}
		if !errors.Is(err, ErrNotProtected) {
			return KeyJoin{}, err
		}
	}
	return KeyJoin{}, fmt.Errorf("@%s has no protected key in this chat: ask the owner for an invitation (ccqc invite <chat> %s)", in.Nick, in.Nick)
}

// secretSource hands out A when the join needs it: the one the client already has, or one made on the
// spot, and only after the broker has shown that it keeps protected keys (no secret is made for a broker
// that can't use it).
type secretSource struct {
	a    []byte
	note string // a warning about the place A is kept in
	err  error  // why it could not be had
}

func (s *secretSource) get() ([]byte, error) {
	if s.a != nil {
		return s.a, nil
	}
	a, ks, err := local.ClientSecret()
	if err != nil {
		s.err = err
		return nil, fmt.Errorf("the client secret could not be set up: %w", err)
	}
	s.a = a
	if ks.Warning != "" {
		s.note = "WARNING: " + ks.Warning + "\n\n"
	}
	return a, nil
}

// enrollWithInvitation: an invitation enrolls a protected key.
func enrollWithInvitation(call Callf, in KeyJoinInput, w *proto.Welcome) (KeyJoin, error) {
	src := &secretSource{}
	signer, priv, err := JoinEnrolled(call, Enroll{ChatID: in.ChatID, Nick: in.Nick, SID: in.SID, A: src.get, Invite: in.Invite}, w)
	switch {
	case err == nil:
		return protectedIn(in, src.a, signer, priv, src.note), nil
	case errors.Is(err, ErrNotProtected):
		return KeyJoin{}, errors.New("the broker keeps no protected keys: run `ccqc broker restart` to update it")
	}
	return KeyJoin{}, err
}

// protectedIn records what the client keeps of a protected nick: that it is one.
func protectedIn(in KeyJoinInput, a []byte, signer *Signer, priv ed25519.PrivateKey, note string) KeyJoin {
	auth, _ := keys.AuthKey(a, in.ChatID, in.Nick)
	if err := local.MarkProtected(in.ChatID, in.Nick); err != nil {
		note += "WARNING: could not record that @" + in.Nick + " is protected (" + err.Error() + ")\n\n"
	}
	return KeyJoin{Signer: signer, Priv: priv, Auth: auth, Protected: true, Note: note}
}

// keyStoreError says what a lost or unreadable client secret means for the person.
func keyStoreError(err error) error {
	if errors.Is(err, local.ErrSecretLost) {
		return fmt.Errorf("%w: ask the owner for new invitations (ccqc invite <chat> <nick>)", err)
	}
	return fmt.Errorf("the client secret could not be read: %w", err)
}
