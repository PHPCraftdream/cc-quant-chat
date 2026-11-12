package broker

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

func eventFrame(ev proto.Event) proto.Frame {
	raw, _ := json.Marshal(ev)
	return proto.Frame{Op: "event", Data: raw}
}

// deliver pushes to an online member; others catch up from history on their next join.
func (b *Broker) deliver(ch *chat, name string, ev proto.Event) {
	if m := ch.members[name]; m != nil && m.conn != nil {
		m.conn.push(eventFrame(ev))
	}
}

// presence notifies online members (of room r, if set) except actor.
func (b *Broker) presence(ch *chat, r *room, actor, text string) {
	f := eventFrame(proto.Event{Kind: "presence", Text: text})
	for name, m := range ch.members {
		if name == actor || m.conn == nil {
			continue
		}
		if r != nil && !r.members[name] {
			continue
		}
		m.conn.push(f)
	}
}

// state lists every registered nick; role and online status come from presence.
func (ch *chat) state(me *member) proto.State {
	st := proto.State{Chat: ch.name, Me: me.name, MyRole: me.role, MyRooms: []string{}}
	for _, n := range sortedKeys(ch.nicks) {
		mi := proto.MemberInfo{Name: n}
		if nk := ch.nicks[n]; nk != nil && nk.pub == "" && n != proto.OwnerNick {
			mi.Unsigned = true // no signing key: its events are stored unsigned
		}
		if nk := ch.nicks[n]; nk != nil && nk.pub != "" && n != proto.OwnerNick && ch.plainKey(n, nk) {
			mi.KeyPlain = true
		}
		if m := ch.members[n]; m != nil {
			mi.Role, mi.Online = m.role, m.conn != nil
			m.fill(&mi)
			mi.Owner = m.owner
			if m.owner {
				mi.Unsigned = false // signs with the owner key
				mi.KeyPlain = false
			}
			if nkk := ch.nicks[n]; nkk != nil && nkk.pinned != "" && m.role == nkk.pinned {
				mi.RolePinned = true
			}
			if r := ch.roles[m.role]; r != nil {
				mi.RoleExclusive = r.exclusive
			}
		} else if nkk := ch.nicks[n]; nkk != nil && nkk.pinned != "" {
			mi.Role, mi.RolePinned = nkk.pinned, true // a pinned role is shown even while the member is away
		}
		st.Members = append(st.Members, mi)
	}
	names := sortedKeys(ch.rooms)
	sort.SliceStable(names, func(i, j int) bool { return names[i] == proto.RoomCommon && names[j] != proto.RoomCommon })
	for _, n := range names {
		r := ch.rooms[n]
		st.Rooms = append(st.Rooms, ch.roomInfo(r))
		if r.members[me.name] {
			st.MyRooms = append(st.MyRooms, n)
		}
	}
	return st
}

func (ch *chat) roomInfo(r *room) proto.RoomInfo {
	ri := proto.RoomInfo{Name: r.name, Topic: r.topic, CreatedBy: r.createdBy}
	if r.name != proto.RoomCommon {
		ri.Members = sortedKeys(r.members)
	}
	return ri
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func pairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "|" + b
}

// keep adds msg to its thread in memory and indexes it by id.
func (ch *chat) keep(key string, msg proto.Message) {
	switch {
	case strings.HasPrefix(key, "r:"):
		r := ch.rooms[key[2:]]
		if r == nil {
			return
		}
		r.hist = ch.capped(r.hist, msg)
	case strings.HasPrefix(key, "d:"):
		ch.dms[key[2:]] = ch.capped(ch.dms[key[2:]], msg)
	default:
		return
	}
	ch.where[msg.ID] = key
}

// capped appends and trims a thread to histCap, dropping trimmed ids from the index.
func (ch *chat) capped(s []proto.Message, msg proto.Message) []proto.Message {
	s = append(s, msg)
	if len(s) > histCap {
		for _, old := range s[:len(s)-histCap] {
			delete(ch.where, old.ID)
		}
		s = append([]proto.Message(nil), s[len(s)-histCap:]...)
	}
	return s
}

func tail(s []proto.Message, n int) []proto.Message {
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return append([]proto.Message{}, s...)
}

// plainKey: the nick has a key and the broker, which keeps protected keys, holds none for it: the key is
// a plain one. False when the store keeps no secrets at all (nothing is protected there).
func (ch *chat) plainKey(name string, nk *nick) bool {
	ss, ok := ch.store.(store.NickSecrets)
	if !ok {
		return false
	}
	row, err := ss.ActiveNickSecret(ch.id, name)
	return err == nil && (row == nil || row.SignPub != nk.pub)
}
