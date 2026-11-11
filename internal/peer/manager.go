package peer

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/events"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

const (
	maxHandshakes = 32 // connections at once that have not finished the TLS handshake; the rest are dropped
	maxSessions   = 64 // sessions at once
)

// Manager keeps this node's connections to its peers: it listens (if asked to), dials the peers that
// have an address, and runs a session on every connection. Everything it knows about peers and
// shares is in the store (node.db); Refresh reads it again after the owner changed it.
type Manager struct {
	st     store.Federation
	origin string
	priv   ed25519.PrivateKey
	logf   func(format string, args ...any)

	// OnAccepted is called after events of a chat have been stored (they came from a peer). It runs
	// on the goroutine of the session and must not wait for what a session does.
	OnAccepted func(chatID string, accepted []events.Event)
	// Accept stores one checked event and applies it; the default is the store's AcceptEvent. The
	// broker sets its own, which does it under the lock of the chat and tells the chat's members.
	Accept func(chatID string, e events.Event) error
	// OnPair is told of what happens to the nodes that are being paired (invitations, codes); OnActive
	// when a pending node becomes active, before its sessions start: the broker makes the chats it
	// offered. Both run on the goroutine that found out and must not wait for a session.
	OnPair func(PairEvent)
	// OnAlert: something about a node the owner should hear (once per node and reason, until a restart).
	OnAlert func(origin, text string)
	// OnQuarantine: a source was put in quarantine, or (released) taken out of it.
	OnQuarantine func(info store.QuarantineInfo, released bool)
	OnActive     func(origin string)

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	wake   chan string

	smu       sync.Mutex
	strangers []time.Time // when connections of unknown keys were served, the last minute

	statsMu sync.Mutex
	stats   map[string]*PeerStats
	alerted map[string]bool

	limMu sync.Mutex
	lim   Limits
	sl    sourceLimits
	q     quarantined

	mu       sync.Mutex
	ln       net.Listener
	sessions map[string]*session // by the origin of the peer
	dialers  map[string]context.CancelFunc
}

// New makes a manager for the node with the given key. logf may be nil.
func New(st store.Federation, origin string, priv ed25519.PrivateKey, logf func(string, ...any)) *Manager {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{st: st, origin: origin, priv: priv, logf: logf, ctx: ctx, cancel: cancel,
		wake: make(chan string, 256), stats: map[string]*PeerStats{}, alerted: map[string]bool{}, lim: DefaultLimits, sl: sourceLimits{src: map[string]*srcState{}}, sessions: map[string]*session{}, dialers: map[string]context.CancelFunc{}}
	m.wg.Add(1)
	m.loadQuarantine()
	go m.pump()
	m.wg.Add(1)
	go m.janitor()
	return m
}

// Notify says that a chat has new events (written here or taken from a peer): every session that
// shares it is told what its peer lacks. It never blocks.
func (m *Manager) Notify(chatID string) {
	select {
	case m.wake <- chatID:
	default: // a flood of them: the pump is behind and will look at the chat anyway
		select {
		case m.wake <- chatID:
		default:
		}
	}
}

func (m *Manager) pump() {
	defer m.wg.Done()
	for {
		select {
		case <-m.ctx.Done():
			return
		case chat := <-m.wake:
			for _, s := range m.sessionsOf(chat) {
				go s.push(chat) // a slow peer must not hold up the others
			}
		}
	}
}

func (m *Manager) sessionsOf(chat string) []*session {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*session
	for _, s := range m.sessions {
		if s.shares(chat) {
			out = append(out, s)
		}
	}
	return out
}

// Listen accepts connections from peers on addr. It returns the address it listens on.
func (m *Manager) Listen(addr string) (net.Addr, error) {
	cfg, err := tlsConfig(m.priv, true, func(origin string, _ ed25519.PublicKey) error {
		return m.notFlagged(origin) // a stranger may show an invitation, nothing else
	})
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.ln = ln
	m.mu.Unlock()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		handshaking := make(chan struct{}, maxHandshakes)
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			select {
			case handshaking <- struct{}{}:
			default: // too many unfinished handshakes: this one is not even started
				nc.Close()
				continue
			}
			m.wg.Add(1)
			go func() {
				defer m.wg.Done()
				m.serve(tls.Server(nc, cfg), func() { <-handshaking })
			}()
		}
	}()
	return ln.Addr(), nil
}

