package mcp

import (
	"errors"
	"fmt"
	"github.com/PHPCraftdream/cc-quant-chat/internal/client"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
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
		conn, err := client.Connect()
		if err != nil {
			log.Printf("reconnect /%s: %v", cc.local, err)
			continue
		}
		var w proto.Welcome
		if err := conn.Call("join", proto.JoinArgs{Token: cc.token, Name: cc.name, Key: cc.key}, &w); err != nil {
			conn.Close()
			log.Printf("rejoin /%s: %v", cc.local, err)
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
		cc.presence("reconnected to broker")
		return conn
	}
	return nil
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
		if m.ReplyTo > 0 {
			meta["reply_to"] = strconv.FormatInt(m.ReplyTo, 10)
		}
		cc.s.emit(m.Text, meta, fmtMsg(cc.local, m)+"   (reply: "+reply+")", false)
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
		"[/"+cc.local+" presence] "+strings.Join(lines, "; "), true)
}
