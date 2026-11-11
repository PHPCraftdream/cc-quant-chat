package broker

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/invite"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// Entering a chat by invitation, and the rules of entry that go with it (docs/design-invites.md).

const (
	inviteTriesPerMinute = 5 // uses of invitations that one connection may try: ids are no secret
	inviteWindow         = time.Minute
)

// inviteTry counts one use of an invitation on this connection; false: too many in the last minute.
func (c *conn) inviteTry() bool {
	now := time.Now()
	kept := c.inviteHits[:0]
	for _, t := range c.inviteHits {
		if now.Sub(t) < inviteWindow {
			kept = append(kept, t)
		}
	}
	c.inviteHits = kept
	if len(kept) >= inviteTriesPerMinute {
		return false
	}
	c.inviteHits = append(c.inviteHits, now)
	return true
}

// entryByInviteOnly: once the machine has an owner, a new nick (and a nick that moves to a key) gets
// into a chat by invitation, a token is not enough. The brokers of most tests keep the entry of
// earlier versions (lenientEntry); a broker made for real never does.
func (b *Broker) entryByInviteOnly() bool { return b.ownerKey() != nil && !b.lenientEntry }

func inviteRequired(ch *chat, nick string) *proto.Error {
	return proto.Errf("invite_required", "this chat is entered by invitation: ask the owner of this machine for one (ccqc invite %s %s)", ch.name, nick)
}

// chatKnown reports whether this node has a chat of that id (a join by id makes none).
func (b *Broker) chatKnown(id string) bool {
	is, ok := b.store.(store.Invites)
	if !ok {
		return false
	}
	name, err := is.ChatName(id)
	return err == nil && name != ""
}

// joinByInvite registers the key of a join for its nick with an invitation: a new nick, or the
// replacement of the key of one that exists (the old key stops working at once). The caller holds
// the chat's lock. It returns the nick and the key, as the other ways into a chat do.
func (b *Broker) joinByInvite(ch *chat, c *conn, a proto.JoinArgs, nk *nick) (*nick, ed25519.PublicKey, *proto.Error) {
	is, ok := b.store.(store.Invites)
	if !ok {
		return nil, nil, proto.Errf("invites_unavailable", "invitations need the SQLite store (the broker runs with CCQC_STORE=memory)")
	}
	if a.Pub == "" || a.NickSig == "" || a.Invite.ID == "" {
		return nil, nil, proto.Errf("bad_args", "an invitation is used with the nick's key: pub, nick_sig and the invitation's proof")
	}
	if !c.inviteTry() {
		return nil, nil, proto.Errf("rate_limited", "too many invitations tried on this connection; wait a minute")
	}
	if m := ch.members[a.Name]; m != nil && m.conn != nil {
		return nil, nil, proto.Errf("name_online", "nick @%s is already online in this chat", a.Name)
	}
	nonce, ok := c.takeNonce()
	if !ok {
		return nil, nil, proto.Errf("bad_nick_sig", "no valid challenge on this connection (ask for a new one)")
	}
	pub, e := decodeKey(a.Pub)
	if e != nil {
		return nil, nil, e
	}
	if sig := decodeSig(a.NickSig); sig == nil || !ed25519.Verify(pub, proto.NickJoinMessage(ch.id, a.Name, a.Pub, nonce), sig) {
		return nil, nil, proto.Errf("bad_nick_sig", "the signature does not verify for this chat, nick, key and challenge")
	}
	proof := decodeSig(a.Invite.Sig)
	proofOK := func(r invite.Record) bool {
		return r.Kind == invite.KindChat && r.Scope == a.Name && proof != nil &&
			invite.Verify(r.Pub, []byte(nonce), proto.InviteSubject(ch.id, a.Name, a.Pub), proof)
	}
	rec := store.Record{T: "nick", Nick: a.Name, Pub: a.Pub, ID: ch.nextID} // nothing is "unread" for a new nick
	if nk != nil {
		rec = store.Record{T: "nick_key", Nick: a.Name, Hash: nk.hash, Pub: a.Pub}
	}
	used, err := is.JoinByInvite(ch.id, a.Invite.ID, time.Now(), a.Pub, proofOK, rec)
	var bad *invite.BadProof
	switch {
	case err == nil:
	case errors.As(err, &bad):
		text := fmt.Sprintf("an invitation for @%s was used with a wrong proof (%d of %d)", bad.Record.Scope, bad.Tries, invite.MaxTries)
		if bad.Burnt {
			text = fmt.Sprintf("the invitation for @%s is burnt by %d wrong proofs: the owner can issue a new one", bad.Record.Scope, invite.MaxTries)
		}
		b.inviteNotice(ch, "invite.fail", bad.Record.ID, bad.Record.Scope, text)
		return nil, nil, proto.Errf("invite_invalid", "the invitation does not work")
	case errors.Is(err, invite.ErrInvalid):
		return nil, nil, proto.Errf("invite_invalid", "the invitation does not work")
	default:
		return nil, nil, proto.Errf("store_failed", "%v", err)
	}
	if nk == nil {
		nk = &nick{pub: a.Pub, seen: ch.nextID}
		ch.nicks[a.Name] = nk
		ch.rooms[proto.RoomCommon].members[a.Name] = true
		b.inviteNotice(ch, "invite.use", used.ID, a.Name, fmt.Sprintf("@%s joined the chat by an invitation (%s)", a.Name, invite.Short(used.ID)))
	} else {
		nk.pub = a.Pub
		b.inviteNotice(ch, "invite.use", used.ID, a.Name, fmt.Sprintf("the key of @%s was replaced by an invitation (%s): the old key no longer works", a.Name, invite.Short(used.ID)))
	}
	return nk, pub, nil
}

// inviteNotice tells the chat, loudly, what happened to an invitation, and writes it in the
// administrator log (the first characters of the id and the nick: nothing that can be used).
func (b *Broker) inviteNotice(ch *chat, op, id, nick, text string) {
	args, _ := json.Marshal(map[string]string{"invite": invite.Short(id), "nick": nick, "chat": ch.id})
	b.logAdmin(op, args, "ok")
	b.announceChat(ch, text)
}

// chatIDOf is the id and name for the chat a join names: a token gives its hash, an id is the id.
func (b *Broker) chatIDOf(a proto.JoinArgs) (name, id string, e *proto.Error) {
	if a.Token != "" {
		var err error
		if name, id, err = proto.ParseToken(a.Token); err != nil {
			return "", "", proto.Errf("bad_token", "%v", err)
		}
		return name, id, nil
	}
	id = a.Chat
	if len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" {
		return "", "", proto.Errf("bad_args", "a join needs the chat's token or its id")
	}
	is, ok := b.store.(store.Invites)
	if !ok {
		return "", "", proto.Errf("invites_unavailable", "joining by a chat id needs the SQLite store")
	}
	name, err := is.ChatName(id)
	if err != nil || name == "" {
		return "", "", proto.Errf("no_such_chat", "this node has no chat with that id")
	}
	return name, id, nil
}
