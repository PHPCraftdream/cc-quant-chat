// Package proto: wire types shared by broker and clients (NDJSON frames).
package proto

import (
	"bytes"
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
	MaxTopic   = 200
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
	Data json.RawMessage // optional detail, e.g. ClockReply for stale_clock
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
	// Proto: protocol level of the client. 2+ confirms delivery with "ack"; the broker then
	// advances the nick's read cursor only on ack, not when the connection ends.
	Proto int `json:"proto,omitempty"`
	// OwnerSig: base64 Ed25519 signature of OwnerJoinMessage(chat id, nick, nonce) with the owner's
	// key, over a nonce from "challenge" on this connection. Valid: the broker marks the member
	// as the owner. Invalid, or no owner registered: the join is refused (bad_owner_sig).
	OwnerSig string `json:"owner_sig,omitempty"`
	// Pub: base64 Ed25519 public key of the nick (proto 4+); NickSig proves the client holds it
	// (a signature over NickJoinMessage with a nonce from "challenge"). A new nick is registered
	// with Pub; a legacy nick is rebound once, presenting its old secret in Key as well.
	Pub     string `json:"pub,omitempty"`
	NickSig string `json:"nick_sig,omitempty"`
	// Chat: the chat's id (64 hex), instead of a token (proto 5): for a nick that has a key, or with
	// an invitation. A chat is never made by a join with an id.
	Chat string `json:"chat,omitempty"`
	// Invite: the use of a one-time invitation (proto 5): it registers Pub for the nick it was
	// issued for, or replaces the key of that nick. Needs Pub and NickSig.
	Invite *JoinInvite `json:"invite,omitempty"`
	// Enroll (proto 6): the key registered by this join is a protected one (see keys.go).
	Enroll *JoinEnroll `json:"enroll,omitempty"`
}

// Challenge: reply to "challenge": a one-time nonce (base64url) for OwnerSig, valid 30 s on the
// connection it was issued to.
type Challenge struct {
	Nonce string `json:"nonce"`
}

// Owner messages. The strings are signed, so both sides build them with these functions.
func OwnerJoinMessage(chatID, nick, nonce string) []byte {
	return []byte(strings.Join([]string{"ccqc/v1/owner-join", chatID, nick, nonce}, "\n"))
}

func OwnerRegisterMessage(pub string) []byte {
	return []byte(strings.Join([]string{"ccqc/v1/owner-register", pub}, "\n"))
}

func OwnerRotateMessage(oldPub, newPub string) []byte {
	return []byte(strings.Join([]string{"ccqc/v1/owner-rotate", oldPub, newPub}, "\n"))
}

// OwnerRegisterArgs: pub is the base64 public key, sig its signature of OwnerRegisterMessage(pub)
// (proof of holding the private key).
type OwnerRegisterArgs struct {
	Pub string `json:"pub"`
	Sig string `json:"sig"`
}

// OwnerRotateArgs: replace the owner key. OldSig is made with the current key over
// OwnerRotateMessage(old, new); NewSig with the new key over the same bytes.
type OwnerRotateArgs struct {
	Pub    string `json:"pub"`
	OldSig string `json:"old_sig"`
	NewSig string `json:"new_sig"`
}

