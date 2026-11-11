package peer

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/events"
	"github.com/PHPCraftdream/cc-quant-chat/internal/node"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

const (
	pushLimit    = 500 // events of one origin fetched per round
	writeTimeout = 30 * time.Second
	helloTimeout = 15 * time.Second
	pingEvery    = 20 * time.Second // a quiet session is told apart from a dead one (a tunnel may not say)
	readIdle     = 75 * time.Second
)

func originOf(pub ed25519.PublicKey) string { return node.Origin(pub) }

// session is one connection with one peer, after the handshake.
type session struct {
	m         *Manager
	conn      *tls.Conn
	peer      string // origin of the other side
	initiator string // origin of the side that dialed

	wmu    sync.Mutex
	enc    *json.Encoder
	pushMu sync.Mutex // one push at a time: two would send the same events twice

	mu     sync.Mutex
	mine   map[string]bool                  // chats this node shares with the peer
	common map[string]bool                  // chats both share: the ones exchanged
	remote map[string]map[string]int64      // chat -> origin -> highest seq the peer is known to hold
	rej    map[string]map[string]*rejection // chat -> origin -> the event that is refused for now
	waits  map[string]int                   // chat/origin/seq -> times the event was put off for what it names
	strike int                              // frames in a row in which an event of the peer itself was refused
	once   sync.Once
	done   chan struct{}
}

func (s *session) shares(chat string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.common[chat]
}

func (s *session) sharedByUs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.mine))
	for c := range s.mine {
		out = append(out, c)
	}
	return out
}

func (s *session) close() {
	s.once.Do(func() {
		close(s.done)
		s.conn.Close()
	})
}

func (s *session) send(f Frame) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	return s.enc.Encode(f)
}

// run is the life of a session: hello, then the exchange, until the connection ends. initiator is
// the origin of the side that opened the connection.
func (m *Manager) run(conn *tls.Conn, initiator string, ctx context.Context) error {
	defer conn.Close()
	pub, err := peerKey(conn.ConnectionState())
	if err != nil {
		return err
	}
	peer := originOf(pub)
	if err := m.st.RememberOrigin(peer, pub); err != nil {
		return err
	}
	if err := m.knownPeer(peer); err != nil { // the dialing side checked the origin by its key; ours is the list
		return err
	}
	peers, err := m.st.Peers()
	if err != nil {
		return err
	}
	s := &session{m: m, conn: conn, peer: peer, initiator: initiator, enc: json.NewEncoder(conn),
		mine: map[string]bool{}, common: map[string]bool{}, remote: map[string]map[string]int64{}, rej: map[string]map[string]*rejection{}, waits: map[string]int{}, done: make(chan struct{})}
	var chats []string
	for _, p := range peers {
		if p.Origin == peer {
			chats = p.Chats
		}
	}
	for _, c := range chats {
		s.mine[c] = true
	}
	go func() { // a cancelled context ends the session
		select {
		case <-ctx.Done():
			s.close()
		case <-s.done:
		}
	}()
	defer s.close()

	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64<<10), maxFrame)
	if err := s.send(Frame{T: "hello", Origin: m.origin, Chats: chats}); err != nil {
		return err
	}
	_ = conn.SetReadDeadline(time.Now().Add(helloTimeout))
	if !sc.Scan() {
		return fmt.Errorf("%s: no hello: %v", peer, sc.Err())
	}
	_ = conn.SetReadDeadline(time.Time{})
	var hello Frame
	if err := json.Unmarshal(sc.Bytes(), &hello); err != nil || hello.T != "hello" || hello.Origin != peer {
		return fmt.Errorf("%s: a bad hello", peer)
	}
	for _, c := range hello.Chats {
		if s.mine[c] {
			s.common[c] = true
		}
	}
	if len(s.common) == 0 { // nothing shared by both: nothing to exchange, nothing to keep
		_ = s.send(Frame{T: "bye"})
		return nil
	}
	if !m.register(s) {
		_ = s.send(Frame{T: "bye"})
		return nil
	}
	defer m.unregister(s)
	m.logf("peer: connected to %s, %d chat(s) in common", peer, len(s.common))
	for c := range s.common {
		if err := s.sendVector(c); err != nil {
			return err
		}
	}

	go s.keepalive()
	scan := func() bool {
		_ = conn.SetReadDeadline(time.Now().Add(readIdle))
		return sc.Scan()
	}
	for scan() {
		var f Frame
		if err := json.Unmarshal(sc.Bytes(), &f); err != nil {
			return fmt.Errorf("%s: a frame that is not JSON", peer)
		}
		switch f.T {
		case "vector":
			if !s.shares(f.Chat) {
				continue
			}
			s.setRemote(f.Chat, f.Vec)
			s.push(f.Chat)
		case "events":
			if !s.shares(f.Chat) {
				continue
			}
			if err := s.receive(f); err != nil {
				return err
			}
		case "bye":
			return nil
		}
	}
	select {
	case <-s.done:
		return nil
	default:
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return errors.New("the connection ended")
}

