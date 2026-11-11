package mcp

import (
	"fmt"
	"github.com/PHPCraftdream/cc-quant-chat/internal/addr"
	"github.com/PHPCraftdream/cc-quant-chat/internal/local"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"sort"
	"strings"
)

func (s *Session) send(to, text string, replyTo int64) (string, error) {
	a, err := addr.Parse(to)
	if err != nil {
		return "", err
	}
	cc, err := s.get(a.Chat)
	if err != nil {
		return "", err
	}
	var r proto.SendResult
	if err := cc.call("send", proto.SendArgs{Room: a.Room, To: a.Member, Text: text, ReplyTo: replyTo}, &r); err != nil {
		return "", err
	}
	return fmt.Sprintf("sent #%d to %s", r.ID, a), nil
}

func (s *Session) state(chat string) (string, error) {
	var list []*chatConn
	if strings.TrimSpace(chat) != "" {
		cc, err := s.get(chat)
		if err != nil {
			return "", err
		}
		list = []*chatConn{cc}
	} else {
		s.mu.Lock()
		for _, cc := range s.chats {
			list = append(list, cc)
		}
		s.mu.Unlock()
		if len(list) == 0 {
			return "not joined to any chat; the user should run /quant-join <token> [name]", nil
		}
		sort.Slice(list, func(i, j int) bool { return list[i].local < list[j].local })
	}
	var b strings.Builder
	for i, cc := range list {
		if i > 0 {
			b.WriteString("\n")
		}
		var st proto.State
		if err := cc.call("state", nil, &st); err != nil {
			fmt.Fprintf(&b, "/%s: %v\n", cc.local, err)
			continue
		}
		b.WriteString(renderState(cc.local, &st))
	}
	return b.String(), nil
}

func (s *Session) leave(chat string) (string, error) {
	cc, err := s.get(chat)
	if err != nil {
		return "", err
	}
	cc.mu.Lock()
	cc.leaving = true
	conn := cc.conn
	if cc.presT != nil {
		cc.presT.Stop()
	}
	cc.mu.Unlock()
	if conn != nil {
		_ = conn.Call("leave", nil, nil)
		conn.Close()
	}
	s.mu.Lock()
	delete(s.chats, cc.local)
	s.mu.Unlock()
	if err := local.ForgetChat(local.ProjectDir(), cc.local); err != nil {
		return "", fmt.Errorf("left /%s, but could not drop it from this folder's auto-join: %v", cc.local, err)
	}
	return "left /" + cc.local + " (no longer joined automatically in this folder)", nil
}

func (s *Session) roomOp(op, room, topic string) (string, error) {
	a, err := addr.Parse(room)
	if err != nil {
		return "", err
	}
	if a.Member != "" || a.Room == proto.RoomDirect {
		return "", fmt.Errorf("room is /%s/@<room>", a.Chat)
	}
	cc, err := s.get(a.Chat)
	if err != nil {
		return "", err
	}
	switch op {
	case "room_create":
		if err := cc.call(op, proto.RoomArgs{Room: a.Room, Topic: topic}, nil); err != nil {
			return "", err
		}
		cc.setRoom(a.Room, true)
		return "created and joined " + a.String(), nil
	case "room_join":
		var r proto.HistoryResult
		if err := cc.call(op, proto.RoomArgs{Room: a.Room}, &r); err != nil {
			return "", err
		}
		cc.setRoom(a.Room, true)
		var b strings.Builder
		b.WriteString("joined " + a.String() + "\n")
		if len(r.Messages) > 0 {
			b.WriteString("recent:\n")
			for i := range r.Messages {
				b.WriteString("  " + fmtMsg(cc.local, &r.Messages[i]) + "\n")
			}
		}
		return b.String(), nil
	default:
		if err := cc.call(op, proto.RoomArgs{Room: a.Room}, nil); err != nil {
			return "", err
		}
		cc.setRoom(a.Room, false)
		return "left " + a.String(), nil
	}
}

func (s *Session) roleClaim(chat, role string, exclusive bool) (string, error) {
	cc, err := s.get(chat)
	if err != nil {
		return "", err
	}
	role = strings.TrimPrefix(strings.TrimSpace(role), "@")
	if err := cc.call("role_claim", proto.RoleArgs{Role: role, Exclusive: exclusive}, nil); err != nil {
		return "", err
	}
	cc.mu.Lock()
	cc.role, cc.excl = role, exclusive
	cc.mu.Unlock()
	kind := "shared"
	if exclusive {
		kind = "exclusive"
	}
	return fmt.Sprintf("you hold role %s (%s) in /%s", role, kind, cc.local), nil
}

func (s *Session) roleRelease(chat string) (string, error) {
	cc, err := s.get(chat)
	if err != nil {
		return "", err
	}
	if err := cc.call("role_release", nil, nil); err != nil {
		return "", err
	}
	cc.mu.Lock()
	cc.role, cc.excl = "", false
	cc.mu.Unlock()
	return "role released in /" + cc.local, nil
}
