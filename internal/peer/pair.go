package peer

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/invite"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// Inviting a node (docs/design-trust.md section 2). A node that is not listed may connect to the
// listening port and do one thing: show an invitation. The proof it gives is signed with the key
// of the invitation over the keying material of this very connection and the two node keys, so it
// is good for this connection only. After it the node is listed as pending: nothing is exchanged
// until the owners of both nodes have compared the code and confirmed it.

const (
	exporterLabel     = "ccqc/invite/v1"
	codeLabel         = "ccqc/peer-code/v1"
	strangerTimeout   = 10 * time.Second // a stranger has this long to say its one frame
	strangerPerMinute = 10               // connections of unknown keys served in a minute
	maxInviteFrame    = 8 << 10
)

// Kinds of PairEvent.
const (
	PairUsed    = "used"    // an invitation was used by a node (this node issued it)
	PairBad     = "bad"     // a wrong proof; Text says how many tries are left or that it burnt
	PairActive  = "active"  // both owners confirmed: the node is active
	PairDropped = "dropped" // a pending node was removed (expired, or three wrong codes)
)

// PairEvent tells the broker something it records and says loudly.
type PairEvent struct {
	Kind   string
	Origin string
	Text   string
	Peer   store.Peer // for PairDropped: what was known of the node
}

// subject is what an invitation is used for: the node it opens and the node that uses it.
func subject(issuer, user ed25519.PublicKey) []byte {
	return append(append([]byte{}, issuer...), user...)
}

// pairCode is the six digits the owners compare: derived from the keying material of the
// connection, which no one who is not one of its two ends can know or choose.
func pairCode(cs tls.ConnectionState) (string, error) {
	km, err := cs.ExportKeyingMaterial(codeLabel, nil, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", binary.BigEndian.Uint64(km[:8])%1_000_000), nil
}

// strangerAllowed counts a connection of an unknown key against the limit of the port.
func (m *Manager) strangerAllowed() bool {
	m.smu.Lock()
	defer m.smu.Unlock()
	cut := time.Now().Add(-time.Minute)
	kept := m.strangers[:0]
	for _, t := range m.strangers {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	m.strangers = kept
	if len(m.strangers) >= strangerPerMinute {
		return false
	}
	m.strangers = append(m.strangers, time.Now())
	return true
}

func (m *Manager) pair(ev PairEvent) {
	if m.OnPair != nil {
		m.OnPair(ev)
	}
}

// stranger serves a connection whose key is not listed: one invitation frame, then it is closed.
func (m *Manager) stranger(conn *tls.Conn, pub ed25519.PublicKey) {
	defer conn.Close()
	pr, ok := m.st.(store.Pairing)
	if !ok || !m.strangerAllowed() {
		return
	}
	_ = conn.SetDeadline(time.Now().Add(strangerTimeout))
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 1024), maxInviteFrame)
	if !sc.Scan() {
		return
	}
	var f Frame
	if json.Unmarshal(sc.Bytes(), &f) != nil || f.T != "invite" {
		return
	}
	enc := json.NewEncoder(conn)
	invalid := func() { _ = enc.Encode(Frame{T: "invite_invalid"}) }
	cs := conn.ConnectionState()
	exp, err := cs.ExportKeyingMaterial(exporterLabel, nil, 32)
	code, err2 := pairCode(cs)
	sig, err3 := base64.StdEncoding.DecodeString(f.Sig)
	if err != nil || err2 != nil || err3 != nil {
		invalid()
		return
	}
	me := m.priv.Public().(ed25519.PublicKey)
	origin := originOf(pub)
	rec, err := pr.AcceptNodeInvite(f.ID, time.Now(), store.Pending{Origin: origin, Pub: pub, Code: code},
		func(r invite.Record) bool {
			return r.Kind == invite.KindNode && invite.Verify(r.Pub, exp, subject(me, pub), sig)
		})
	var bad *invite.BadProof
	switch {
	case errors.As(err, &bad):
		left := invite.MaxTries - bad.Tries
		text := fmt.Sprintf("a node %s showed a wrong proof for an invitation %s (%d tries left)", origin, invite.Short(bad.Record.ID), left)
		if bad.Burnt {
			text = fmt.Sprintf("a node %s showed a wrong proof for an invitation %s: it is burnt", origin, invite.Short(bad.Record.ID))
		}
		m.pair(PairEvent{Kind: PairBad, Origin: origin, Text: text})
		invalid()
		return
	case err != nil:
		invalid()
		return
	}
	scope, _ := store.ParseNodeScope(rec.Scope)
	_ = m.st.RememberOrigin(origin, pub)
	_ = enc.Encode(Frame{T: "invite_ok", Origin: m.origin, Offer: scope.Chats})
	m.pair(PairEvent{Kind: PairUsed, Origin: origin, Text: fmt.Sprintf("the invitation %s was used by the node %s: it waits for the owners to confirm the code", invite.Short(rec.ID), origin)})
	_ = m.Refresh()
}

