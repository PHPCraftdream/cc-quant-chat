package client

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/PHPCraftdream/cc-quant-chat/internal/invite"
	"github.com/PHPCraftdream/cc-quant-chat/internal/keys"
	"github.com/PHPCraftdream/cc-quant-chat/internal/local"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// How a client gets into a chat under a nick, with protected keys (docs/design-keys.md): the key of a
// nick is derived from the client secret A and the broker's B. The way in is the proof of A, then the
// join with the derived key; a nick that still has a plain key moves to a protected one at its next
// join, with the proof of the old key; an invitation enrolls a protected key straight away. Against a
// broker that does not know protected keys everything goes as before. The order is the one that can't
// lose a nick: A is made and read back before the broker is asked, and the plain key is removed only
// after the broker has the protected one.

// KeyJoinInput says what to join and who is joining.
type KeyJoinInput struct {
	ChatID string
	Token  string // a chat of tokens is joined by its token; "" for a chat of invitations
	Nick   string
	Invite *invite.Invitation
	SID    string     // the Claude Code session id ("" for a terminal)
	IDs    SessionIDs // what the session knows of its id (the chain after /clear or a compaction)
}

// KeyJoin is what getting in brought back.
type KeyJoin struct {
	Signer    *Signer
	Priv      ed25519.PrivateKey // the key of the nick, for the writes and for the next connection
	Auth      ed25519.PrivateKey // for a protected nick: proves A on every connection (nil: a plain key)
	Protected bool
	Note      string // for the person: what happened to the keys, or a warning
}

// JoinWithKeys gets the connection into the chat.
func JoinWithKeys(call Callf, in KeyJoinInput, w *proto.Welcome) (KeyJoin, error) {
	if in.Invite != nil {
		return enrollWithInvitation(call, in, w)
	}
	args := proto.JoinArgs{Name: in.Nick}
	if in.Token != "" {
		args.Token = in.Token
		args.Key = local.NickKey(in.ChatID, in.Nick) // the old secret: a nick of the time of secrets moves with it
	} else {
		args.Chat = in.ChatID
	}
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
	return moveOrPlain(call, in, a, args, w)
}

// secretSource hands out A when the join needs it: the one the client already has, or one made on the
// spot, and only after the broker has shown that it keeps protected keys (no secret is made for a broker
// that can't use it). what says why a failure matters to the person.
type secretSource struct {
	a    []byte
	note string // a warning about the place A is kept in
	err  error  // why it could not be had
}

var errNoSecret = errors.New("no client secret")

func (s *secretSource) get() ([]byte, error) {
	if s.a != nil {
		return s.a, nil
	}
	a, ks, err := local.ClientSecret()
	if err != nil {
		s.err = err
		return nil, fmt.Errorf("%w: %v", errNoSecret, err)
	}
	s.a = a
	if ks.Warning != "" {
		s.note = "WARNING: " + ks.Warning + "\n\n"
	}
	return a, nil
}

// moveOrPlain: the nick has no protected key yet. If the client holds its old key (or secret) the nick
// moves to a protected one; else, or against a broker that can't keep one, the plain key is used as ever.
func moveOrPlain(call Callf, in KeyJoinInput, a []byte, args proto.JoinArgs, w *proto.Welcome) (KeyJoin, error) {
	seed, hasSeed, err := local.PlainNickKey(in.ChatID, in.Nick)
	if err != nil {
		return KeyJoin{}, fmt.Errorf("the nick key could not be read: %w", err)
	}
	note := ""
	if hasSeed || (in.Token != "" && args.Key != "") {
		src := &secretSource{a: a}
		e := Enroll{ChatID: in.ChatID, Nick: in.Nick, SID: in.SID, A: src.get, Token: in.Token}
		if hasSeed {
			e.OldKey = ed25519.NewKeyFromSeed(seed)
		} else {
			e.OldSecret = args.Key
		}
		signer, priv, err := JoinEnrolled(call, e, w)
		switch {
		case err == nil:
			return protectedIn(in, src.a, signer, priv, src.note+"The key of @"+in.Nick+" was moved to a protected key: the old one no longer works.\n\n"), nil
		case errors.Is(err, errNoSecret):
			note = "the nick key stays plain: the client secret could not be set up (" + src.err.Error() + ")\n\n"
		case errors.Is(err, ErrNotProtected):
			note = "the broker is older and keeps no protected keys: the nick key stays plain until `ccqc broker restart` updates it\n\n"
		default:
			return KeyJoin{}, err
		}
	}
	priv, err := local.NickPrivateKey(in.ChatID, in.Nick)
	if err != nil {
		return KeyJoin{}, fmt.Errorf("the nick key could not be read or saved: %w", err)
	}
	signer, err := JoinSigned(call, args, priv, in.ChatID, w)
	if err != nil {
		return KeyJoin{}, err
	}
	return KeyJoin{Signer: signer, Priv: priv, Note: note}, nil
}

// enrollWithInvitation: an invitation registers a protected key, or (broker too old, or no place for the
// client secret) a plain one as before; that one is kept in nick-keys.json.
func enrollWithInvitation(call Callf, in KeyJoinInput, w *proto.Welcome) (KeyJoin, error) {
	src := &secretSource{}
	signer, priv, err := JoinEnrolled(call, Enroll{ChatID: in.ChatID, Nick: in.Nick, SID: in.SID, A: src.get, Invite: in.Invite}, w)
	note := ""
	switch {
	case err == nil:
		return protectedIn(in, src.a, signer, priv, src.note), nil
	case errors.Is(err, errNoSecret):
		note = "the nick key is a plain one: the client secret could not be set up (" + src.err.Error() + ")\n\n"
	case errors.Is(err, ErrNotProtected):
		note = "the broker is older and keeps no protected keys: the nick key is a plain one until `ccqc broker restart` updates it\n\n"
	default:
		return KeyJoin{}, err
	}
	_, fresh, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return KeyJoin{}, err
	}
	plain, err := JoinInvited(call, in.Nick, fresh, *in.Invite, w)
	if err != nil {
		return KeyJoin{}, err
	}
	if err := local.SetNickPrivateKey(in.ChatID, in.Nick, fresh); err != nil {
		note += fmt.Sprintf("WARNING: the key of nick @%s could not be saved (%v). It works until this session ends; after that the owner must issue a new invitation.\n\n", in.Nick, err)
	}
	return KeyJoin{Signer: plain, Priv: fresh, Note: note}, nil
}

// protectedIn records what the client keeps of a protected nick: that it is one, and nothing of the old
// plain key (the broker has the new one, so the old no longer opens the nick: it goes only now).
func protectedIn(in KeyJoinInput, a []byte, signer *Signer, priv ed25519.PrivateKey, note string) KeyJoin {
	auth, _ := keys.AuthKey(a, in.ChatID, in.Nick)
	if err := local.MarkProtected(in.ChatID, in.Nick); err != nil {
		note += "WARNING: could not record that @" + in.Nick + " is protected (" + err.Error() + ")\n\n"
	}
	if err := local.DropNickKeys(in.ChatID, in.Nick); err != nil {
		note += "WARNING: the old plain key of @" + in.Nick + " could not be removed from nick-keys.json (" + err.Error() + "): it no longer opens the nick\n\n"
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
