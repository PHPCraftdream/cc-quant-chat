// Package store: persistence seam of the broker: Memory (nothing persisted) or
// the SQLite backend in store/sqlite.
package store

import "github.com/PHPCraftdream/cc-quant-chat/internal/proto"

// Record kinds: msg, room, in (joined room), out (left room), nick (registered), seen.
type Record struct {
	T     string         `json:"t"`
	Key   string         `json:"k,omitempty"` // thread key of a msg
	Msg   *proto.Message `json:"m,omitempty"`
	Room  string         `json:"r,omitempty"`
	Topic string         `json:"topic,omitempty"`
	Nick  string         `json:"n,omitempty"`
	Hash  string         `json:"h,omitempty"` // nick key hash
	ID    int64          `json:"id,omitempty"`
}

// Backend stores chat records; the broker replays Load on first use of a chat.
// Load also registers the chat (name is for humans; the id is SHA-256 of the token).
type Backend interface {
	Load(chatID, name string) ([]Record, error)
	Append(chatID string, r Record) error
}

// Memory keeps nothing beyond the broker's own memory.
type Memory struct{}

func (Memory) Load(string, string) ([]Record, error) { return nil, nil }
func (Memory) Append(string, Record) error           { return nil }
