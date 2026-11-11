package proto

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Signed events: what a nick signs to join, what an author signs for a write, and the pieces
// both sides use to build those bytes.

// ClockReply: reply to "clock", and the data of a stale_clock error: the logical clock value a
// signed write must carry now.
type ClockReply struct {
	Next int64 `json:"next"`
}

// StaleClock reads the clock value out of a stale_clock error.
func StaleClock(err error) (next int64, ok bool) {
	var pe *Error
	if !errors.As(err, &pe) || pe.Code != "stale_clock" {
		return 0, false
	}
	var r ClockReply
	if json.Unmarshal(pe.Data, &r) != nil || r.Next <= 0 {
		return 0, false
	}
	return r.Next, true
}

// NickJoinMessage is what a nick signs to join (and to prove it holds the key it presents).
func NickJoinMessage(chatID, nick, pub, nonce string) []byte {
	return []byte(strings.Join([]string{"ccqc/v1/nick-join", chatID, nick, pub, nonce}, "\n"))
}

// EventMessage is what an author signs for an event: the chat, the origin (the broker the
// request is addressed to: the numbers in it, such as a task id or a version, are that node's
// own), the kind (msg, the board op such as task.create, doc.<op>), the canonical JSON of the request without its
// sig, lamport and origin, and the chat's clock.
func EventMessage(chatID, origin, kind string, canon []byte, lamport int64) []byte {
	return []byte(strings.Join([]string{"ccqc/v1/event", chatID, origin, kind, string(canon), fmt.Sprint(lamport)}, "\n"))
}

// Signed is what a signed write carries next to its arguments.
type Signed struct {
	Canon   []byte // the arguments in canonical JSON without the three fields below
	Origin  string
	Lamport int64
	Sig     string
}

// SplitSigned reads the arguments of a write: Canon and the signed fields ("origin", "lamport",
// "sig"), which are not part of what is signed.
func SplitSigned(raw []byte) (s Signed, err error) {
	if len(raw) == 0 {
		return Signed{Canon: []byte("{}")}, nil
	}
	var m map[string]json.RawMessage
	if err = json.Unmarshal(raw, &m); err != nil {
		return Signed{}, err
	}
	for k, dst := range map[string]any{"origin": &s.Origin, "lamport": &s.Lamport, "sig": &s.Sig} {
		if v, ok := m[k]; ok {
			_ = json.Unmarshal(v, dst)
			delete(m, k)
		}
	}
	rest, err := json.Marshal(m)
	if err != nil {
		return Signed{}, err
	}
	s.Canon, err = CanonicalJSON(rest)
	return s, err
}

// Stamper is implemented by the argument types of writes: it sets the fields of a signed write.
type Stamper interface {
	Stamp(origin string, lamport int64, sig string)
}

func (a *SendArgs) Stamp(origin string, lamport int64, sig string) {
	a.Origin, a.Lamport, a.Sig = origin, lamport, sig
}

func (a *BoardArgs) Stamp(origin string, lamport int64, sig string) {
	a.Origin, a.Lamport, a.Sig = origin, lamport, sig
}

func (a *DocArgs) Stamp(origin string, lamport int64, sig string) {
	a.Origin, a.Lamport, a.Sig = origin, lamport, sig
}
