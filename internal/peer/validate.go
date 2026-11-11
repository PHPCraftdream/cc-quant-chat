package peer

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/events"
	"github.com/PHPCraftdream/cc-quant-chat/internal/node"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

var (
	originRe = regexp.MustCompile(`^[0-9a-f]{32}$`)
	hashRe   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	kindRe   = regexp.MustCompile(`^[a-z0-9._-]{1,64}$`)
)

const maxEventBytes = 2 << 20 // contents of one event

// errFlagged: the event's origin has been caught equivocating.
var errFlagged = errors.New("the origin of the event is flagged: it wrote two different events with one seq")

// check is everything an event passes before it is kept: its shape, the key of its origin, the
// node's signature, the author's, the clock, and the chain (which the store checks again when it
// writes). An event that fails is not kept; the reason goes to the log of the node and the
// exchange goes on. What the content of an event claims to do (the board, documents) is checked
// by the rules of the application of events, not here.
func (m *Manager) check(chat string, e events.Event, pubs map[string]string) error {
	if !originRe.MatchString(e.Origin) {
		return fmt.Errorf("origin %q is not a node name", e.Origin)
	}
	if reason, flagged, err := m.st.Flagged(e.Origin); err != nil {
		return err
	} else if flagged {
		return fmt.Errorf("%w (%s)", errFlagged, reason)
	}
	if e.Seq < 1 || !kindRe.MatchString(e.Kind) {
		return fmt.Errorf("event %s#%d: a malformed header", e.Origin, e.Seq)
	}
	if (e.Seq == 1) != (e.Prev == "") || (e.Prev != "" && !hashRe.MatchString(e.Prev)) {
		return fmt.Errorf("event %s#%d: prev does not fit its place in the chain", e.Origin, e.Seq)
	}
	if _, err := time.Parse(time.RFC3339, e.TS); err != nil {
		return fmt.Errorf("event %s#%d: time %q", e.Origin, e.Seq, e.TS)
	}
	if len(e.Payload)+len(e.Assigned)+len(e.AuthorPub)+len(e.Sig) > maxEventBytes {
		return fmt.Errorf("event %s#%d: too large", e.Origin, e.Seq)
	}
	if h := e.Hidden; h != nil {
		for _, x := range []string{h.Payload, h.Author, h.Sig, h.Assigned} {
			if !hashRe.MatchString(x) {
				return fmt.Errorf("event %s#%d: a hash of its hidden contents is malformed", e.Origin, e.Seq)
			}
		}
		if e.Payload != "" || e.AuthorPub != "" || e.Sig != "" || e.Assigned != "" {
			return fmt.Errorf("event %s#%d: contents next to their hashes", e.Origin, e.Seq)
		}
	}

	pub, known, err := m.originKey(e.Origin, pubs)
	if err != nil {
		return fmt.Errorf("event %s#%d: %w", e.Origin, e.Seq, err)
	}
	if !e.VerifyNode(pub) {
		return fmt.Errorf("event %s#%d: the node's signature does not verify", e.Origin, e.Seq)
	}
	if !known { // the key names its origin and has just signed: keep it
		if err := m.st.RememberOrigin(e.Origin, pub); err != nil {
			return err
		}
	}
	if ok, _ := e.VerifyAuthor(); !ok {
		return fmt.Errorf("event %s#%d: the author's signature does not verify", e.Origin, e.Seq)
	}

	// The clock: no event may be far ahead of what the chat has seen, or a node could take the
	// future and win every merge; and along one chain it never goes back.
	max, err := m.st.MaxLamport(chat)
	if err != nil {
		return err
	}
	if e.Lamport < 0 || e.Lamport > max+lamportJump {
		return fmt.Errorf("event %s#%d: clock %d is more than %d ahead of the chat's %d", e.Origin, e.Seq, e.Lamport, lamportJump, max)
	}
	if e.Seq > 1 {
		prev, err := m.st.EventsAfter(chat, e.Origin, e.Seq-2, 1)
		if err == nil && len(prev) == 1 && prev[0].Seq == e.Seq-1 && e.Lamport < prev[0].Lamport {
			return fmt.Errorf("event %s#%d: clock %d is behind the previous event's %d", e.Origin, e.Seq, e.Lamport, prev[0].Lamport)
		}
	}
	return nil
}

// originKey is the key of an origin: the one this node recorded, or the one offered with the
// events. The origin is a hash of its key, so an offered key is only taken if it hashes to it.
func (m *Manager) originKey(origin string, pubs map[string]string) (ed25519.PublicKey, bool, error) {
	if raw, ok, err := m.st.OriginKey(origin); err != nil {
		return nil, false, err
	} else if ok {
		return ed25519.PublicKey(raw), true, nil
	}
	if origin == m.origin {
		return m.priv.Public().(ed25519.PublicKey), true, nil
	}
	b64, ok := pubs[origin]
	if !ok {
		return nil, false, errors.New("the key of its origin is not known and was not offered")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, false, errors.New("the offered key of its origin is malformed")
	}
	pub := ed25519.PublicKey(raw)
	if node.Origin(pub) != origin {
		return nil, false, errors.New("the offered key does not hash to its origin")
	}
	return pub, false, nil
}

// accept checks an event and stores it as the next of its origin. A fork (two different events
// with one origin and seq) flags the origin, with both events as the proof.
func (m *Manager) accept(chat string, e events.Event, pubs map[string]string) error {
	e.ChatID = chat
	if err := m.check(chat, e, pubs); err != nil {
		return err
	}
	var err error
	if m.Accept != nil {
		err = m.Accept(chat, e)
	} else {
		_, err = m.st.AcceptEvent(chat, e)
	}
	var fork *store.ForkError
	if errors.As(err, &fork) {
		evidence, _ := json.Marshal([]events.Event{fork.Stored, e})
		if ferr := m.st.FlagOrigin(e.Origin, "fork", string(evidence)); ferr != nil {
			return ferr
		}
		m.logf("peer: %s wrote two different events %s#%d in chat %.8s: its events are no longer accepted", e.Origin, e.Origin, e.Seq, chat)
		m.loadQuarantine() // a fork is a quarantine, lifted by the owner only
		m.alert(e.Origin, "fork", fmt.Sprintf("node %s wrote two different events %s#%d: it is in quarantine (fork), its new events are not taken; the owner lifts it with ccqc peer release", e.Origin, e.Origin, e.Seq))
		m.dropOrigin(e.Origin)
	}
	return err
}
