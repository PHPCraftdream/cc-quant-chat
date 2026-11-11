package peer

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/events"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

func (s *session) originPub(origin string) (ed25519.PublicKey, bool) {
	if origin == s.m.origin {
		return s.m.priv.Public().(ed25519.PublicKey), true
	}
	raw, ok, err := s.m.st.OriginKey(origin)
	if err != nil || !ok {
		return nil, false
	}
	return ed25519.PublicKey(raw), true
}

// receive takes the events of a frame: each is checked and stored in turn. One that fails is left
// out, with the rest of its origin's events in the frame (they no longer follow); the others go on.
// A chain that is missing events is asked for again. A fork ends the session with the node that
// forked, when that is the peer itself.
func (s *session) receive(f Frame) error {
	chat := f.Chat
	var accepted []events.Event
	blocked := map[string]bool{}
	refusedOwn, keptOwn := false, false
	neigh := s.neighbors()
	for _, e := range f.Events {
		s.mu.Lock()
		if s.remote[chat] == nil {
			s.remote[chat] = map[string]int64{}
		}
		s.remote[chat][e.Origin] = max(s.remote[chat][e.Origin], e.Seq) // the peer holds what it sends
		s.mu.Unlock()
		if blocked[e.Origin] {
			continue
		}
		if why := s.admit(e, neigh); why != "" { // this node does not take it from this peer: not stored, not asked for again
			blocked[e.Origin] = true
			s.m.noteRefused(s.peer, why)
			continue
		}
		err := s.m.accept(chat, e, f.Pubs)
		var fork *store.ForkError
		switch {
		case err == nil:
			accepted = append(accepted, e)
			s.m.noteAccepted(s.peer)
			s.forgive(chat, e)
			keptOwn = keptOwn || e.Origin == s.peer
		case errors.Is(err, store.ErrDuplicate):
		case errors.As(err, &fork):
			blocked[e.Origin] = true
			if e.Origin == s.peer {
				return fmt.Errorf("%s forked its journal", s.peer)
			}
		case errors.Is(err, store.ErrGap):
			blocked[e.Origin] = true // we lack events before this one: our vector, sent below, makes the peer resend
		case errors.Is(err, store.ErrMissingRef):
			// It names an event of another origin that has not come: the peer sends that one on our
			// vector and this one again. Quietly, a few times; a peer that cannot supply it is then
			// treated as one whose event is refused (backoff, one line of log).
			blocked[e.Origin] = true
			if s.waitedFor(chat, e) > maxWaits {
				s.refuse(chat, e, err)
				refusedOwn = refusedOwn || e.Origin == s.peer
			}
		default:
			blocked[e.Origin] = true
			s.refuse(chat, e, err)
			refusedOwn = refusedOwn || e.Origin == s.peer
		}
	}
	if len(accepted) > 0 {
		if s.m.OnAccepted != nil {
			s.m.OnAccepted(chat, accepted)
		}
		s.m.Notify(chat) // the other peers of this chat get them too
	}
	switch {
	case keptOwn:
		s.strike = 0
	case refusedOwn:
		s.strike++
		if s.strike >= maxStrikes {
			return fmt.Errorf("%s: its events were refused %d times in a row: %w", s.peer, s.strike, errRefused)
		}
	}
	return s.sendVector(chat)
}

// A refused event does not come back at once. The peer would send it again as soon as our vector says
// we lack it, and we would refuse it again: a loop at the speed of the network. So the session
// remembers it, tells the peer in its vector that it holds that origin to the end (nothing of it, nor
// of what follows, is sent), tells the log once, and asks again after a pause that doubles; the
// peer that is refused maxStrikes times in a row is let go.
type rejection struct {
	id    string
	until time.Time
	pause time.Duration
}

const (
	maxStrikes  = 3
	firstPause  = 5 * time.Second
	longestWait = 5 * time.Minute
)

var errRefused = errors.New("refused")

func (s *session) refuse(chat string, e events.Event, why error) {
	s.mu.Lock()
	byOrigin := s.rej[chat]
	if byOrigin == nil {
		byOrigin = map[string]*rejection{}
		s.rej[chat] = byOrigin
	}
	r, again := byOrigin[e.Origin], false
	if r != nil && r.id == e.ID() {
		again = true
		r.pause = min(2*r.pause, longestWait)
	} else {
		r = &rejection{id: e.ID(), pause: firstPause}
		byOrigin[e.Origin] = r
	}
	r.until = time.Now().Add(r.pause)
	pause := r.pause
	s.mu.Unlock()
	if !again {
		s.m.logf("peer: %s: event %s#%d of chat %.8s was not kept: %v (asked for again in %v)", s.peer, e.Origin, e.Seq, chat, why, pause)
	}
	time.AfterFunc(pause+50*time.Millisecond, func() {
		select {
		case <-s.done:
		default:
			_ = s.sendVector(chat)
		}
	})
}

// forgive drops what was held against an origin once an event of it has been kept.
func (s *session) forgive(chat string, e events.Event) {
	s.mu.Lock()
	delete(s.rej[chat], e.Origin)
	delete(s.waits, fmt.Sprintf("%s/%s/%d", chat, e.Origin, e.Seq))
	s.mu.Unlock()
}

const maxWaits = 3 // how many times an event may be put off for something it names before it counts as refused

// waitedFor counts one more time that the event is put off for something it names.
func (s *session) waitedFor(chat string, e events.Event) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := fmt.Sprintf("%s/%s/%d", chat, e.Origin, e.Seq)
	s.waits[k]++
	return s.waits[k]
}

// sendVector says what this node holds of a chat; of the origins whose event is refused for now it
// says "all", so that the peer sends nothing of them until the pause is over.
func (s *session) sendVector(chat string) error {
	vec, err := s.m.st.Vector(chat)
	if err != nil {
		return err
	}
	if vec == nil {
		vec = map[string]int64{}
	}
	switch s.level { // what is asked for is what is accepted: "*" says "all the others are held"
	case store.TrustRead:
		vec = map[string]int64{"*": math.MaxInt64}
	case store.TrustMember:
		vec = map[string]int64{"*": math.MaxInt64, s.peer: vec[s.peer]}
	}
	s.holdVector(vec)
	now := time.Now()
	s.mu.Lock()
	for origin, r := range s.rej[chat] {
		if now.Before(r.until) {
			vec[origin] = math.MaxInt64
		}
	}
	s.mu.Unlock()
	return s.send(Frame{T: "vector", Chat: chat, Vec: vec})
}
