package mcp

import (
	"fmt"
	"regexp"
	"strings"
	"time"

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
	if m.Owner {
		// Set by the broker after checking the owner's signature. Capitals: nicks and roles are
		// [a-z0-9-], so no sender can write this marker as a name.
		from = ownerMarker + " " + from
	}
	return fmt.Sprintf("[/%s/@%s #%d %s%s] %s%s: %s", local, m.Room, m.ID, m.TS, re, from, to, neutralize(m.Text))
}

const ownerMarker = "OWNER"

// A line of a message that looks like the start of an event would read as a message nobody sent
// (the plugin-ui cards and the model both read these lines). Such lines, after the first, get a
// visible prefix.
var headerLike = regexp.MustCompile(`(?m)^([ »]*)\[/`)

func neutralize(text string) string {
	first, rest, ok := strings.Cut(text, "\n")
	if !ok {
		return text
	}
	return first + "\n" + headerLike.ReplaceAllString(rest, "${1}¦ [/")
}

// eventLine is one message event as the wake-up batch and chat_inbox list it.
// The plugin-ui cards parse this text (plugin-ui/hooks/format.ts); see TestWireFormat.
func eventLine(local string, m *proto.Message, toMe bool) string {
	return fmtMsg(local, m) + "   (reply: " + replyAddr(local, m, toMe) + ")"
}

// presenceLine is a batch of presence changes as the wake-up batch lists it.
func presenceLine(local string, lines []string) string {
	return "[/" + local + " presence] " + strings.Join(lines, "; ")
}

// fmtHits is chat_search's listing of one chat: a heading and the hits, indented.
func fmtHits(local string, total int, msgs []proto.Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "/%s: %d hit(s), showing %d, newest first\n", local, total, len(msgs))
	for i := range msgs {
		b.WriteString("  " + fmtMsg(local, &msgs[i]) + "\n")
	}
	return b.String()
}

// fmtContext is chat_context's answer: blocks of messages (the asked-for ones marked) and the
// ids that could not be shown.
func fmtContext(local string, r proto.ContextResult) string {
	var b strings.Builder
	for _, blk := range r.Blocks {
		hit := map[int64]bool{}
		for _, h := range blk.Hits {
			hit[h] = true
		}
		first, last := blk.Messages[0].ID, blk.Messages[len(blk.Messages)-1].ID
		fmt.Fprintf(&b, "── /%s/@%s  #%d…#%d\n", local, blk.Thread, first, last)
		for i := range blk.Messages {
			mark := "   "
			if hit[blk.Messages[i].ID] {
				mark = " » "
			}
			b.WriteString(mark + fmtMsg(local, &blk.Messages[i]) + "\n")
		}
	}
	if len(r.Missing) > 0 {
		miss := make([]string, len(r.Missing))
		for i, id := range r.Missing {
			miss[i] = fmt.Sprintf("#%d", id)
		}
		fmt.Fprintf(&b, "not found or not visible to you: %s\n", strings.Join(miss, ", "))
	}
	return b.String()
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
			if m.RolePinned {
				r += "*" // pinned by the owner: the member can't change it
			}
			if m.RoleExclusive {
				r += " (exclusive)"
			}
		}
		status := "online"
		if !m.Online {
			status = "offline"
		} else if line := statusLine(m, time.Now()); line != "" {
			status += " · " + line
		}
		you := ""
		if m.Name == st.Me {
			you = " (you)"
		}
		mark := ""
		if m.Owner {
			mark = "  " + ownerMarker
		}
		fmt.Fprintf(&b, "  @%s%s%s  role: %s  %s\n", m.Name, you, mark, r, status)
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
