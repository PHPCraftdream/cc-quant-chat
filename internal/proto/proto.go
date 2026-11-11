// Package proto: wire types shared by broker and clients (NDJSON frames).
package proto

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	RoomCommon = "common"
	RoomDirect = "direct"
	MaxText    = 8192
	secretLen  = 32
)

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// ValidName: chat, room, member and role names.
func ValidName(s string) bool { return nameRe.MatchString(s) }

// Frame: request {op,id,args}, response {op:"ok"|"err",id,data|code,msg}, event {op:"event",data}.
type Frame struct {
	Op   string          `json:"op"`
	ID   int64           `json:"id,omitempty"`
	Args json.RawMessage `json:"args,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
	Code string          `json:"code,omitempty"`
	Msg  string          `json:"msg,omitempty"`
}

type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string {
	if e.Msg == "" {
		return e.Code
	}
	return e.Code + ": " + e.Msg
}

func Errf(code, format string, a ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// IsCode reports whether err is a broker error with the given code.
func IsCode(err error, code string) bool {
	var pe *Error
	return errors.As(err, &pe) && pe.Code == code
}

// NewToken returns "<chat>.<32 random bytes base64url>".
func NewToken(chat string) (string, error) {
	if !ValidName(chat) {
		return "", fmt.Errorf("bad chat name %q: want [a-z0-9-]{1,32}", chat)
	}
	b := make([]byte, secretLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return chat + "." + base64.RawURLEncoding.EncodeToString(b), nil
}

// ParseToken returns the chat name and chat id (hex SHA-256 of the whole token).
func ParseToken(tok string) (chat, id string, err error) {
	tok = strings.TrimSpace(tok)
	bad := errors.New("bad token format: want <chat>.<secret>")
	i := strings.LastIndexByte(tok, '.')
	if i <= 0 {
		return "", "", bad
	}
	chat = tok[:i]
	sec, derr := base64.RawURLEncoding.DecodeString(tok[i+1:])
	if !ValidName(chat) || derr != nil || len(sec) != secretLen {
		return "", "", bad
	}
	h := sha256.Sum256([]byte(tok))
	return chat, hex.EncodeToString(h[:]), nil
}

type JoinArgs struct {
	Token string `json:"token"`
	Name  string `json:"name"`
	Key   string `json:"key,omitempty"` // nick key, required once the nick is registered
}

// TimelineArgs: everything visible to you that was sent by or to Member (default: you).
type TimelineArgs struct {
	Member string `json:"member,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Before int64  `json:"before,omitempty"`
}

type SendArgs struct {
	Room    string `json:"room"`
	To      string `json:"to,omitempty"`
	Text    string `json:"text"`
	ReplyTo int64  `json:"reply_to,omitempty"`
}

type HistoryArgs struct {
	Room   string `json:"room"`
	With   string `json:"with,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Before int64  `json:"before,omitempty"` // only messages with id < before
}

// SearchArgs: all query words must occur (case-insensitive). Room/With narrow to one thread.
type SearchArgs struct {
	Query string `json:"query,omitempty"`
	Room  string `json:"room,omitempty"`
	With  string `json:"with,omitempty"`
	From  string `json:"from,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type SearchResult struct {
	Messages []Message `json:"messages"` // newest first
	Total    int       `json:"total"`
}

// ContextArgs: messages within Radius positions around each id, in that message's thread.
type ContextArgs struct {
	IDs    []int64 `json:"ids"`
	Radius int     `json:"radius,omitempty"`
}

type ContextBlock struct {
	Thread   string    `json:"thread"` // room name or "direct/@<peer>"
	Hits     []int64   `json:"hits"`
	Messages []Message `json:"messages"`
}

type ContextResult struct {
	Blocks  []ContextBlock `json:"blocks"`
	Missing []int64        `json:"missing,omitempty"` // unknown, expired or not visible to you
}

type RoomArgs struct {
	Room  string `json:"room"`
	Topic string `json:"topic,omitempty"`
}

type RoleArgs struct {
	Role      string `json:"role"`
	Exclusive bool   `json:"exclusive,omitempty"`
}

type Message struct {
	ID       int64  `json:"id"`
	TS       string `json:"ts"`
	Room     string `json:"room"`
	From     string `json:"from"`
	FromRole string `json:"from_role,omitempty"`
	To       string `json:"to,omitempty"`
	Text     string `json:"text"`
	ReplyTo  int64  `json:"reply_to,omitempty"`
}

// Event kinds: "msg", "presence".
type Event struct {
	Kind string   `json:"kind"`
	Msg  *Message `json:"msg,omitempty"`
	ToMe bool     `json:"to_me,omitempty"`
	Text string   `json:"text,omitempty"`
}

type MemberInfo struct {
	Name          string `json:"name"`
	Role          string `json:"role,omitempty"`
	RoleExclusive bool   `json:"role_exclusive,omitempty"`
	Online        bool   `json:"online"`
}

type RoomInfo struct {
	Name      string   `json:"name"`
	Topic     string   `json:"topic,omitempty"`
	CreatedBy string   `json:"created_by,omitempty"`
	Members   []string `json:"members,omitempty"`
}

type State struct {
	Chat    string       `json:"chat"`
	Me      string       `json:"me"`
	MyRole  string       `json:"my_role,omitempty"`
	MyRooms []string     `json:"my_rooms"`
	Members []MemberInfo `json:"members"`
	Rooms   []RoomInfo   `json:"rooms"`
}

type Welcome struct {
	State       State   `json:"state"`
	Backlog     []Event `json:"backlog,omitempty"`      // unread since the nick was last online
	UnreadTotal int     `json:"unread_total,omitempty"` // backlog may be cut to the newest
	Key         string  `json:"key,omitempty"`          // set once, when the nick is registered
	Warning     string  `json:"warning,omitempty"`      // e.g. history is not being saved
}

// BrokerInfo: reply to "ping"; shown by `ccqc broker status`.
type BrokerInfo struct {
	Version string `json:"version"`
	PID     int    `json:"pid"`
	Started string `json:"started"`
	Chats   int    `json:"chats"`   // loaded chats
	Clients int    `json:"clients"` // connected members
	Store   string `json:"store"`
	Warning string `json:"warning,omitempty"`
}

type SendResult struct {
	ID int64 `json:"id"`
}

type HistoryResult struct {
	Messages []Message `json:"messages"`
}
