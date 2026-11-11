package peer

import (
	"encoding/json"
	"strings"

	"github.com/PHPCraftdream/cc-quant-chat/internal/events"
)

const (
	maxFrame    = 16 << 20 // bytes of one frame
	batchEvents = 200      // events in one frame
	batchBytes  = 4 << 20  // bytes of events in one frame (estimated)
	lamportJump = 1000     // how far above the highest clock value known an event's may be
)

// Frame is one line of the exchange. T says what it is:
//
//	hello   Origin, Chats: who the sender is and the chats it shares with the receiver
//	vector  Chat, Vec: the highest seq the sender holds per origin in a chat (asks for the rest)
//	events  Chat, Events, Pubs: events in the order of the clock, with the keys of their origins
//	ping    nothing to say; the connection is alive
//	bye     the sender is closing
type Frame struct {
	T      string            `json:"t"`
	Origin string            `json:"origin,omitempty"`
	Chats  []string          `json:"chats,omitempty"`
	Chat   string            `json:"chat,omitempty"`
	Vec    map[string]int64  `json:"vec,omitempty"`
	Events []events.Event    `json:"events,omitempty"`
	Pubs   map[string]string `json:"pubs,omitempty"` // origin -> base64 public key
}

// exportable is what another node is given of an event. Most events go whole; those that are this
// node's own business, or that only it can read, go redacted: a header with hashes, enough for the
// chain and the node's signature to verify, not enough to read.
//
//   - whole: messages to a room, the board and documents, who a nick is (nick.register, nick.key,
//     nick.reset) and rooms (room.*);
//   - redacted: direct messages, settings (config.set), pinned roles (nick.pin), snapshots of a
//     database laid out from an older version (import.*), and any kind not known yet.
func exportable(e events.Event) events.Event {
	if e.Redacted() {
		return e
	}
	switch {
	case e.Kind == "msg":
		var p struct {
			Room string `json:"room"`
		}
		if json.Unmarshal([]byte(e.Payload), &p) != nil || p.Room == "direct" {
			return e.Redact()
		}
		return e
	case e.Kind == "nick.register", e.Kind == "nick.key", e.Kind == "nick.reset":
		return e
	case strings.HasPrefix(e.Kind, "room."), strings.HasPrefix(e.Kind, "task."), strings.HasPrefix(e.Kind, "sprint."),
		strings.HasPrefix(e.Kind, "board."), strings.HasPrefix(e.Kind, "doc."):
		return e
	}
	return e.Redact()
}
