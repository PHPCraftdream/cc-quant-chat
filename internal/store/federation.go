package store

import (
	"errors"

	"github.com/PHPCraftdream/cc-quant-chat/internal/events"
)

// Exchange of the journals of chats with other nodes (docs/design-federation.md).

// Peer is a node this one exchanges journals with.
type Peer struct {
	Origin  string
	Addr    string // where to reach it; "" if only it connects to us
	Trust   string // what this node accepts from it: TrustRead, TrustMember or TrustTrusted
	State   string // StateActive, or StatePending (an invitation used, the code not yet confirmed)
	Pubkey  []byte // its public key, once seen
	AddedAt string
	// PendingUntil: when a pending node is dropped (RFC 3339), "" for an active one.
	PendingUntil string
	Chats        []string // ids of the chats shared with it
}

// What this node accepts from a node (docs/design-trust.md section 1).
const (
	TrustRead    = "read"    // nothing: it receives, its events are not taken
	TrustMember  = "member"  // the events of its own source: messages, the board, pages
	TrustTrusted = "trusted" // also the events of other sources that it brings
)

// ValidTrust reports whether s is a level.
func ValidTrust(s string) bool { return s == TrustRead || s == TrustMember || s == TrustTrusted }

// Peer states.
const (
	StateActive  = "active"
	StatePending = "pending"
)

// Why AcceptEvent refused an event.
var (
	ErrGap       = errors.New("the event is not the next of its origin: earlier ones are missing")
	ErrChain     = errors.New("the event does not follow the last event of its origin (prev)")
	ErrDuplicate = errors.New("the event is stored already")
	// ErrMissingRef: the event names something made by an event of another origin that has not
	// arrived yet. Like a gap, it is waited out: the event comes again with what it lacks.
	ErrMissingRef = errors.New("the event names something that this node does not hold yet")
)

// ForkError: the origin has two different events with one (origin, seq). Stored is the one this
// node has; the one offered is the other half of the proof.
type ForkError struct{ Stored events.Event }

func (e *ForkError) Error() string { return "two different events with one (origin, seq)" }

// Federation is the optional part of a backend that can exchange journals with other nodes.
type Federation interface {
	// The nodes: node.db.
	AddPeer(origin, addr string) error
	RemovePeer(origin string) error
	// SetPeerTrust changes the level of a node and returns the one it had; an error if it is not listed.
	SetPeerTrust(origin, level string) (old string, err error)
	Peers() ([]Peer, error)
	SharePeerChat(chatID, origin string) error
	UnsharePeerChat(chatID, origin string) error
	// SharedWith: the origins a chat is shared with.
	SharedWith(chatID string) ([]string, error)
	FlagOrigin(origin, reason, evidence string) error
	Flagged(origin string) (reason string, flagged bool, err error)
	OriginKey(origin string) ([]byte, bool, error)
	RememberOrigin(origin string, pub []byte) error

	// The journal of a chat.
	Vector(chatID string) (map[string]int64, error) // highest seq held per origin
	// EventsAfter: events of one origin with seq > after, in order, as stored (whole or redacted).
	EventsAfter(chatID, origin string, after int64, limit int) ([]events.Event, error)
	// AcceptEvent stores an event that came from another node as the next one of its origin and
	// applies it to the projections, in one transaction: the caller has checked its signatures; the
	// chain and the rules of application are checked here. It returns what the broker has to learn
	// of (records in the numbers of this node), or ErrGap, ErrChain, ErrDuplicate, *ForkError, or
	// an error of the rules (the event is then not stored).
	AcceptEvent(chatID string, e events.Event) ([]Record, error)
	MaxLamport(chatID string) (int64, error) // the highest clock value the chat has seen
	// OnEvents sets a function called, after the change is committed, with the id of a chat that
	// has new events. It runs on the goroutine that wrote them, possibly with locks held: it must
	// only note the fact.
	OnEvents(fn func(chatID string))
}
