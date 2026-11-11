// Package store: persistence seam of the broker: Memory (nothing persisted) or
// the SQLite backend in store/sqlite.
package store

import (
	"errors"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// Record kinds: msg, room, in (joined room), out (left room), nick (registered),
// nick_key (new key hash; empty = reset), seen, and maxid (Load only: newest message id
// of the chat, which may lie outside the returned window).
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

// ErrOwnerExists: an owner is registered already.
var ErrOwnerExists = errors.New("an owner is already registered")

// ErrOwnerChanged: the stored owner key is not the one the caller expected.
var ErrOwnerChanged = errors.New("the owner key changed meanwhile")

// Memory keeps nothing beyond the broker's own memory.
type Memory struct{}

func (Memory) Load(string, string) ([]Record, error) { return nil, nil }
func (Memory) Append(string, Record) error           { return nil }
