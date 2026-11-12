package broker

import (
	"crypto/ed25519"
	"sort"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// A nick enters a chat by an invitation that enrolls its protected key, and afterwards only by that key (keys.go).
// The owner enters under a nick by its own key, every time.

// dispatchJoin serves a join: what needs no chat is checked first; then the chat is found or loaded
// (without the broker's lock held), locked, entered, and the reply sent before the lock is let go.
func (b *Broker) dispatchJoin(c *conn, f *proto.Frame) *proto.Error {
	fail := func(perr *proto.Error) *proto.Error {
		c.reply(f.ID, nil, perr)
		return perr
	}
	if b.stopping.Load() {
		return fail(proto.Errf("restarting", "the broker is restarting; repeat in a moment"))
	}
	var a proto.JoinArgs
	if e := decode(f.Args, &a); e != nil {
		return fail(e)
	}
	name, id, signed, e := b.joinPre(c, a)
	if e != nil {
		return fail(e)
	}
	ch, err := b.useChat(id, name)
	if err != nil {
		return fail(proto.Errf("store_unavailable", "chat history can't be read (%v); not joining so nothing gets overwritten. See broker.log", err))
	}
	res, perr := b.joinIn(ch, c, a, signed)
	c.reply(f.ID, b.markQuarantined(res), perr)
	b.doneChat(ch)
	if perr != nil { // a chat loaded for a join that failed must not stay cached without members
		b.unloadIfEmpty(ch)
	}
	return perr
}

// joinPre: the checks of a join that need no chat.
func (b *Broker) joinPre(c *conn, a proto.JoinArgs) (name, id string, signed bool, e *proto.Error) {
	if c.m != nil {
		return "", "", false, proto.Errf("already_joined", "this connection already joined a chat")
	}
	if a.Proto < proto.KeysProto {
		return "", "", false, proto.Errf("old_client", "this broker speaks protocol %d and your ccqc speaks %d: update ccqc", proto.ProtoVersion, a.Proto)
	}
	name, id, e = b.chatIDOf(a)
	if e != nil {
		return "", "", false, e
	}
	if !proto.ValidName(a.Name) {
		return "", "", false, proto.Errf("bad_name", "nick must match [a-z0-9-]{1,32}: %q", a.Name)
	}
	// The owner proves itself by signing the challenge; nothing else a client sends counts. Once
	// there is an owner, its nick is reserved for it.
	if a.OwnerSig != "" {
		if e := b.verifyOwner(c, id, a.Name, a.OwnerSig); e != nil {
			return "", "", false, e
		}
		signed = true
	} else if a.Name == proto.OwnerNick && b.ownerKey() != nil {
		return "", "", false, proto.Errf("reserved", "the nick @%s is reserved for the owner of this machine", proto.OwnerNick)
	}
	return name, id, signed, nil
}

// joinIn enters the chat; the caller holds the chat's lock.
func (b *Broker) joinIn(ch *chat, c *conn, a proto.JoinArgs, signed bool) (any, *proto.Error) {
	id := ch.id
	var pub ed25519.PublicKey
	nk := ch.nicks[a.Name]
	if e := b.protectedJoinCheck(ch, c, a, nk, signed); e != nil {
		return nil, e
	}
	switch {
	case a.Invite != nil: // a one-time invitation enrolls the key of the nick, or replaces it
		var e *proto.Error
		if nk, pub, e = b.joinByInvite(ch, c, a, nk); e != nil {
			return nil, e
		}
	case signed: // the owner, under a nick that is not anybody's protected one: made on its first entry
		if nk != nil && nk.pub != "" {
			return nil, proto.Errf("name_taken", "nick @%s is a protected nick of this chat; the owner enters under a nick of its own", a.Name)
		}
		if nk == nil {
			fresh := &nick{seen: ch.nextID} // nothing is "unread" for a new nick
			if e := ch.save(store.Record{T: "nick", Nick: a.Name, ID: fresh.seen}); e != nil {
				return nil, e
			}
			nk = fresh
			ch.nicks[a.Name] = nk
			ch.rooms[proto.RoomCommon].members[a.Name] = true
		}
	default: // a protected nick: its key, after key_unlock (protectedJoinCheck has seen to that and to the key)
		var e *proto.Error
		if pub, e = b.verifyNick(c, id, a); e != nil {
			return nil, e
		}
		if nk == nil || nk.pub != a.Pub {
			return nil, proto.Errf("name_taken", "nick @%s is registered in this chat under another key", a.Name)
		}
	}

	m := ch.members[a.Name]
	if m != nil && m.conn != nil {
		return nil, proto.Errf("name_online", "nick @%s is already online in this chat", a.Name)
	}
	backlog, total := ch.unread(a.Name, nk.seen)
	acks := a.Proto >= 2
	if m != nil {
		m.conn, m.offline, m.acks, m.proto, m.owner, m.pub = c, time.Time{}, acks, a.Proto, signed, pub
		b.presence(ch, nil, m.name, m.name+" is back")
	} else {
		m = &member{name: a.Name, conn: c, acks: acks, proto: a.Proto, owner: signed, pub: pub}
		ch.members[m.name] = m
		b.presence(ch, nil, m.name, m.name+" joined")
	}
	if nk.pinned != "" && m.role != nk.pinned { // the owner's role comes back with the member
		b.release(ch, m)
		b.holdRole(ch, m, nk.pinned)
	}
	c.ch, c.m = ch, m
	return proto.Welcome{State: ch.state(m), Backlog: backlog, UnreadTotal: total, Warning: b.warning, Proto: proto.ProtoVersion, Origin: b.origin}, nil
}

// unread: messages visible to the nick, newer than seen and not its own; newest unreadCap of them.
func (ch *chat) unread(name string, seen int64) ([]proto.Event, int) {
	var msgs []proto.Message
	for _, k := range ch.threadsOf(name) {
		for _, msg := range ch.threadMsgs(k) {
			if msg.ID > seen && msg.From != name {
				msgs = append(msgs, msg)
			}
		}
	}
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].ID < msgs[j].ID })
	total := len(msgs)
	if len(msgs) > unreadCap {
		msgs = msgs[len(msgs)-unreadCap:]
	}
	evs := make([]proto.Event, len(msgs))
	for i := range msgs {
		toMe := msgs[i].Room == proto.RoomDirect || msgs[i].To == name
		evs[i] = proto.Event{Kind: "msg", Msg: &msgs[i], ToMe: toMe}
	}
	return evs, total
}