// JoinResult is what the owner who used an invitation is shown.
type JoinResult struct {
	Origin string // of the node that invited
	Addr   string
	Code   string // the owners compare it
	Chats  []store.ChatRef
	Until  time.Time
}

// Join uses a node invitation: it connects to the address in it, pinned to the key in it (a
// certificate of another key is refused before anything is said), proves it holds the invitation
// and lists the node as pending at the level trust.
func (m *Manager) Join(ctx context.Context, in invite.Invitation, trust string) (JoinResult, error) {
	pr, ok := m.st.(store.Pairing)
	if !ok {
		return JoinResult{}, errors.New("this store cannot pair with other nodes")
	}
	if in.Kind != invite.KindNode {
		return JoinResult{}, errors.New("this is an invitation to a chat, not to a node")
	}
	if !store.ValidTrust(trust) {
		return JoinResult{}, fmt.Errorf("trust is read, member or trusted, not %q", trust)
	}
	raw, err := base64.StdEncoding.DecodeString(in.Key)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return JoinResult{}, invite.ErrFormat
	}
	issuer := ed25519.PublicKey(raw)
	origin := originOf(issuer)
	if origin == m.origin {
		return JoinResult{}, errors.New("that is an invitation of this node")
	}
	var lastErr error
	for _, addr := range in.Addrs {
		var res JoinResult
		if res, lastErr = m.joinAt(ctx, pr, in, addr, issuer, trust); lastErr == nil {
			return res, nil
		}
		if errors.Is(lastErr, errInviteRefused) {
			break // the node answered: another address will say the same
		}
	}
	if lastErr == nil {
		lastErr = errors.New("the invitation has no address")
	}
	return JoinResult{}, lastErr
}

var errInviteRefused = errors.New("the invitation was not accepted (it may be used, expired, revoked or not meant for this node)")

func (m *Manager) joinAt(ctx context.Context, pr store.Pairing, in invite.Invitation, addr string, issuer ed25519.PublicKey, trust string) (JoinResult, error) {
	origin := originOf(issuer)
	cfg, err := tlsConfig(m.priv, false, func(got string, _ ed25519.PublicKey) error {
		if got != origin {
			return fmt.Errorf("the node at %s is not the one that invited (it shows another key)", addr)
		}
		return nil
	})
	if err != nil {
		return JoinResult{}, err
	}
	uri, hostport, err := ParseAddr(addr)
	if err != nil {
		return JoinResult{}, err
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	nc, err := d.DialContext(ctx, "tcp", hostport)
	if err != nil {
		return JoinResult{}, err
	}
	conn := tls.Client(nc, cfg)
	defer conn.Close()
	hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = conn.HandshakeContext(hctx)
	cancel()
	if err != nil {
		return JoinResult{}, err
	}
	cs := conn.ConnectionState()
	exp, err := cs.ExportKeyingMaterial(exporterLabel, nil, 32)
	if err != nil {
		return JoinResult{}, err
	}
	code, err := pairCode(cs)
	if err != nil {
		return JoinResult{}, err
	}
	me := m.priv.Public().(ed25519.PublicKey)
	sig := base64.StdEncoding.EncodeToString(in.Proof(exp, subject(issuer, me)))
	_ = conn.SetDeadline(time.Now().Add(strangerTimeout))
	if err := json.NewEncoder(conn).Encode(Frame{T: "invite", ID: in.ID(), Sig: sig}); err != nil {
		return JoinResult{}, err
	}
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 1024), maxFrame)
	if !sc.Scan() {
		return JoinResult{}, fmt.Errorf("no answer from %s: %v", addr, sc.Err())
	}
	var f Frame
	if json.Unmarshal(sc.Bytes(), &f) != nil || f.T != "invite_ok" || f.Origin != origin {
		return JoinResult{}, errInviteRefused
	}
	until := time.Now().Add(store.PendingTTL)
	if err := m.st.RememberOrigin(origin, issuer); err != nil {
		return JoinResult{}, err
	}
	if err := pr.AddPendingPeer(store.Pending{Origin: origin, Pub: issuer, Addr: uri, Trust: trust, Until: until, Code: code, Chats: f.Offer}); err != nil {
		return JoinResult{}, err
	}
	if err := m.Refresh(); err != nil {
		return JoinResult{}, err
	}
	return JoinResult{Origin: origin, Addr: uri, Code: code, Chats: f.Offer, Until: until}, nil
}