// OwnerInfo: reply to "owner_info".
type OwnerInfo struct {
	Registered  bool   `json:"registered"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Since       string `json:"since,omitempty"` // when the key was registered (RFC 3339)
}

// AdminMessage is what the owner signs for an administrator operation: the operation, its
// arguments in canonical JSON, and the nonce of a challenge on the connection.
func AdminMessage(op string, canonicalArgs []byte, nonce string) []byte {
	return []byte(strings.Join([]string{"ccqc/v1/admin", op, string(canonicalArgs), nonce}, "\n"))
}

// CanonicalJSON re-encodes a JSON value with sorted keys and no whitespace, so two sides that
// hold the same data sign the same bytes.
func CanonicalJSON(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return []byte("null"), nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return json.Marshal(v) // map keys are written in sorted order
}

// AdminArgs is an administrator operation: Op with Args (a JSON object), signed by the owner key
// over AdminMessage(Op, CanonicalJSON(Args), nonce).
type AdminArgs struct {
	Op   string          `json:"op"`
	Args json.RawMessage `json:"args"`
	Sig  string          `json:"sig"`
}

// Arguments of the administrator operations (no secrets: they are logged).
type AdminRole struct {
	Chat string `json:"chat"` // chat id
	Nick string `json:"nick"`
	Role string `json:"role,omitempty"`
}

type AdminConfig struct {
	Chat  string `json:"chat,omitempty"` // chat id for chat-scoped keys
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ConfigItem is one setting with its effective value.
type ConfigItem struct {
	Key     string `json:"key"`
	Chat    bool   `json:"chat"` // chat-scoped
	Value   string `json:"value"`
	Default string `json:"default"`
	Source  string `json:"source"` // default | env | setting
	Help    string `json:"help"`
}

type ConfigList struct {
	Items []ConfigItem `json:"items"`
}

// OwnerNick is reserved for the owner once one is registered.
const OwnerNick = "owner"

// ProtoVersion is the protocol level this build speaks.
const ProtoVersion = KeysProto

// LegacyProto is the last level whose clients do not sign: they join by their secret and their
// events are stored unsigned.
const LegacyProto = 3

// AckArgs: everything visible to the nick up to message ID was delivered to the session.
type AckArgs struct {
	ID int64 `json:"id"`
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
	// ClientMsgID makes a send idempotent: repeating it (after a lost reply or a broker
	// restart) returns the id of the stored message and delivers nothing again.
	ClientMsgID string `json:"client_msg_id,omitempty"`
	// Origin, Lamport and Sig: the broker's origin (Welcome.Origin), the clock value from "clock"
	// and the author's signature (see EventMessage).
	Origin  string `json:"origin,omitempty"`
	Lamport int64  `json:"lamport,omitempty"`
	Sig     string `json:"sig,omitempty"`
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
	// Owner: set by the broker, never by a client, on messages of a connection that proved it
	// holds the owner key; stored with the message.
	Owner bool `json:"owner,omitempty"`
	// Quarantined: set by the broker on output when the message comes from a node in quarantine (taken
	// before it was put there).
	Quarantined bool `json:"quarantined,omitempty"`
}

// BoardArgs is the one request of the task board; Op says what to do. Pointer fields are
// "leave alone" when nil (partial updates).
//
//	task.create task.update task.move task.delete task.get task.list
//	sprint.create sprint.update sprint.start sprint.close sprint.list
//	board.log board.revert board.workflow_get board.workflow_set
type BoardArgs struct {
	Op          string  `json:"op"`
	ID          int64   `json:"id,omitempty"` // task or sprint, by Op
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
	Assignee    *string `json:"assignee,omitempty"`
	Name        *string `json:"name,omitempty"` // sprint
	Goal        *string `json:"goal,omitempty"` // sprint
	Priority    *int    `json:"priority,omitempty"`
	Sprint      *int64  `json:"sprint,omitempty"`
	Status      string  `json:"status,omitempty"`
	Text        string  `json:"text,omitempty"`  // task.comment
	Carry       int64   `json:"carry,omitempty"` // sprint.close: where unfinished tasks go (0: backlog)
	LogID       int64   `json:"log_id,omitempty"`
	Force       bool    `json:"force,omitempty"`
	Limit       int     `json:"limit,omitempty"`
	Before      int64   `json:"before,omitempty"`
	Entity      string  `json:"entity,omitempty"` // board.log: "" the board, "task" or "sprint" (with ID)
	// task.list filters
	SprintFilter   string          `json:"sprint_filter,omitempty"` // "", active, backlog, all, or a number
	AssigneeFilter string          `json:"assignee_filter,omitempty"`
	Mine           bool            `json:"mine,omitempty"`
	Done           bool            `json:"done,omitempty"`
	Workflow       json.RawMessage `json:"workflow,omitempty"`
	Origin         string          `json:"origin,omitempty"`
	Lamport        int64           `json:"lamport,omitempty"`
	Sig            string          `json:"sig,omitempty"`
}

// DocArgs is the one request of the document tree; Op says what to do:
// tree outline get put append move delete search history revert. Pointer fields are "absent".
type DocArgs struct {
	Op      string  `json:"op"`
	Path    string  `json:"path,omitempty"`
	To      string  `json:"to,omitempty"`     // move
	Prefix  string  `json:"prefix,omitempty"` // tree, search
	Depth   int     `json:"depth,omitempty"`
	Title   *string `json:"title,omitempty"`
	Body    string  `json:"body,omitempty"`
	Text    string  `json:"text,omitempty"` // append
	Base    *int64  `json:"base,omitempty"` // the version the writer read
	Note    string  `json:"note,omitempty"`
	Section string  `json:"section,omitempty"`
	Version int64   `json:"version,omitempty"` // get, revert
	Query   string  `json:"query,omitempty"`
	Limit   int     `json:"limit,omitempty"`
	Before  int64   `json:"before,omitempty"`
	All     bool    `json:"all,omitempty"`  // tree: also return every page path
	Full    bool    `json:"full,omitempty"` // get: the whole page whatever its size (people, not models)
	Origin  string  `json:"origin,omitempty"`
	Lamport int64   `json:"lamport,omitempty"`
	Sig     string  `json:"sig,omitempty"`
}

// DocReply: Text is for people and models; Body is a page alone (get); Paths every live page of a
// tree request with all=true.
type DocReply struct {
	Text    string   `json:"text"`
	Body    string   `json:"body,omitempty"`
	Paths   []string `json:"paths,omitempty"`
	Version int64    `json:"version,omitempty"`
}

// BoardReply: the board's answer is text, short by design.
type BoardReply struct {
	Text string `json:"text"`
}

// Event kinds: "msg", "presence", "board" (a change of a task assigned to you; Wake says whether
// it should wake the session).
type Event struct {
	Kind string   `json:"kind"`
	Wake bool     `json:"wake,omitempty"`
	Msg  *Message `json:"msg,omitempty"`
	ToMe bool     `json:"to_me,omitempty"`
	Text string   `json:"text,omitempty"`
}

type MemberInfo struct {
	Name          string `json:"name"`
	Role          string `json:"role,omitempty"`
	RoleExclusive bool   `json:"role_exclusive,omitempty"`
	Online        bool   `json:"online"`
	Unsigned      bool   `json:"unsigned,omitempty"`    // present without a signing key (older client)
	Owner         bool   `json:"owner,omitempty"`       // an owner-verified connection
	Quarantined   bool   `json:"quarantined,omitempty"` // a member of a node in quarantine
	RolePinned    bool   `json:"role_pinned,omitempty"`
	// Status of a present member (not stored; see StatusArgs). Activity is "busy", "free" or ""
	// when the member's client does not report it.
	Activity  string `json:"activity,omitempty"`
	BusySince string `json:"busy_since,omitempty"` // RFC 3339, while busy
	Folder    string `json:"folder,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Note      string `json:"note,omitempty"` // free text set by status_set
}

