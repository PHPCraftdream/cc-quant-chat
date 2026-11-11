package mcp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/addr"
)

const (
	presenceDelay = 3 * time.Second // presence lines are batched into one event
	inboxCap      = 300
	quietCap      = 50
)

type Session struct {
	mu     sync.Mutex
	chats  map[string]*chatConn // key: local chat name
	notify func(content string, meta map[string]string)
	inbox  []inboxItem
	seq    int64
	read   int64
	quiet  []string // presence lines held until the next message
	sid    string   // Claude Code session id, learned from the hooks

	hookMu sync.Mutex // guards hook
	hook   hookState  // `ccqc wait` rewake hook
}

type inboxItem struct {
	seq  int64
	line string
}

func newSession(notify func(string, map[string]string)) *Session {
	return &Session{chats: map[string]*chatConn{}, notify: notify,
		hook: hookState{used: hookDelivery()}}
}

func (s *Session) shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cc := range s.chats {
		cc.mu.Lock()
		cc.leaving = true
		if cc.conn != nil {
			cc.conn.Close() // member stays offline for the grace period: rejoin keeps name/role/rooms
		}
		cc.mu.Unlock()
	}
}

// emit records an event for chat_inbox and pushes it into the session:
// via the rewake hook if in use, else as a channel notification.
// Quiet events (presence) don't wake Claude: they ride along with the next loud one.
func (s *Session) emit(content string, meta map[string]string, line string, quiet bool) {
	s.mu.Lock()
	s.seq++
	s.inbox = append(s.inbox, inboxItem{s.seq, line})
	if len(s.inbox) > inboxCap {
		s.inbox = append([]inboxItem(nil), s.inbox[len(s.inbox)-inboxCap:]...)
	}
	if quiet {
		s.quiet = append(s.quiet, line)
		if len(s.quiet) > quietCap {
			s.quiet = s.quiet[len(s.quiet)-quietCap:]
		}
		s.mu.Unlock()
		return
	}
	held := s.quiet
	s.quiet = nil
	s.mu.Unlock()
	if s.route(append(held, line)) {
		return
	}
	if len(held) > 0 {
		meta["presence"] = strings.Join(held, "; ")
	}
	s.notify(content, meta)
}

func (s *Session) get(chat string) (*chatConn, error) {
	name := addr.Chat(chat)
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == "" && len(s.chats) == 1 {
		for _, cc := range s.chats {
			return cc, nil
		}
	}
	if cc := s.chats[name]; cc != nil {
		return cc, nil
	}
	joined := make([]string, 0, len(s.chats))
	for n := range s.chats {
		joined = append(joined, n)
	}
	sort.Strings(joined)
	if len(joined) == 0 {
		return nil, fmt.Errorf("not_joined: not in any chat; the user should run /quant-join <token> <nick>")
	}
	return nil, fmt.Errorf("not_joined: not in chat %q; joined: %s", name, strings.Join(joined, ", "))
}

// Call dispatches an MCP tool call.
func (s *Session) Call(name string, raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage("{}")
	}
	var a struct {
		Token     string  `json:"token"`
		Name      string  `json:"name"`
		Alias     string  `json:"alias"`
		To        string  `json:"to"`
		Text      string  `json:"text"`
		ReplyTo   int64   `json:"reply_to"`
		Chat      string  `json:"chat"`
		Target    string  `json:"target"`
		Limit     int     `json:"limit"`
		Room      string  `json:"room"`
		Topic     string  `json:"topic"`
		Role      string  `json:"role"`
		Exclusive bool    `json:"exclusive"`
		Before    int64   `json:"before"`
		Query     string  `json:"query"`
		In        string  `json:"in"`
		From      string  `json:"from"`
		IDs       []int64 `json:"ids"`
		Radius    int     `json:"radius"`
		Member    string  `json:"member"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("bad arguments: %v", err)
	}
	switch name {
	case "chat_join":
		return s.join(a.Token, a.Name, a.Alias, false)
	case "chat_send":
		return s.send(a.To, a.Text, a.ReplyTo)
	case "chat_state":
		return s.state(a.Chat)
	case "chat_history":
		return s.history(a.Target, a.Limit, a.Before)
	case "chat_timeline":
		return s.timeline(a.Chat, a.Member, a.Limit, a.Before)
	case "chat_search":
		return s.search(a.Query, a.Chat, a.In, a.From, a.Limit)
	case "chat_context":
		return s.context(a.Chat, a.IDs, a.Radius)
	case "chat_inbox":
		return s.inboxRead(), nil
	case "chat_leave":
		return s.leave(a.Chat)
	case "room_create", "room_join", "room_leave":
		return s.roomOp(name, a.Room, a.Topic)
	case "role_claim":
		return s.roleClaim(a.Chat, a.Role, a.Exclusive)
	case "role_release":
		return s.roleRelease(a.Chat)
	}
	return "", fmt.Errorf("unknown tool %q", name)
}

func (s *Session) inboxRead() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	for _, it := range s.inbox {
		if it.seq > s.read {
			b.WriteString(it.line + "\n")
		}
	}
	s.read = s.seq
	if b.Len() == 0 {
		return "no new events"
	}
	return b.String()
}
