package mcp

import (
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/gitbranch"
	"github.com/PHPCraftdream/cc-quant-chat/internal/local"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// Member status: a free-text note the session sets (status_set) and, automatically, busy/free,
// the project folder and the git branch. The broker keeps it only while the member is present,
// so the session repeats it after a reconnect (like rooms and role).

// projectInfo: the base name of the project folder and its current branch, read from .git/HEAD.
func projectInfo() (folder, branch string) {
	dir := local.ProjectDir()
	return filepath.Base(dir), gitbranch.Branch(dir)
}

func (s *Session) isBusy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy
}

// fullStatus is everything the broker should know about this session in this chat.
func (cc *chatConn) fullStatus() proto.StatusArgs {
	folder, branch := projectInfo()
	busy := cc.s.isBusy()
	cc.mu.Lock()
	note := cc.note
	cc.mu.Unlock()
	return proto.StatusArgs{Text: &note, Busy: &busy, Folder: &folder, Branch: &branch}
}

// pushStatus sends a status update; an older broker without the op is not an error.
func (cc *chatConn) pushStatus(a proto.StatusArgs) {
	if err := cc.call("status", a, nil); err != nil && !proto.IsCode(err, "unknown_op") {
		log.Printf("status /%s: %v", cc.local, err)
	}
}

// setBusy records whether the session is working on a turn and tells every chat. It is called
// when a prompt is submitted and a wake-up batch is handed over (busy) and when the rewake hook
// of a finished turn arrives (free). The branch is read again each time it changes.
func (s *Session) setBusy(busy bool) {
	s.mu.Lock()
	changed := s.busy != busy
	s.busy = busy
	chats := make([]*chatConn, 0, len(s.chats))
	for _, cc := range s.chats {
		chats = append(chats, cc)
	}
	s.mu.Unlock()
	if !changed {
		return
	}
	folder, branch := projectInfo()
	for _, cc := range chats {
		go cc.pushStatus(proto.StatusArgs{Busy: &busy, Folder: &folder, Branch: &branch})
	}
}

// statusSet: the status_set tool. Without chat the note goes to every chat of the session.
func (s *Session) statusSet(chat, text string) (string, error) {
	var targets []*chatConn
	if strings.TrimSpace(chat) != "" {
		cc, err := s.get(chat)
		if err != nil {
			return "", err
		}
		targets = []*chatConn{cc}
	} else {
		s.mu.Lock()
		for _, cc := range s.chats {
			targets = append(targets, cc)
		}
		s.mu.Unlock()
		if len(targets) == 0 {
			return "", fmt.Errorf("not_joined: not in any chat")
		}
		sort.Slice(targets, func(i, j int) bool { return targets[i].local < targets[j].local })
	}
	names := make([]string, 0, len(targets))
	for _, cc := range targets {
		if err := cc.call("status", proto.StatusArgs{Text: &text}, nil); err != nil {
			return "", fmt.Errorf("/%s: %w", cc.local, err)
		}
		cc.mu.Lock()
		cc.note = oneLineNote(text)
		cc.mu.Unlock()
		names = append(names, "/"+cc.local)
	}
	if strings.TrimSpace(text) == "" {
		return "status cleared in " + strings.Join(names, ", "), nil
	}
	return "status set in " + strings.Join(names, ", "), nil
}

func oneLineNote(s string) string { return strings.Join(strings.Fields(s), " ") }

// fmtSince: "45s", "12m", "2h05m".
func fmtSince(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// statusLine: what follows "online" in chat_state: activity, folder@branch, the note.
func statusLine(m proto.MemberInfo, now time.Time) string {
	var parts []string
	switch m.Activity {
	case "busy":
		since := ""
		if t, err := time.Parse(time.RFC3339, m.BusySince); err == nil && now.After(t) {
			since = " " + fmtSince(now.Sub(t))
		}
		parts = append(parts, "busy"+since)
	case "free":
		parts = append(parts, "free")
	}
	switch {
	case m.Folder != "" && m.Branch != "":
		parts = append(parts, m.Folder+"@"+m.Branch)
	case m.Folder != "":
		parts = append(parts, m.Folder)
	}
	if m.Note != "" {
		parts = append(parts, "«"+m.Note+"»")
	}
	return strings.Join(parts, " · ")
}