// Limits of a member's status.
const (
	MaxStatusText = 200
	MaxFolder     = 64
	MaxBranch     = 100
)

// StatusArgs: a partial update of the sender's status; absent fields are left as they are.
// Text "" clears the note.
type StatusArgs struct {
	Text   *string `json:"text,omitempty"`
	Busy   *bool   `json:"busy,omitempty"`
	Folder *string `json:"folder,omitempty"`
	Branch *string `json:"branch,omitempty"`
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
	Proto       int     `json:"proto,omitempty"`        // protocol level of the broker; 0 = older than levels
	Origin      string  `json:"origin,omitempty"`       // the broker's identity, for signed writes (proto 4)
}

// BrokerInfo: reply to "ping"; shown by `ccqc broker status`.
type BrokerInfo struct {
	Version string `json:"version"`
	Proto   int    `json:"proto,omitempty"` // protocol level; 0 = a broker from before levels existed
	PID     int    `json:"pid"`
	Started string `json:"started"`
	Chats   int    `json:"chats"`   // loaded chats
	Clients int    `json:"clients"` // connected members
	Store   string `json:"store"`
	Warning string `json:"warning,omitempty"`
	// ByProto: connected members per protocol level of their client ("0" = no acks).
	ByProto map[string]int `json:"by_proto,omitempty"`
	// Node: this broker's identity (origin). Owner: fingerprint of the registered owner key, ""
	// while no owner is initialized.
	Node       string `json:"node,omitempty"`
	Owner      string `json:"owner,omitempty"`
	OwnerSince string `json:"owner_since,omitempty"`
}

type SendResult struct {
	ID  int64 `json:"id"`
	Dup bool  `json:"dup,omitempty"` // the message had been stored by an earlier attempt
}

type HistoryResult struct {
	Messages []Message `json:"messages"`
}
