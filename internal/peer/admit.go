package peer

import (
	"fmt"
	"math"

	"github.com/PHPCraftdream/cc-quant-chat/internal/events"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// maxSourcesVia: how many sources, that are not neighbors of this node, one trusted neighbor may
// bring (docs/design-trust.md section 3).
const maxSourcesVia = 16

// neighbors is what this node lists, by origin; read once per frame.
func (s *session) neighbors() map[string]store.Peer {
	out := map[string]store.Peer{}
	if peers, err := s.m.st.Peers(); err == nil {
		for _, p := range peers {
			out[p.Origin] = p
		}
	}
	return out
}

// admit says why an event is not taken from this peer ("" if it is). From a read node nothing; from a
// member only the events of its own source; from a trusted node also those of the sources it brings,
// at the level of a member at most: a source that is itself a neighbor here is held to its own level
// (read, or not confirmed yet: nothing), one that is not is counted against the neighbor that brought
// it and refused beyond the limit. (What a member may write is limited by what applying does:
// settings, pinned roles, invitations and snapshots of a node are not applied.)
func (s *session) admit(e events.Event, neigh map[string]store.Peer) string {
	if _, ok := s.m.InQuarantine(e.Origin); ok {
		return s.hold(e.Origin, "source in quarantine")
	}
	switch s.level {
	case store.TrustRead:
		return "read"
	case store.TrustMember:
		if e.Origin != s.peer {
			return "foreign source via a member"
		}
		return ""
	}
	if e.Origin == s.peer || e.Origin == s.m.origin {
		return ""
	}
	if p, ok := neigh[e.Origin]; ok {
		if p.State != store.StateActive || p.Trust == store.TrustRead {
			return s.hold(e.Origin, "source that is read or pending here")
		}
		return ""
	}
	if s.srcOK[e.Origin] {
		return ""
	}
	src, ok := s.m.st.(store.Sources)
	if !ok {
		return ""
	}
	allowed, fresh, err := src.NoteSource(e.Origin, s.peer, s.m.limits().Sources)
	switch {
	case err != nil:
		s.m.logf("peer: sources of %s: %v", s.peer, err)
		return s.hold(e.Origin, "sources unavailable")
	case !allowed:
		s.m.alert(s.peer, "too many sources", fmt.Sprintf("node %s brought more than %d sources that are not neighbors here: the events of %s and of further new ones are not taken through it", s.peer, s.m.limits().Sources, e.Origin))
		return s.hold(e.Origin, "too many sources via it")
	case fresh:
		s.m.logf("peer: %s brought a new source %s", s.peer, e.Origin)
	}
	s.srcOK[e.Origin] = true
	return ""
}

// hold makes the peer's vector say that this node holds the origin to the end, so that its events are
// not sent again and again; it returns why.
func (s *session) hold(origin, why string) string {
	s.mu.Lock()
	s.held[origin] = true
	s.mu.Unlock()
	return why
}

// holdVector marks the held origins in a vector.
func (s *session) holdVector(vec map[string]int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for origin := range s.held {
		vec[origin] = math.MaxInt64
	}
}

// alert tells the broker, once per node and reason until it restarts, of something the owner should
// know; the broker says it loudly.
func (m *Manager) alert(origin, reason, text string) {
	k := origin + "/" + reason
	m.statsMu.Lock()
	if m.alerted[k] {
		m.statsMu.Unlock()
		return
	}
	m.alerted[k] = true
	m.statsMu.Unlock()
	m.logf("peer: %s", text)
	if m.OnAlert != nil {
		m.OnAlert(origin, text)
	}
}
