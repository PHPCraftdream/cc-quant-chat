package broker

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"sort"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// A nick is registered on first join and bound to a random key returned to that session.
// Rejoining under the nick requires the key, so nobody else can read its direct messages.

func newNickKey() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashNickKey(key string) string {
	h := sha256.Sum256([]byte("ccqc/v1/nick|" + key))
	return hex.EncodeToString(h[:])
}

func (n *nick) check(key string) bool {
	if key == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(hashNickKey(key)), []byte(n.hash)) == 1
}

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
	name, id, e = b.chatIDOf(a)
	if e != nil {
		return "", "", false, e
	}
	if a.Token != "" && b.entryByInviteOnly() && !b.chatKnown(id) {
		if _, ok := b.store.(store.Invites); ok {
			return "", "", false, proto.Errf("no_such_chat", "chats are made by the owner of this machine (ccqc chat new); this token belongs to no chat here")
		}
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
	var issued string
	var pub ed25519.PublicKey
	nk := ch.nicks[a.Name]
	if e := b.protectedJoinCheck(ch, c, a, nk, signed); e != nil {
		return nil, e
	}
	switch {
	case a.Enroll != nil && a.Invite == nil: // proto 6: the key of a nick that exists moves to a protected one (keys_move.go)
		var e *proto.Error
		if nk, pub, e = b.joinMove(ch, c, a, nk); e != nil {
			return nil, e
		}
	case a.Invite != nil: // proto 5: a one-time invitation registers the key, or replaces the nick's
		var e *proto.Error
		if nk, pub, e = b.joinByInvite(ch, c, a, nk); e != nil {
			return nil, e
		}
	case a.Pub != "": // proto 4: the nick is its Ed25519 key
		var e *proto.Error
		if pub, e = b.verifyNick(c, id, a); e != nil {
			return nil, e
		}
		if nk, e = b.bindPub(ch, a, nk, signed); e != nil {
			return nil, e
		}
	case nk != nil && nk.pub != "":
		return nil, proto.Errf("name_taken", "nick @%s joins with a signing key and this client has none: update ccqc (the secret alone no longer opens it)", a.Name)
	case (nk == nil || nk.hash == "") && b.entryByInviteOnly() && !signed: // by token, with an owner: by invitation only
		return nil, inviteRequired(ch, a.Name)
	case nk == nil || nk.hash == "": // new nick, or one freed by `ccqc nick reset`
		// Bind it to the presented key, else a fresh one.
		key := a.Key
		if key == "" {
			key = newNickKey()
		}
		hash := hashNickKey(key)
		if nk == nil {
			// One record: the store registers the nick and its place in @common atomically.
			fresh := &nick{hash: hash, seen: ch.nextID} // nothing is "unread" for a new nick
			if e := ch.save(store.Record{T: "nick", Nick: a.Name, Hash: hash, ID: fresh.seen}); e != nil {
				return nil, e
			}
			nk = fresh
			ch.nicks[a.Name] = nk
			ch.rooms[proto.RoomCommon].members[a.Name] = true
		} else {
			if e := ch.save(store.Record{T: "nick_key", Nick: a.Name, Hash: hash}); e != nil {
				return nil, e
			}
			nk.hash = hash
		}
		if a.Key == "" {
			issued = key
		}
	case !nk.check(a.Key):
		return nil, proto.Errf("name_taken", "nick @%s is registered in this chat by another session; pick another nick (or, if it is yours and its key was lost, `ccqc nick reset - %s` (token on stdin))", a.Name, a.Name)
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
	return proto.Welcome{State: ch.state(m), Backlog: backlog, UnreadTotal: total, Key: issued, Warning: b.warnFor(a.Proto), Proto: proto.ProtoVersion, Origin: b.origin}, nil
}

// bindPub binds the key a client proved to hold to its nick. A new nick is registered with it; a
// legacy nick (or one freed by `ccqc nick reset`) moves to it once, a legacy nick only for the
// holder of its old secret; a nick that has a key takes only that key.
func (b *Broker) bindPub(ch *chat, a proto.JoinArgs, nk *nick, owner bool) (*nick, *proto.Error) {
	taken := func(why string) *proto.Error {
		return proto.Errf("name_taken", "nick @%s is registered in this chat %s; pick another nick (or, if it is yours and its key was lost, `ccqc nick reset - %s` (token on stdin))", a.Name, why, a.Name)
	}
	// Once the machine has an owner, a new nick and a nick that moves to a key are made by invitation
	// only; the owner enters as before (it proves itself by its key).
	byToken := b.entryByInviteOnly() && !owner
	switch {
	case nk == nil && byToken:
		return nil, inviteRequired(ch, a.Name)
	case nk != nil && nk.pub == "" && byToken && nk.pub != a.Pub:
		return nil, inviteRequired(ch, a.Name)
	case nk == nil:
		fresh := &nick{pub: a.Pub, seen: ch.nextID} // nothing is "unread" for a new nick
		if e := ch.save(store.Record{T: "nick", Nick: a.Name, Pub: a.Pub, ID: fresh.seen}); e != nil {
			return nil, e
		}
		ch.nicks[a.Name] = fresh
		ch.rooms[proto.RoomCommon].members[a.Name] = true
		return fresh, nil
	case nk.pub == a.Pub:
		return nk, nil
	case nk.pub != "":
		return nil, taken("under another key")
	case nk.hash != "" && !nk.check(a.Key):
		return nil, taken("by another session (moving it to a key takes its old secret)")
	}
	if e := ch.save(store.Record{T: "nick_key", Nick: a.Name, Hash: nk.hash, Pub: a.Pub}); e != nil {
		return nil, e
	}
	nk.pub = a.Pub
	return nk, nil
}

// nickReset frees a registered nick whose key is lost: the next join binds a new key, and the
// nick keeps its history and rooms. Needs the chat token; refused while the nick is present.
// Anyone holding the token can do this to an offline nick, so the chat is told when it happens.
func (b *Broker) nickReset(a proto.NickResetArgs) (any, *proto.Error) {
	name, id, err := proto.ParseToken(a.Token)
	if err != nil {
		return nil, proto.Errf("bad_token", "%v", err)
	}
	ch, err := b.useChat(id, name)
	if err != nil {
		return nil, errNoStore(err)
	}
	defer func() {
		b.doneChat(ch)
		b.unloadIfEmpty(ch)
	}()
	nk := ch.nicks[a.Name]
	if nk == nil {
		return nil, proto.Errf("no_such_member", "no nick @%s in this chat", a.Name)
	}
	if ch.members[a.Name] != nil {
		return nil, proto.Errf("name_online", "@%s is online or was online a moment ago; reset works for an offline nick (wait about a minute)", a.Name)
	}
	if e := ch.save(store.Record{T: "nick_key", Nick: a.Name}); e != nil {
		return nil, e
	}
	nk.hash, nk.pub = "", ""
	b.dropSecrets(ch.id, a.Name)
	b.presence(ch, nil, "", "the key of @"+a.Name+" was reset: the next join under this nick takes it over")
	return nil, nil
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
