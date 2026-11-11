// Package store: persistence seam of the broker. Today nothing is persisted (Memory);
// a SQLite (WAL) backend is planned behind the same interface.
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
type Backend interface {
	Load(chatID string) ([]Record, error)
	Append(chatID string, r Record) error
}

// Memory keeps nothing beyond the broker's own memory.
type Memory struct{}

func (Memory) Load(string) ([]Record, error) { return nil, nil }
func (Memory) Append(string, Record) error   { return nil }
