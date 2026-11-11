package broker

import (
	"strings"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

const statusLimit = 120 // status updates per minute per nick

// memberStatus is what a present member reports about itself. It lives and dies with the member:
// nothing is stored.
type memberStatus struct {
	known  bool // the client has reported busy/free at least once
	busy   bool
	since  time.Time
	note   string
	folder string
	branch string
}

// oneLine turns text into a single printable line: control characters become spaces.
func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// setStatus applies a partial update. It raises no event and wakes nobody.
func (b *Broker) setStatus(ch *chat, m *member, a proto.StatusArgs) *proto.Error {
	if !b.allow(ch, m.name, "status", statusLimit) {
		return proto.Errf("rate_limited", "max %d status updates per minute", statusLimit)
	}
	if a.Text != nil && len(*a.Text) > proto.MaxStatusText {
		return proto.Errf("too_large", "status text is %d bytes, max %d", len(*a.Text), proto.MaxStatusText)
	}
	s := &m.status
	if a.Text != nil {
		s.note = oneLine(*a.Text)
	}
	if a.Folder != nil {
		s.folder = cut(oneLine(*a.Folder), proto.MaxFolder)
	}
	if a.Branch != nil {
		s.branch = cut(oneLine(*a.Branch), proto.MaxBranch)
	}
	if a.Busy != nil {
		switch {
		case *a.Busy && !s.busy:
			s.since = time.Now()
		case !*a.Busy:
			s.since = time.Time{}
		}
		s.busy, s.known = *a.Busy, true
	}
	return nil
}

// fill adds a present member's status to its state entry.
func (m *member) fill(mi *proto.MemberInfo) {
	if m.conn == nil {
		return
	}
	s := m.status
	mi.Folder, mi.Branch, mi.Note = s.folder, s.branch, s.note
	switch {
	case !s.known:
	case s.busy:
		mi.Activity = "busy"
		mi.BusySince = s.since.UTC().Format(time.RFC3339)
	default:
		mi.Activity = "free"
	}
}
