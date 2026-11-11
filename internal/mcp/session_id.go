package mcp

import (
	"fmt"
	"log"
	"regexp"
	"sort"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/local"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

var sidRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// autoState: the outcome of joining the chats remembered for a session id.
type autoState struct {
	done  chan struct{}
	lines []string          // "fail /chat as @nick: reason"
	ok    map[string]string // joined chats: local name → "/chat as @nick"
}

const (
	autoJoinTries = 6
	autoJoinDelay = 1500 * time.Millisecond
)

// setSessionID learns the Claude Code session id from a hook. A hook talking to us means the
// rewake hook is configured: from now on events wait for it instead of going out as channel
// notifications.
//
// The first id starts the auto-join. A different id later means the user moved to another
// session inside this process; only SessionStart says why (hello=true, source):
//   - clear, compact: the same conversation continues, the chats move to the new id;
//   - resume, fork, startup: another session; its chats are left and the new session's
//     remembered chats are joined;
//   - anything else (or unknown): nothing changes.
//
// A `wait` hook with another id changes nothing: the SessionStart hook decides.
func (s *Session) setSessionID(sid, source string, hello bool) {
	if !sidRe.MatchString(sid) {
		return
	}
	s.hookMu.Lock()
	s.hook.used = true
	s.hookMu.Unlock()

	s.mu.Lock()
	old := s.sid
	switch {
	case old == sid:
		s.mu.Unlock()
	case old == "":
		s.sid = sid
		st := s.startAuto()
		s.mu.Unlock()
		go s.autoJoin(sid, st)
	case !hello:
		s.mu.Unlock()
	case source == "clear" || source == "compact":
		s.sid = sid
		s.mu.Unlock()
		if err := local.MoveSession(local.ProjectDir(), old, sid); err != nil {
			log.Printf("move session chats: %v", err)
		}
	case source == "resume" || source == "fork" || source == "startup":
		s.sid = sid
		st := s.startAuto()
		s.mu.Unlock()
		s.dropChats()
		go s.autoJoin(sid, st)
	default: // unknown reason: don't guess, keep what we have
		s.mu.Unlock()
		log.Printf("session id changed %s -> %s with source %q: left as is", old, sid, source)
	}
}

// startAuto: caller holds s.mu.
func (s *Session) startAuto() *autoState {
	st := &autoState{done: make(chan struct{}), ok: map[string]string{}}
	s.auto = st
	return st
}

// dropChats disconnects from every chat without forgetting them: they stay remembered
// for the session they belong to.
func (s *Session) dropChats() {
	s.mu.Lock()
	chats := s.chats
	s.chats = map[string]*chatConn{}
	s.mu.Unlock()
	for _, cc := range chats {
		cc.mu.Lock()
		cc.leaving = true
		if cc.conn != nil {
			cc.conn.Close()
		}
		cc.mu.Unlock()
	}
}

// autoJoin enters the chats remembered for the session and records the outcome per chat.
func (s *Session) autoJoin(sid string, st *autoState) {
	defer close(st.done)
	entries, err := local.SessionChats(local.ProjectDir(), sid)
	if err != nil {
		log.Printf("auto-join: %v", err)
		st.lines = append(st.lines, fmt.Sprintf("fail the remembered chats could not be read: %v", err))
		return
	}
	if len(entries) > 0 {
		_ = local.TouchSession(local.ProjectDir(), sid)
	}
	for _, e := range entries {
		var err error
		for try := 1; try <= autoJoinTries; try++ {
			if _, err = s.joinEntry(e); err == nil || !(proto.IsCode(err, "name_online") || proto.IsCode(err, "restarting")) {
				break // the previous connection of this nick may still be closing: only that and a restarting broker are retried
			}
			time.Sleep(autoJoinDelay)
		}
		if err != nil {
			st.lines = append(st.lines, fmt.Sprintf("fail /%s as @%s: %v", e.Local(), e.Name, err))
			msg := fmt.Sprintf("[/%s] auto-join as @%s failed: %v", e.Local(), e.Name, err)
			s.emit(msg, map[string]string{"kind": "error", "chat": e.Local()}, msg, false, ackRef{})
			continue
		}
		st.ok[e.Local()] = fmt.Sprintf("/%s as @%s", e.Local(), e.Name)
	}
}

// autoResult waits (up to wait) for the auto-join of sid and returns what happened.
func (s *Session) autoResult(sid string, wait time.Duration) []string {
	s.mu.Lock()
	st := s.auto
	same := s.sid == sid
	s.mu.Unlock()
	if st == nil || !same {
		return nil
	}
	select {
	case <-st.done:
	case <-time.After(wait):
		return []string{"pending the chats are still being joined in the background"}
	}
	// What counts is where the session is now: a chat left since the auto-join isn't reported.
	out := append([]string(nil), st.lines...)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, local := range sortedChats(st.ok) {
		if s.chats[local] != nil {
			out = append(out, "ok "+st.ok[local])
		}
	}
	return out
}

func sortedChats(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