// ConfirmCode: the owner of this node enters the code of a pending node. A right one tells the node
// (if it is connected; otherwise its next hello says so); when both have confirmed the node is active.
func (m *Manager) ConfirmCode(origin, code string) (store.ConfirmResult, error) {
	pr, ok := m.st.(store.Pairing)
	if !ok {
		return store.ConfirmResult{}, errors.New("this store cannot pair with other nodes")
	}
	before, _ := m.peerRow(origin)
	res, err := pr.Confirm(origin, code)
	if err != nil {
		return res, err
	}
	switch {
	case res.Gone:
		m.pair(PairEvent{Kind: PairDropped, Origin: origin, Peer: before, Text: fmt.Sprintf("the node %s was dropped: three wrong codes (its key %s, address %q)", origin, base64.StdEncoding.EncodeToString(before.Pubkey), before.Addr)})
		_ = m.Refresh()
	case res.Right:
		m.mu.Lock()
		s := m.sessions[origin]
		m.mu.Unlock()
		if s != nil {
			_ = s.send(Frame{T: "confirm"})
		}
		if res.Active {
			m.activated(origin)
		}
	}
	return res, nil
}

// remoteConfirmed notes that the owner of a node has confirmed; true if the node is active now.
func (m *Manager) remoteConfirmed(origin string) bool {
	pr, ok := m.st.(store.Pairing)
	if !ok {
		return false
	}
	active, err := pr.ConfirmedRemote(origin)
	if err != nil {
		m.logf("peer: %s: %v", origin, err)
		return false
	}
	if active {
		m.activated(origin)
	}
	return active
}

// activated: both owners have confirmed. The broker makes the chats the node offered, then the
// sessions are renewed so that the exchange starts.
func (m *Manager) activated(origin string) {
	if m.OnActive != nil {
		m.OnActive(origin)
	}
	m.pair(PairEvent{Kind: PairActive, Origin: origin, Text: fmt.Sprintf("the node %s is active: both owners confirmed the code", origin)})
	_ = m.Refresh()
}

// expire removes the pending nodes whose time is out.
func (m *Manager) expire() {
	pr, ok := m.st.(store.Pairing)
	if !ok {
		return
	}
	gone, err := pr.DropExpired(time.Now())
	if err != nil {
		m.logf("peer: dropping pending nodes: %v", err)
		return
	}
	for _, p := range gone {
		m.pair(PairEvent{Kind: PairDropped, Origin: p.Origin, Peer: p, Text: fmt.Sprintf("the node %s was dropped: its code was not confirmed in time (its key %s, address %q)", p.Origin, base64.StdEncoding.EncodeToString(p.Pubkey), p.Addr)})
	}
	if len(gone) > 0 {
		_ = m.Refresh()
	}
}

func (m *Manager) peerRow(origin string) (store.Peer, bool) {
	peers, err := m.st.Peers()
	if err != nil {
		return store.Peer{}, false
	}
	for _, p := range peers {
		if p.Origin == origin {
			return p, true
		}
	}
	return store.Peer{}, false
}

// Public is the key of this node, as an invitation carries it.
func (m *Manager) Public() ed25519.PublicKey { return m.priv.Public().(ed25519.PublicKey) }
