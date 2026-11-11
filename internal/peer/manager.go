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

// Manager keeps this node's connections to its peers: it listens (if asked to), dials the peers that
// have an address, and runs a session on every connection. Everything it knows about peers and
// shares is in the store (node.db); Refresh reads it again after the owner changed it.
type Manager struct {
	st     store.Federation
	origin string
	priv   ed25519.PrivateKey
	logf   func(format string, args ...any)

	// OnAccepted is called after events of a chat have been stored (they came from a peer); the
	// broker applies them to its projections. It runs on the goroutine of the session and must not
	// wait for what a session does.
	OnAccepted func(chatID string, accepted []events.Event)

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	wake   chan string

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
		wake: make(chan string, 256), sessions: map[string]*session{}, dialers: map[string]context.CancelFunc{}}
	m.wg.Add(1)
	go m.pump()
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
		return m.knownPeer(origin)
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
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			m.wg.Add(1)
			go func() {
				defer m.wg.Done()
				m.serve(tls.Server(nc, cfg), "")
			}()
		}
	}()
	return ln.Addr(), nil
}

// knownPeer: only a node the owner added, and one that has not been flagged, may connect.
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
		if p, ok := want[origin]; !ok || p.Addr == "" {
			cancel()
			delete(m.dialers, origin)
		}
	}
	for origin, s := range m.sessions {
		p, ok := want[origin]
		if !ok || !sameSet(p.Chats, s.sharedByUs()) {
			s.close()
		}
	}
	for _, p := range peers {
		if p.Addr == "" || m.dialers[p.Origin] != nil || m.ctx.Err() != nil {
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

// dial keeps a connection to one peer: it connects, runs the session, and tries again after a
// pause that grows while it fails.
func (m *Manager) dial(ctx context.Context, origin string) {
	pause := time.Second
	for ctx.Err() == nil {
		peers, err := m.st.Peers()
		addr := ""
		for _, p := range peers {
			if p.Origin == origin {
				addr = p.Addr
			}
		}
		if err == nil && addr != "" {
			started := time.Now()
			if err := m.dialOnce(ctx, origin, addr); err != nil && ctx.Err() == nil {
				m.logf("peer: %s at %s: %v", origin, addr, err)
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
	d := net.Dialer{Timeout: 5 * time.Second}
	nc, err := d.DialContext(ctx, "tcp", addr)
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
func (m *Manager) serve(conn *tls.Conn, _ string) {
	hctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
	err := conn.HandshakeContext(hctx)
	cancel()
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
}

var errClosed = errors.New("the session is closed")
