package broker

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// changes: ops that alter state; a stopping broker refuses them so the client repeats them
// against the next broker.
var changes = map[string]bool{
	"join": true, "send": true, "room_create": true, "room_join": true, "room_leave": true,
	"role_claim": true, "role_release": true, "nick_reset": true, "owner_register": true, "owner_rotate": true,
}

func (b *Broker) handle(c *conn, f *proto.Frame) (any, *proto.Error) {
	if changes[f.Op] && b.stopping.Load() {
		return nil, proto.Errf("restarting", "the broker is restarting; repeat in a moment")
	}
	switch f.Op {
	case "ping":
		return b.info(), nil
	case "shutdown":
		// Reply first, then stop: the caller (ccqc broker stop/restart) waits for the pipe to go.
		go func() {
			time.Sleep(200 * time.Millisecond)
			b.shutdown("requested")
		}()
		return nil, nil
	case "join":
		var a proto.JoinArgs
		if e := decode(f.Args, &a); e != nil {
			return nil, e
		}
		return b.join(c, a)
	case "challenge":
		return b.challenge(c)
	case "owner_info":
		return b.ownerInfo(), nil
	case "owner_register":
		var a proto.OwnerRegisterArgs
		if e := decode(f.Args, &a); e != nil {
			return nil, e
		}
		return b.ownerRegister(a)
	case "owner_rotate":
		var a proto.OwnerRotateArgs
		if e := decode(f.Args, &a); e != nil {
			return nil, e
		}
		return b.ownerRotate(a)
	case "nick_reset":
		var a proto.NickResetArgs
		if e := decode(f.Args, &a); e != nil {
			return nil, e
		}
		return b.nickReset(a)
	}
	if c.m == nil {
		return nil, proto.Errf("not_joined", "join first")
	}
	ch, m := c.ch, c.m
	switch f.Op {
	case "room_create", "room_join", "room_leave", "role_claim", "role_release":
		if !b.allow(ch, m.name, "ctl", ctlLimit) {
			return nil, proto.Errf("rate_limited", "max %d room/role changes per minute", ctlLimit)
		}
	}
	switch f.Op {
	case "leave":
		if !m.acks {
			ch.markSeen(m.name)
		}
		b.removeMember(ch, m, m.name+" left")
		c.m = nil
		return nil, nil
	case "ack":
		var a proto.AckArgs
		if e := decode(f.Args, &a); e != nil {
			return nil, e
		}
		return nil, ch.ack(m.name, a.ID)
	case "state":
		return ch.state(m), nil
	case "status":
		var a proto.StatusArgs
		if e := decode(f.Args, &a); e != nil {
			return nil, e
		}
		return nil, b.setStatus(ch, m, a)
	case "send":
		var a proto.SendArgs
		if e := decode(f.Args, &a); e != nil {
			return nil, e
		}
		return b.send(ch, m, a)
	case "history":
		var a proto.HistoryArgs
		if e := decode(f.Args, &a); e != nil {
			return nil, e
		}
		return ch.history(m, a)
	case "timeline":
		var a proto.TimelineArgs
		if e := decode(f.Args, &a); e != nil {
			return nil, e
		}
		return ch.timeline(m, a)
	case "search":
		var a proto.SearchArgs
		if e := decode(f.Args, &a); e != nil {
			return nil, e
		}
		return ch.search(m, a)
	case "context":
		var a proto.ContextArgs
		if e := decode(f.Args, &a); e != nil {
			return nil, e
		}
		return ch.context(m, a)
	case "room_create", "room_join", "room_leave":
		var a proto.RoomArgs
		if e := decode(f.Args, &a); e != nil {
			return nil, e
		}
		return b.roomOp(f.Op, ch, m, a)
	case "role_claim":
		var a proto.RoleArgs
		if e := decode(f.Args, &a); e != nil {
			return nil, e
		}
		return b.claim(ch, m, a)
	case "role_release":
		if old := b.release(ch, m); old != "" {
			b.presence(ch, nil, m.name, fmt.Sprintf("%s released role %s", m.name, old))
		}
		return nil, nil
	}
	return nil, proto.Errf("unknown_op", "%s", f.Op)
}