// notFlagged: a node that was caught writing two events with one (origin, seq) is not talked to at all.
func (m *Manager) notFlagged(origin string) error {
	if q, ok := m.InQuarantine(origin); ok {
		return fmt.Errorf("node %s is in quarantine (%s)", origin, q.Reason)
	}
	if reason, flagged, err := m.st.Flagged(origin); err != nil {
		return err
	} else if flagged {
		return fmt.Errorf("node %s is flagged (%s)", origin, reason)
	}
	return nil
}

// knownPeer: only a node the owner added, and one that has not been flagged, may exchange chats.
func (m *Manager) knownPeer(origin string) error {
	peers, err := m.st.Peers()
	if err != nil {
		return err
	}
	for _, p := range peers {
		if p.Origin == origin {
			if reason, flagged, err := m.st.Flagged(origin); err != nil {
				return err
			} else if flagged {
				return fmt.Errorf("node %s is flagged (%s)", origin, reason)
			}
			return nil
		}
	}
	return fmt.Errorf("node %s is not a peer of this one", origin)
}

// Refresh reads the peers and shares again: it starts a dialer for a peer that has an address and
// has none yet, stops those of peers that are gone, and ends sessions whose shared chats changed
// (they come back with the new set).
func (m *Manager) Refresh() error {
	peers, err := m.st.Peers()
	if err != nil {
		return err
	}
	want := map[string]store.Peer{}
	for _, p := range peers {
		want[p.Origin] = p
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for origin, cancel := range m.dialers {
		if p, ok := want[origin]; !ok || !dialable(p) || m.quarantinedNode(origin) {
			cancel()
			delete(m.dialers, origin)
		}
	}
	for origin, s := range m.sessions {
		p, ok := want[origin]
		if !ok || (p.State == store.StateActive && !sameSet(p.Chats, s.sharedByUs())) || p.Trust != s.level || p.State != s.state || m.quarantinedNode(origin) {
			s.close()
		}
	}
	for _, p := range peers {
		if !dialable(p) || m.quarantinedNode(p.Origin) || m.dialers[p.Origin] != nil || m.ctx.Err() != nil {
			continue
		}
		ctx, cancel := context.WithCancel(m.ctx)
		m.dialers[p.Origin] = cancel
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.dial(ctx, p.Origin)
		}()
	}
	return nil
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]bool{}
	for _, x := range a {
		seen[x] = true
	}
	for _, x := range b {
		if !seen[x] {
			return false
		}
	}
	return true
}

// dial keeps a connection to one peer that has an address: it connects, runs the session, and tries
// again after a pause that grows while it fails. One connection is enough, in either direction: while
// the peer is connected (it may have dialed us) nothing is dialed, and a failure is logged once, not
// at every try (a peer behind a NAT has an address nobody can reach).
func (m *Manager) dial(ctx context.Context, origin string) {
	pause := time.Second
	lastErr := ""
	for ctx.Err() == nil {
		peers, err := m.st.Peers()
		addr := ""
		for _, p := range peers {
			if p.Origin == origin && dialable(p) { // nothing shared with it, and not pending: nothing to dial for
				addr = p.Addr
			}
		}
		switch {
		case m.connected(origin):
			pause, lastErr = time.Second, ""
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		case err == nil && addr != "":
			started := time.Now()
			err := m.dialOnce(ctx, origin, addr)
			if errors.Is(err, errRefused) && ctx.Err() == nil { // a peer whose events are refused: not again soon
				m.logf("peer: %s at %s: %v", origin, addr, err)
				pause = min(max(2*pause, time.Minute), 10*time.Minute)
				select {
				case <-ctx.Done():
					return
				case <-time.After(pause):
				}
				continue
			}
			if err != nil && ctx.Err() == nil {
				if msg := err.Error(); msg != lastErr {
					m.logf("peer: %s at %s: %v", origin, addr, err)
					lastErr = msg
				}
			} else if err == nil {
				lastErr = ""
			}
			if time.Since(started) > 10*time.Second {
				pause = time.Second
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pause):
		}
		if pause < 30*time.Second {
			pause *= 2
		}
	}
}

