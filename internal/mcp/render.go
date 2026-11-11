package mcp

import (
	"fmt"
	"strings"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

func replyAddr(local string, m *proto.Message, toMe bool) string {
	switch {
	case m.Room == proto.RoomDirect:
		return "/" + local + "/@direct/@" + m.From
	case toMe:
		return "/" + local + "/@" + m.Room + "/@" + m.From
	default:
		return "/" + local + "/@" + m.Room
	}
}

func fmtMsg(local string, m *proto.Message) string {
	from := m.From
	if m.FromRole != "" {
		from += "(" + m.FromRole + ")"
	}
	to := ""
	if m.To != "" {
		to = " → @" + m.To
	}
	re := ""
	if m.ReplyTo > 0 {
		re = fmt.Sprintf(" ↩#%d", m.ReplyTo)
	}
	return fmt.Sprintf("[/%s/@%s #%d %s%s] %s%s: %s", local, m.Room, m.ID, m.TS, re, from, to, m.Text)
}

func renderState(local string, st *proto.State) string {
	var b strings.Builder
	role := st.MyRole
	if role == "" {
		role = "none (role_claim to declare one)"
	}
	fmt.Fprintf(&b, "chat /%s — you are %q, role: %s\n", local, st.Me, role)
	fmt.Fprintf(&b, "members (%d):\n", len(st.Members))
	for _, m := range st.Members {
		r := "—"
		if m.Role != "" {
			r = m.Role
			if m.RoleExclusive {
				r += " (exclusive)"
			}
		}
		status := "online"
		if !m.Online {
			status = "offline"
		}
		you := ""
		if m.Name == st.Me {
			you = " (you)"
		}
		fmt.Fprintf(&b, "  @%s%s  role: %s  %s\n", m.Name, you, r, status)
	}
	mine := set(st.MyRooms)
	b.WriteString("rooms:\n")
	for _, r := range st.Rooms {
		line := "  /" + local + "/@" + r.Name
		if r.Name == proto.RoomCommon {
			line += "  — everyone"
		} else {
			if r.Topic != "" {
				line += "  — " + r.Topic
			}
			line += "  members: " + strings.Join(r.Members, ", ")
		}
		if mine[r.Name] && r.Name != proto.RoomCommon {
			line += "  [joined]"
		}
		b.WriteString(line + "\n")
	}
	fmt.Fprintf(&b, "send: /%[1]s/@common[/@member] · /%[1]s/@<room>[/@member] · /%[1]s/@direct/@member\n", local)
	return b.String()
}

func set(s []string) map[string]bool {
	m := make(map[string]bool, len(s))
	for _, v := range s {
		m[v] = true
	}
	return m
}
