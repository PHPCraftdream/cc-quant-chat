package broker

import (
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

func (b *Broker) join(c *conn, a proto.JoinArgs) (any, *proto.Error) {
	res, perr := b.joinChat(c, a)
	if perr != nil { // a chat loaded for a join that failed must not stay cached without members
		if _, id, err := proto.ParseToken(a.Token); err == nil {
			if ch := b.chats[id]; ch != nil {
				b.unloadIfEmpty(ch)
			}
		}
	}
	return res, perr
}

func (b *Broker) joinChat(c *conn, a proto.JoinArgs) (any, *proto.Error) {
	if c.m != nil {
		return nil, proto.Errf("already_joined", "this connection already joined a chat")
	}
	name, id, err := proto.ParseToken(a.Token)
	if err != nil {
		return nil, proto.Errf("bad_token", "%v", err)
	}
	if !proto.ValidName(a.Name) {
		return nil, proto.Errf("bad_name", "nick must match [a-z0-9-]{1,32}: %q", a.Name)
	}
	// The owner proves itself by signing the challenge; nothing else a client sends counts. Once
	// there is an owner, its nick is reserved for it.
	signed := false
	if a.OwnerSig != "" {
		if e := b.verifyOwner(c, id, a.Name, a.OwnerSig); e != nil {
			return nil, e
		}
		signed = true
	} else if a.Name == proto.OwnerNick && b.ownerPub != nil {
		return nil, proto.Errf("reserved", "the nick @%s is reserved for the owner of this machine", proto.OwnerNick)
	}
	ch := b.chats[id]
	if ch == nil {
		loaded, err := b.loadChat(id, name)
		if err != nil {
			return nil, proto.Errf("store_unavailable", "chat history can't be read (%v); not joining so nothing gets overwritten. See broker.log", err)
		}
		ch = loaded
		b.chats[id] = ch
	}

	var issued string
	nk := ch.nicks[a.Name]
	switch {
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
		m.conn, m.offline, m.acks, m.proto, m.owner = c, time.Time{}, acks, a.Proto, signed
		b.presence(ch, nil, m.name, m.name+" is back")
	} else {
		m = &member{name: a.Name, conn: c, acks: acks, proto: a.Proto, owner: signed}
		ch.members[m.name] = m
		b.presence(ch, nil, m.name, m.name+" joined")
	}
	if nk.pinned != "" && m.role != nk.pinned { // the owner's role comes back with the member
		b.release(ch, m)
		b.holdRole(ch, m, nk.pinned)
	}
	c.ch, c.m = ch, m
	return proto.Welcome{State: ch.state(m), Backlog: backlog, UnreadTotal: total, Key: issued, Warning: b.warnFor(a.Proto), Proto: proto.ProtoVersion}, nil
}

// nickReset frees a registered nick whose key is lost: the next join binds a new key, and the
// nick keeps its history and rooms. Needs the chat token; refused while the nick is present.
// Anyone holding the token can do this to an offline nick, so the chat is told when it happens.
func (b *Broker) nickReset(a proto.NickResetArgs) (any, *proto.Error) {
	name, id, err := proto.ParseToken(a.Token)
	if err != nil {
		return nil, proto.Errf("bad_token", "%v", err)
	}
	ch := b.chats[id]
	if ch == nil {
		loaded, err := b.loadChat(id, name)
		if err != nil {
			return nil, proto.Errf("store_unavailable", "chat history can't be read (%v)", err)
		}
		ch = loaded
		b.chats[id] = ch
		defer b.unloadIfEmpty(ch)
	}
	nk := ch.nicks[a.Name]
	if nk == nil {
		return nil, proto.Errf("no_such_member", "no nick @%s in this chat", a.Name)
	}
	if ch.members[a.Name] != nil {
		return nil, proto.Errf("name_online", "@%s is online or was online a moment ago; reset works for an offline nick (wait about a minute)", a.Name)
	}
	if e := ch.save(store.Record{T: "nick_key", Nick: a.Name, Hash: ""}); e != nil {
		return nil, e
	}
	nk.hash = ""
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