func (m *Manager) connected(origin string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[origin] != nil
}

func (m *Manager) dialOnce(ctx context.Context, origin, addr string) error {
	cfg, err := tlsConfig(m.priv, false, func(got string, _ ed25519.PublicKey) error {
		if got != origin {
			return fmt.Errorf("the node at %s is %s, not the expected %s", addr, got, origin)
		}
		return nil
	})
	if err != nil {
		return err
	}
	_, hostport, err := ParseAddr(addr)
	if err != nil {
		return err
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	nc, err := d.DialContext(ctx, "tcp", hostport)
	if err != nil {
		return err
	}
	conn := tls.Client(nc, cfg)
	hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = conn.HandshakeContext(hctx)
	cancel()
	if err != nil {
		nc.Close()
		return err
	}
	return m.run(conn, m.origin, ctx)
}

// serve runs a session on an accepted connection.
func (m *Manager) serve(conn *tls.Conn, handshaken func()) {
	hctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
	err := conn.HandshakeContext(hctx)
	cancel()
	handshaken()
	if err != nil {
		conn.Close()
		m.logf("peer: a connection was refused: %v", err)
		return
	}
	pub, err := peerKey(conn.ConnectionState())
	if err != nil {
		conn.Close()
		return
	}
	if m.knownPeer(originOf(pub)) != nil { // not listed: it may show an invitation, nothing else
		m.stranger(conn, pub)
		return
	}
	// The peer dialed: it is the initiator.
	if err := m.run(conn, originOf(pub), m.ctx); err != nil && m.ctx.Err() == nil {
		m.logf("peer: %v", err)
	}
}

// register records a session. Two connections between the same pair of nodes (each dialed the
// other) are one too many: the one opened by the node with the smaller origin stays.
func (m *Manager) register(s *session) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sessions) >= maxSessions && m.sessions[s.peer] == nil {
		return false
	}
	if old := m.sessions[s.peer]; old != nil && old != s {
		if s.initiator > old.initiator {
			return false
		}
		old.close()
	}
	m.sessions[s.peer] = s
	return true
}

func (m *Manager) unregister(s *session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions[s.peer] == s {
		delete(m.sessions, s.peer)
	}
}

// dropOrigin ends the session with a node that has been flagged.
func (m *Manager) dropOrigin(origin string) {
	m.mu.Lock()
	s := m.sessions[origin]
	m.mu.Unlock()
	if s != nil {
		s.close()
	}
}

// Sessions lists the origins of the peers connected now.
func (m *Manager) Sessions() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for o := range m.sessions {
		out = append(out, o)
	}
	return out
}

// Close stops everything.
func (m *Manager) Close() {
	m.cancel()
	m.mu.Lock()
	if m.ln != nil {
		m.ln.Close()
	}
	var ss []*session
	for _, s := range m.sessions {
		ss = append(ss, s)
	}
	m.mu.Unlock()
	for _, s := range ss {
		s.close()
	}
	m.wg.Wait()
	m.flushLimits()
}

var errClosed = errors.New("the session is closed")

// dialable: a node with an address is dialed when something is shared with it, or when it is pending
// (the code is confirmed over that connection).
func dialable(p store.Peer) bool {
	return p.Addr != "" && (len(p.Chats) > 0 || p.State == store.StatePending)
}

// janitor removes pending nodes whose time is out.
func (m *Manager) janitor() {
	defer m.wg.Done()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-t.C:
			m.expire()
			m.flushLimits()
		}
	}
}
