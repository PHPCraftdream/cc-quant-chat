package mcp

import (
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/client"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// chatConn: membership in one chat. Token lives only in memory, used to rejoin after broker loss.
type chatConn struct {
	s     *Session
	local string
	token string
	name  string
	key   string // nick key

	mu      sync.Mutex
	conn    *client.Conn
	role    string
	excl    bool
	rooms   map[string]bool
	leaving bool
	pres    []string
	presT   *time.Timer

	note string // the status_set text, repeated after a reconnect

	last  int64 // newest message id handed to the session (dedupe)
	done  int64 // newest message id confirmed delivered
	acked int64 // newest id the broker has acknowledged
}

func (cc *chatConn) call(op string, args, out any) error {
	cc.mu.Lock()
	conn := cc.conn
	cc.mu.Unlock()
	if conn == nil {
		return errors.New("reconnecting to broker, retry in a few seconds")
	}
	return conn.Call(op, args, out)
}

func (cc *chatConn) setRoom(room string, in bool) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if in {
		cc.rooms[room] = true
	} else {
		delete(cc.rooms, room)
	}
}

func (cc *chatConn) isLeaving() bool {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	return cc.leaving
}

func (cc *chatConn) run(conn *client.Conn) {
	for {
		cc.pump(conn)
		if cc.isLeaving() {
			return
		}
		cc.mu.Lock()
		cc.conn = nil
		cc.mu.Unlock()
		cc.presence("connection to broker lost, reconnecting")
		if conn = cc.reconnect(); conn == nil {
			return
		}
	}
}

func (cc *chatConn) pump(conn *client.Conn) {
	for {
		select {
		case ev := <-conn.Events:
			cc.onEvent(ev)
		case <-conn.Done():
			for {
				select {
				case ev := <-conn.Events:
					cc.onEvent(ev)
				default:
					return
				}
			}
		}
	}
}

func (cc *chatConn) reconnect() *client.Conn {
	delay := 500 * time.Millisecond
	for !cc.isLeaving() {
		time.Sleep(delay)
		if delay < 10*time.Second {
			delay *= 2
		}
		conn, err := client.ConnectWaiting(restartWait) // give `ccqc broker restart` time to bring up the new broker
		if err != nil {
			log.Printf("reconnect /%s: %v", cc.local, err)
			continue
		}
		var w proto.Welcome
		if err := conn.Call("join", proto.JoinArgs{Token: cc.token, Name: cc.name, Key: cc.key, Proto: proto.ProtoVersion}, &w); err != nil {
			conn.Close()
			log.Printf("rejoin /%s: %v", cc.local, err)
			if permanentJoinError(err) {
				cc.giveUp(err)
				return nil
			}
			continue
		}
		cc.restore(conn, &w.State)
		cc.mu.Lock()
		if cc.leaving {
			cc.mu.Unlock()
			conn.Close()
			return nil
		}
		cc.conn = conn
		cc.mu.Unlock()
		for _, ev := range w.Backlog {
			cc.onEvent(ev)
		}
		go cc.flushAck()                  // a confirmation lost with the old connection
		go cc.pushStatus(cc.fullStatus()) // the new broker knows nothing of our status
		cc.presence("reconnected to broker")
		return conn
	}
	return nil
}

// permanentJoinError: retrying can't help. name_online (the old connection is still closing),
// store errors and a missing broker are transient and keep being retried.
func permanentJoinError(err error) bool {
	for _, code := range []string{"bad_token", "bad_name", "name_taken", "already_joined"} {
		if proto.IsCode(err, code) {
			return true
		}
	}
	return false
}

// giveUp ends the membership after a permanent rejoin error and says so loudly.
func (cc *chatConn) giveUp(err error) {
	cc.mu.Lock()
	cc.leaving = true
	cc.mu.Unlock()
	cc.s.mu.Lock()
	if cc.s.chats[cc.local] == cc {
		delete(cc.s.chats, cc.local)
	}
	cc.s.mu.Unlock()
	msg := fmt.Sprintf("[/%s] you are no longer in this chat: rejoining as @%s failed for good: %v. "+
		"Tell the user; after fixing it they can join again with /quant-join.", cc.local, cc.name, err)
	cc.s.emit(msg, map[string]string{"kind": "error", "chat": cc.local}, msg, false, ackRef{})
}

// restore re-enters rooms and role after the broker lost them (e.g. broker restart).
func (cc *chatConn) restore(conn *client.Conn, st *proto.State) {
	cc.mu.Lock()
	rooms := make([]string, 0, len(cc.rooms))
	for r := range cc.rooms {
		rooms = append(rooms, r)
	}
	role, excl := cc.role, cc.excl
	cc.mu.Unlock()
	have := set(st.MyRooms)
	for _, r := range rooms {
		if have[r] {
			continue
		}
		err := conn.Call("room_join", proto.RoomArgs{Room: r}, nil)
		if proto.IsCode(err, "no_such_room") {
			err = conn.Call("room_create", proto.RoomArgs{Room: r}, nil)
		}
		if err != nil {
			log.Printf("restore room @%s: %v", r, err)
		}
	}
	if role != "" && st.MyRole != role {
		if err := conn.Call("role_claim", proto.RoleArgs{Role: role, Exclusive: excl}, nil); err != nil {
			cc.mu.Lock()
			cc.role, cc.excl = "", false
			cc.mu.Unlock()
			cc.presence(fmt.Sprintf("could not restore your role %s: %v", role, err))
		}
	}
}

func (cc *chatConn) onEvent(ev proto.Event) {
	switch ev.Kind {
	case "msg":
		if ev.Msg == nil {
			return
		}
		m := ev.Msg
		if !cc.fresh(m.ID) {
			return
		}
		kind := "msg"
		if m.Room == proto.RoomDirect {
			kind = "dm"
		}
		reply := replyAddr(cc.local, m, ev.ToMe)
		meta := map[string]string{
			"kind":   kind,
			"chat":   cc.local,
			"room":   m.Room,
			"from":   m.From,
			"msg_id": strconv.FormatInt(m.ID, 10),
			"reply":  reply,
		}
		if m.FromRole != "" {
			meta["from_role"] = m.FromRole
		}
		if m.To != "" {
			meta["to"] = m.To
		}
		if ev.ToMe {
			meta["to_me"] = "1"
		}
		if m.Owner {
			meta["owner"] = "1"
		}
		if m.ReplyTo > 0 {
			meta["reply_to"] = strconv.FormatInt(m.ReplyTo, 10)
		}
		cc.s.emit(m.Text, meta, eventLine(cc.local, m, ev.ToMe), false, ackRef{cc, m.ID})
	case "presence":
		cc.presence(ev.Text)
	}
}

func (cc *chatConn) presence(text string) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	cc.pres = append(cc.pres, text)
	if cc.presT == nil {
		cc.presT = time.AfterFunc(presenceDelay, cc.flushPresence)
	}
}

func (cc *chatConn) flushPresence() {
	cc.mu.Lock()
	lines, leaving := cc.pres, cc.leaving
	cc.pres, cc.presT = nil, nil
	cc.mu.Unlock()
	if len(lines) == 0 || leaving {
		return
	}
	cc.s.emit(strings.Join(lines, "\n"),
		map[string]string{"kind": "presence", "chat": cc.local},
		presenceLine(cc.local, lines), true, ackRef{})
}