func (b *Broker) send(ch *chat, m *member, a proto.SendArgs) (any, *proto.Error) {
	if len(a.ClientMsgID) > maxClientID {
		return nil, proto.Errf("bad_args", "client_msg_id is longer than %d bytes", maxClientID)
	}
	if id, ok := ch.sentID(m.name, a.ClientMsgID); ok { // a repeat of a send that went through
		return proto.SendResult{ID: id, Dup: true}, nil
	}
	if strings.TrimSpace(a.Text) == "" {
		return nil, proto.Errf("empty", "text is empty")
	}
	if len(a.Text) > proto.MaxText {
		return nil, proto.Errf("too_large", "text is %d bytes, max %d", len(a.Text), proto.MaxText)
	}
	now := time.Now()
	if !b.allow(ch, m.name, "msg", rateLimit) {
		return nil, proto.Errf("rate_limited", "max %d messages per minute", rateLimit)
	}
	if a.To != "" && ch.nicks[a.To] == nil {
		return nil, proto.Errf("no_such_member", "no nick @%s in this chat", a.To)
	}
	msg := proto.Message{
		Room: a.Room, From: m.name, FromRole: m.role, To: a.To,
		Text: a.Text, ReplyTo: a.ReplyTo, TS: now.UTC().Format(time.RFC3339), Owner: m.owner,
	}
	var key string
	var recipients []string
	if a.Room == proto.RoomDirect {
		if a.To == "" {
			return nil, proto.Errf("member_required", "@direct needs a nick")
		}
		if a.To == m.name {
			return nil, proto.Errf("no_such_member", "can't send @direct to yourself")
		}
		key = "d:" + pairKey(m.name, a.To)
		recipients = []string{a.To}
	} else {
		r := ch.rooms[a.Room]
		if r == nil {
			return nil, proto.Errf("no_such_room", "no room @%s", a.Room)
		}
		if !r.members[m.name] {
			return nil, proto.Errf("not_in_room", "you are not in @%s; room_join first", a.Room)
		}
		if a.To != "" && !r.members[a.To] {
			return nil, proto.Errf("not_in_room", "@%s is not in @%s", a.To, a.Room)
		}
		key = "r:" + r.name
		for name := range r.members {
			if name != m.name {
				recipients = append(recipients, name)
			}
		}
	}
	msg.ID = ch.nextID + 1
	var dup *store.DuplicateError
	if e := ch.saveMsg(store.Record{T: "msg", Key: key, Msg: &msg, ClientID: a.ClientMsgID}, &dup); e != nil {
		return nil, e // not stored: no id taken, nothing delivered
	}
	if dup != nil { // stored by an earlier attempt (the database knows, e.g. across a broker restart)
		b.refund(ch, m.name, "msg")
		ch.rememberSent(m.name, a.ClientMsgID, dup.ID)
		return proto.SendResult{ID: dup.ID, Dup: true}, nil
	}
	ch.rememberSent(m.name, a.ClientMsgID, msg.ID)
	ch.nextID = msg.ID
	ch.keep(key, msg)
	for _, name := range recipients {
		b.deliver(ch, name, proto.Event{Kind: "msg", Msg: &msg, ToMe: a.Room == proto.RoomDirect || name == a.To})
	}
	return proto.SendResult{ID: msg.ID}, nil
}