func (s *session) keepalive() {
	t := time.NewTicker(pingEvery)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			if s.send(Frame{T: "ping"}) != nil {
				s.close()
				return
			}
		}
	}
}

func (s *session) setRemote(chat string, vec map[string]int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remote[chat] = vec
	if vec == nil {
		s.remote[chat] = map[string]int64{}
	}
}

// push sends the peer what it lacks of a chat, in the order of the clock, whole or redacted as
// exportable says. What it sends it takes as held by the peer; the peer's next vector corrects it.
func (s *session) push(chat string) {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	for {
		more, err := s.pushOnce(chat)
		if err != nil {
			s.m.logf("peer: %s: sending chat %.8s: %v", s.peer, chat, err)
			s.close()
			return
		}
		if !more {
			return
		}
	}
}

func (s *session) pushOnce(chat string) (more bool, err error) {
	if !s.shares(chat) {
		return false, nil
	}
	local, err := s.m.st.Vector(chat)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	remote := map[string]int64{}
	for o, n := range s.remote[chat] {
		remote[o] = n
	}
	s.mu.Unlock()

	var all []events.Event
	bound, truncated := int64(math.MaxInt64), false
	for origin, top := range local {
		if _, flagged, err := s.m.st.Flagged(origin); err != nil {
			return false, err
		} else if flagged {
			continue
		}
		after := remote[origin]
		if top <= after {
			continue
		}
		evs, err := s.m.st.EventsAfter(chat, origin, after, pushLimit)
		if err != nil {
			return false, err
		}
		if len(evs) == pushLimit && evs[len(evs)-1].Seq < top { // there is more of this origin: stop before its clock
			truncated = true
			bound = min(bound, evs[len(evs)-1].Lamport)
		}
		all = append(all, evs...)
	}
	sort.Slice(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.Lamport != b.Lamport {
			return a.Lamport < b.Lamport
		}
		if a.Origin != b.Origin {
			return a.Origin < b.Origin
		}
		return a.Seq < b.Seq
	})
	if truncated {
		for len(all) > 0 && all[len(all)-1].Lamport > bound {
			all = all[:len(all)-1]
		}
	}
	for len(all) > 0 {
		n, size := 0, 0
		for n < len(all) && n < batchEvents && size < batchBytes {
			size += len(all[n].Payload) + len(all[n].Assigned) + 512
			n++
		}
		batch := make([]events.Event, n)
		pubs := map[string]string{}
		for i, e := range all[:n] {
			batch[i] = exportable(e)
			if _, ok := pubs[e.Origin]; !ok {
				if pub, ok := s.originPub(e.Origin); ok {
					pubs[e.Origin] = base64.StdEncoding.EncodeToString(pub)
				}
			}
		}
		if err := s.send(Frame{T: "events", Chat: chat, Events: batch, Pubs: pubs}); err != nil {
			return false, err
		}
		s.mu.Lock()
		if s.remote[chat] == nil {
			s.remote[chat] = map[string]int64{}
		}
		for _, e := range all[:n] {
			s.remote[chat][e.Origin] = max(s.remote[chat][e.Origin], e.Seq)
		}
		s.mu.Unlock()
		all = all[n:]
	}
	return truncated, nil
}

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
		err := s.m.accept(chat, e, f.Pubs)
		var fork *store.ForkError
		switch {
		case err == nil:
			accepted = append(accepted, e)
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
