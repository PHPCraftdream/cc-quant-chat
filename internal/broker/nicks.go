package broker

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"sort"

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
	ch := b.chats[id]
	if ch == nil {
		ch = b.loadChat(id, name)
		b.chats[id] = ch
	}

	var issued string
	nk := ch.nicks[a.Name]
	switch {
	case nk == nil:
		// New nick (or the broker forgot it): bind it to the presented key, else a fresh one.
		key := a.Key
		if key == "" {
			key = newNickKey()
			issued = key
		}
		nk = &nick{hash: hashNickKey(key)}
		ch.nicks[a.Name] = nk
		ch.rooms[proto.RoomCommon].members[a.Name] = true
		ch.save(store.Record{T: "nick", Nick: a.Name, Hash: nk.hash})
		ch.save(store.Record{T: "in", Nick: a.Name, Room: proto.RoomCommon})
	case !nk.check(a.Key):
		return nil, proto.Errf("name_taken", "nick @%s is registered in this chat by another session; pick another nick", a.Name)
	}

	m := ch.members[a.Name]
	if m != nil && m.conn != nil {
		return nil, proto.Errf("name_taken", "nick @%s is already online in this chat", a.Name)
	}
	backlog, total := ch.unread(a.Name, nk.seen)
	if m != nil {
		m.conn, m.offline = c, zeroTime
		b.presence(ch, nil, m.name, m.name+" is back")
	} else {
		m = &member{name: a.Name, conn: c}
		ch.members[m.name] = m
		b.presence(ch, nil, m.name, m.name+" joined")
	}
	c.ch, c.m = ch, m
	return proto.Welcome{State: ch.state(m), Backlog: backlog, UnreadTotal: total, Key: issued}, nil
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
