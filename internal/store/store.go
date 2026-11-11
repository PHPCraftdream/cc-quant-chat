// Package store: persistence seam of the broker: Memory (nothing persisted) or
// the SQLite backend in store/sqlite.
package store

import (
	"crypto/ed25519"
	"errors"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// Record kinds: msg, room, in (joined room), out (left room), nick (registered), pin (a role
// pinned to a nick), nick_key (the nick's credentials now, Hash and Pub; both empty = reset),
// seen, and maxid (Load only: newest message id of the chat, which may lie outside the returned
// window).
type Record struct {
	T     string         `json:"t"`
	Key   string         `json:"k,omitempty"` // thread key of a msg
	Msg   *proto.Message `json:"m,omitempty"`
	Room  string         `json:"r,omitempty"`
	Topic string         `json:"topic,omitempty"`
	Nick  string         `json:"n,omitempty"`
	Hash  string         `json:"h,omitempty"` // nick key hash
	ID    int64          `json:"id,omitempty"`
	// ClientID: idempotency key of a msg (per sender); "" = none.
	ClientID string `json:"cid,omitempty"`
	// Origin: the node (broker) that wrote a msg; see NodeStore.
	Origin string `json:"origin,omitempty"`
	// Role: for "pin", the role pinned to Nick ("" unpins).
	Role string `json:"role,omitempty"`
	// Pub: the base64 Ed25519 key of a nick ("nick", "nick_key"; "" = none). For a "msg":
	// AuthorPub, Sig and Payload, the author's signature of the event and what was signed (all
	// empty for an unsigned client).
	Pub       string `json:"pub,omitempty"`
	AuthorPub string `json:"author_pub,omitempty"`
	Sig       string `json:"sig,omitempty"`
	Payload   string `json:"payload,omitempty"`
	// Admin: set when the record is the effect of a signed administrator operation.
	Admin *AdminAttest `json:"-"`
}

// AdminAttest is the owner's signature of the administrator operation that causes a record: the
// operation, its arguments in canonical JSON, the challenge nonce it was signed with, the base64
// signature and the owner's key. The record's event keeps them, so the signature can be checked
// from the event alone.
type AdminAttest struct {
	Op, Args, Nonce, Sig, Pub string
}

// AttestedSettings is the optional part of a backend that records a chat setting changed by an
// administrator operation together with the owner's signature of it.
type AttestedSettings interface {
	SetChatSettingAttested(chatID, key, value string, a AdminAttest) error
}

// EventSig is the author's signature on an event, kept with every row the event writes.
// Payload is the canonical JSON that was signed (the request without sig and lamport).
type EventSig struct {
	Pub     string
	Sig     string
	Payload string
	Canon   string // the request in canonical JSON, signed or not: the payload of its event
}

// Clock is the optional part of a backend that knows a chat's logical clock (the value of the
// last write); a signed write must carry Lamport(chat)+1.
type Clock interface {
	Lamport(chatID string) (int64, error)
}

// AdminEntry is one attempt of an administrator operation.
type AdminEntry struct {
	TS, Op, Args, Result, Origin string
}

// AdminStore is the optional part of a backend that keeps settings and the administrator log.
type AdminStore interface {
	// Settings returns every stored setting as scope → key → value.
	Settings() (map[string]map[string]string, error)
	SetSetting(scope, key, value string) error
	LogAdmin(e AdminEntry) error
}

// DuplicateError: Append refused a msg whose sender and ClientID were stored before; ID is the
// id of that earlier message.
type DuplicateError struct{ ID int64 }

func (e *DuplicateError) Error() string { return "duplicate client message id" }

// ThreadWindow: how many of the newest messages per room/dialog Load returns.
const ThreadWindow = 10000

// Backend stores chat records; the broker replays Load on first use of a chat.
// Load also registers the chat (name is for humans; the id is SHA-256 of the token).
type Backend interface {
	Load(chatID, name string) ([]Record, error)
	Append(chatID string, r Record) error
}

// OwnerStore is the optional part of a backend that keeps the owner's public key (one per
// broker). A backend without it keeps the key in the broker's memory only.
type OwnerStore interface {
	// Owner returns the key and when it was registered (RFC 3339).
	Owner() (pub []byte, since string, found bool, err error)
	// SetOwner registers the first owner key; ErrOwnerExists when there is one already.
	SetOwner(pub []byte) error
	// ReplaceOwner swaps the key if the stored one is old (compare-and-set).
	ReplaceOwner(old, pub []byte) error
}

// NodeStore is the optional part of a backend that records this broker's identity (origin) and
// puts it on rows written before there was one.
type NodeStore interface {
	RegisterOrigin(origin string, pub []byte) error
}

// Journal is the optional part of a backend that keeps the journal of events: SetNode gives it
// the identity its events are signed with; BackfillEvents moves the rows that predate the
// journal into it (once; it reports how many chats and events this run moved).
type Journal interface {
	SetNode(origin string, priv ed25519.PrivateKey)
	BackfillEvents() (chats, events int, err error)
}

// ErrOwnerExists: an owner is registered already.
var ErrOwnerExists = errors.New("an owner is already registered")

// ErrOwnerChanged: the stored owner key is not the one the caller expected.
var ErrOwnerChanged = errors.New("the owner key changed meanwhile")

// Memory keeps nothing beyond the broker's own memory.
type Memory struct{}

func (Memory) Load(string, string) ([]Record, error) { return nil, nil }
func (Memory) Append(string, Record) error           { return nil }