func (b *Broker) roomOp(op string, ch *chat, m *member, a proto.RoomArgs) (any, *proto.Error) {
	if !proto.ValidName(a.Room) {
		return nil, proto.Errf("bad_name", "room name must match [a-z0-9-]{1,32}: %q", a.Room)
	}
	if a.Room == proto.RoomDirect {
		return nil, proto.Errf("reserved", "@direct is not a room")
	}
	r := ch.rooms[a.Room]
	switch op {
	case "room_create":
		if a.Room == proto.RoomCommon {
			return nil, proto.Errf("reserved", "@common always exists")
		}
		if r != nil {
			return nil, proto.Errf("room_exists", "@%s already exists; room_join it", a.Room)
		}
		if len(a.Topic) > proto.MaxTopic {
			return nil, proto.Errf("too_large", "topic is %d bytes, max %d", len(a.Topic), proto.MaxTopic)
		}
		if len(ch.rooms) >= maxRooms {
			return nil, proto.Errf("too_many_rooms", "a chat holds at most %d rooms", maxRooms)
		}
		if e := ch.save(store.Record{T: "room", Room: a.Room, Topic: a.Topic, Nick: m.name}); e != nil {
			return nil, e
		}
		if e := ch.save(store.Record{T: "in", Room: a.Room, Nick: m.name}); e != nil {
			return nil, e
		}
		r = &room{name: a.Room, topic: a.Topic, createdBy: m.name, members: map[string]bool{m.name: true}}
		ch.rooms[a.Room] = r
		text := fmt.Sprintf("%s created room @%s", m.name, a.Room)
		if a.Topic != "" {
			text += " — " + a.Topic
		}
		b.presence(ch, nil, m.name, text)
		return ch.roomInfo(r), nil
	case "room_join":
		if r == nil {
			return nil, proto.Errf("no_such_room", "no room @%s", a.Room)
		}
		if !r.members[m.name] {
			if e := ch.save(store.Record{T: "in", Room: a.Room, Nick: m.name}); e != nil {
				return nil, e
			}
			r.members[m.name] = true
			b.presence(ch, r, m.name, fmt.Sprintf("%s joined @%s", m.name, a.Room))
		}
		return proto.HistoryResult{Messages: tail(r.hist, joinRecent)}, nil
	case "room_leave":
		if a.Room == proto.RoomCommon {
			return nil, proto.Errf("cannot_leave_common", "@common can't be left")
		}
		if r == nil {
			return nil, proto.Errf("no_such_room", "no room @%s", a.Room)
		}
		if !r.members[m.name] {
			return nil, proto.Errf("not_in_room", "you are not in @%s", a.Room)
		}
		if e := ch.save(store.Record{T: "out", Room: a.Room, Nick: m.name}); e != nil {
			return nil, e
		}
		delete(r.members, m.name)
		b.presence(ch, r, m.name, fmt.Sprintf("%s left @%s", m.name, a.Room))
		return nil, nil
	}
	return nil, proto.Errf("unknown_op", "%s", op)
}

func (b *Broker) claim(ch *chat, m *member, a proto.RoleArgs) (any, *proto.Error) {
	if !proto.ValidName(a.Role) {
		return nil, proto.Errf("bad_name", "role must match [a-z0-9-]{1,32}: %q", a.Role)
	}
	r := ch.roles[a.Role]
	if r != nil && len(r.holders) > 0 && !(len(r.holders) == 1 && r.holders[m.name]) {
		held := strings.Join(sortedKeys(r.holders), ", ")
		if r.exclusive {
			return nil, proto.Errf("role_taken", "role %q is exclusive, held by %s", a.Role, held)
		}
		if a.Exclusive {
			return nil, proto.Errf("role_taken", "role %q is shared, already held by %s; claim without exclusive", a.Role, held)
		}
	}
	if m.role != "" && m.role != a.Role {
		b.release(ch, m)
	}
	if r == nil || len(r.holders) == 0 {
		r = &role{holders: map[string]bool{}}
		ch.roles[a.Role] = r
	}
	if len(r.holders) == 0 || (len(r.holders) == 1 && r.holders[m.name]) {
		r.exclusive = a.Exclusive
	}
	r.holders[m.name] = true
	m.role = a.Role
	kind := "shared"
	if r.exclusive {
		kind = "exclusive"
	}
	b.presence(ch, nil, m.name, fmt.Sprintf("%s took role %s (%s)", m.name, a.Role, kind))
	return ch.state(m), nil
}

func (b *Broker) release(ch *chat, m *member) string {
	old := m.role
	if old == "" {
		return ""
	}
	if r := ch.roles[old]; r != nil {
		delete(r.holders, m.name)
		if len(r.holders) == 0 {
			delete(ch.roles, old)
		}
	}
	m.role = ""
	return old
}

// removeMember ends presence: role released; nick, rooms and history stay.
// A chat with nobody present is unloaded; the store backend keeps what it persists.
func (b *Broker) removeMember(ch *chat, m *member, text string) {
	b.release(ch, m)
	delete(ch.members, m.name)
	b.presence(ch, nil, m.name, text)
	b.unloadIfEmpty(ch)
}

func (b *Broker) unloadIfEmpty(ch *chat) {
	if len(ch.members) == 0 {
		if _, memoryOnly := ch.store.(store.Memory); !memoryOnly { // memory-only: unloading would lose history
			delete(b.chats, ch.id)
			log.Printf("broker: chat %s#%s unloaded (nobody present)", ch.name, ch.id[:8])
		}
	}
}
