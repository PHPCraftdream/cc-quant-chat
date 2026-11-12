package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/PHPCraftdream/cc-quant-chat/internal/events"
	"github.com/PHPCraftdream/cc-quant-chat/internal/node"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// The events of other nodes, applied to the projections (docs/design-federation.md §4). The nodes
// number their own things (#id, T-n, vN) alone, so what arrives is given a number of this node; the
// name that is the same everywhere is (origin, seq) of the event that made the thing (entity_refs).
// A member of another node is called nick@abcd here, abcd being the first characters of its node's
// origin; this node's own members have no suffix, and a reference to one of them in an event of
// another node (nick@<our origin>) is turned back into the bare name.
//
// applyRemote is the one function that does it, for an event just accepted and for a rebuild alike,
// so that a rebuilt projection is the live one. It writes the projections and returns what the
// broker must learn of, as records, in the numbers of this node.

const (
	metaRefs = "refs_version"
	refsDone = "1"

	placeholderLamport = math.MaxInt64 // a room that a message or a member named before its creation arrived
	placeholderOrigin  = "~"
)

// remoteName is the name this node gives to a nick that an event of origin mentions: nick@abcd, abcd
// being the short name of the origin (shortOf). A reference to one of our own members is written by
// the other node nick@<the start of our origin>, whatever length it gave it: it is the bare name.
func remoteName(tx *sql.Tx, self, origin, name string) string {
	if i := strings.LastIndex(name, "@"); i >= 0 {
		if suffix := name[i+1:]; len(suffix) >= 4 && strings.HasPrefix(self, suffix) {
			return name[:i]
		}
		return name
	}
	return name + "@" + shortOf(tx, origin)
}

// shortOf is the short name of an origin in this chat: the first four characters of it, or more
// when another origin of the chat already has those (two keys can be ground to one prefix). It is
// given once, in the order the origins first appear, and never changes.
func shortOf(tx *sql.Tx, origin string) string {
	var short string
	if err := tx.QueryRow(`SELECT short FROM origin_short WHERE origin = ?`, origin).Scan(&short); err == nil {
		return short
	}
	for n := 4; n <= len(origin); n += 4 {
		short = origin[:n]
		var taken int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM origin_short WHERE short = ?`, short).Scan(&taken); err != nil || taken > 0 {
			if err != nil {
				break
			}
			continue
		}
		if _, err := tx.Exec(`INSERT INTO origin_short (origin, short) VALUES (?, ?)`, origin, short); err == nil {
			return short
		}
		break
	}
	return node.Short(origin)
}

type eventRef struct {
	Origin string `json:"o"`
	Seq    int64  `json:"s"`
}

func applyRemote(tx *sql.Tx, e events.Event, self string) ([]store.Record, error) {
	if e.Redacted() {
		return nil, nil // kept for the chain only
	}
	switch {
	case e.Kind == "msg":
		return remoteMsg(tx, e, self)
	case e.Kind == "nick.register", e.Kind == "nick.key", e.Kind == "nick.reset":
		return remoteNick(tx, e, self)
	case strings.HasPrefix(e.Kind, "room."):
		return remoteRoom(tx, e, self)
	case isBoardKind(e.Kind):
		return nil, remoteBoard(tx, e, self)
	case isDocKind(e.Kind):
		return nil, remoteDoc(tx, e, self)
	}
	// Pinned roles, settings and snapshots are a node's own business.
	return nil, nil
}

func remoteMsg(tx *sql.Tx, e events.Event, self string) ([]store.Record, error) {
	var a msgAssigned
	if err := json.Unmarshal([]byte(e.Assigned), &a); err != nil {
		return nil, fmt.Errorf("message: assigned: %w", err)
	}
	p, err := decodeMap(e.Payload)
	if err != nil {
		return nil, fmt.Errorf("message: payload: %w", err)
	}
	room := str(p, "room")
	if room == proto.RoomDirect {
		return nil, errors.New("a direct message of another node: direct messages between nodes are not supported")
	}
	if !proto.ValidName(room) || a.From == "" || strings.Contains(a.From, "@") {
		return nil, errors.New("message: a malformed room or sender")
	}
	if err := checkAuthor(tx, e, a.From); err != nil { // a nick with a key is spoken for only with that key
		return nil, err
	}
	var recs []store.Record
	var rooms int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM rooms WHERE room = ?`, room).Scan(&rooms); err != nil {
		return nil, err
	}
	if rooms == 0 { // the sender joined it before (the join of the same origin comes first): a room that is not here is not the room of a member
		return nil, fmt.Errorf("message of %s: there is no room @%s here", a.From, room)
	}
	var registered, member int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM remote_nicks WHERE origin = ? AND nick = ?`, e.Origin, a.From).Scan(&registered); err != nil {
		return nil, err
	}
	if registered > 0 {
		if err := tx.QueryRow(`SELECT COUNT(*) FROM room_members WHERE room = ? AND nick = ?`, room, remoteName(tx, self, e.Origin, a.From)).Scan(&member); err != nil {
			return nil, err
		}
		if member == 0 {
			return nil, fmt.Errorf("message of %s: it is not in @%s", a.From, room)
		}
	}
	var id int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(id), 0) + 1 FROM messages`).Scan(&id); err != nil {
		return nil, err
	}
	var replyTo int64
	if a.ReplyRef != nil {
		replyTo, _ = localID(tx, "msg", a.ReplyRef.Origin, a.ReplyRef.Seq) // 0: what it replies to is not held
	}
	to := ""
	if t := str(p, "to"); t != "" {
		to = remoteName(tx, self, e.Origin, t)
	}
	from := remoteName(tx, self, e.Origin, a.From)
	payload := ""
	if e.Sig != "" {
		payload = e.Payload
	}
	if _, err := tx.Exec(`INSERT INTO messages (id, thread, room, from_nick, from_role, to_nick, text, reply_to, ts, client_msg_id, owner, origin, lamport, author_pub, sig, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', 0, ?, ?, ?, ?, ?)`,
		id, "r:"+room, room, from, a.FromRole, to, str(p, "text"), replyTo, e.TS, e.Origin, e.Lamport, e.AuthorPub, e.Sig, payload); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`INSERT INTO entity_refs (kind, id, origin, seq, num) VALUES ('msg', ?, ?, ?, ?)`, id, e.Origin, e.Seq, a.ID); err != nil {
		return nil, err
	}
	msg := &proto.Message{ID: id, TS: e.TS, Room: room, From: from, FromRole: a.FromRole, To: to, Text: str(p, "text"), ReplyTo: replyTo}
	return append(recs, store.Record{T: "msg", Key: "r:" + room, Msg: msg, Origin: e.Origin}), nil
}

// localID is the number this node shows a thing under, from the (origin, seq) of the event that made it.
func localID(tx *sql.Tx, kind, origin string, seq int64) (int64, bool) {
	var id int64
	if err := tx.QueryRow(`SELECT id FROM entity_refs WHERE kind = ? AND origin = ? AND seq = ?`, kind, origin, seq).Scan(&id); err != nil {
		return 0, false
	}
	return id, true
}

// refOf is the name that is the same everywhere for a thing shown under a local number.
func refOf(tx *sql.Tx, kind string, id int64) (*eventRef, error) {
	var r eventRef
	err := tx.QueryRow(`SELECT origin, seq FROM entity_refs WHERE kind = ? AND id = ?`, kind, id).Scan(&r.Origin, &r.Seq)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &r, err
}

// withReplyRef adds to the assigned part of a message event the global name of the message it
// replies to (the number in the signed request is the author's own).
func withReplyRef(tx *sql.Tx, assigned string, replyTo int64) (string, error) {
	ref, err := refOf(tx, "msg", replyTo)
	if err != nil || ref == nil {
		return assigned, err
	}
	var a msgAssigned
	if err := json.Unmarshal([]byte(assigned), &a); err != nil {
		return "", err
	}
	a.ReplyRef = ref
	return jsonText(a)
}

// ensureRoom makes the room if there is none: a message or a member may be named before the
// creation of the room has arrived (it came from another node). The room made so has the latest
// possible stamp, so that the real creation replaces it.
func ensureRoom(tx *sql.Tx, room string) ([]store.Record, error) {
	res, err := tx.Exec(`INSERT OR IGNORE INTO rooms (room, topic, created_by, lamport, origin) VALUES (?, '', '', ?, ?)`, room, int64(placeholderLamport), placeholderOrigin)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil
	}
	return []store.Record{{T: "room", Room: room}}, nil
}

func remoteNick(tx *sql.Tx, e events.Event, self string) ([]store.Record, error) {
	p, err := decodeMap(e.Payload)
	if err != nil {
		return nil, fmt.Errorf("%s: payload: %w", e.Kind, err)
	}
	nick := str(p, "nick")
	if nick == "" || strings.Contains(nick, "@") {
		return nil, errors.New("a nick of another node: a malformed name")
	}
	name, pub := remoteName(tx, self, e.Origin, nick), str(p, "pub")
	switch e.Kind {
	case "nick.register":
		if _, err := tx.Exec(`INSERT INTO nicks (nick, key_hash, seen, pubkey) VALUES (?, '', 0, ?)
			ON CONFLICT (nick) DO UPDATE SET pubkey = excluded.pubkey`, name, pub); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO room_members (room, nick) VALUES (?, ?)`, proto.RoomCommon, name); err != nil {
			return nil, err
		}
	case "nick.key":
		if _, err := tx.Exec(`UPDATE nicks SET pubkey = ? WHERE nick = ?`, pub, name); err != nil {
			return nil, err
		}
	case "nick.reset":
		pub = ""
		if _, err := tx.Exec(`UPDATE nicks SET pubkey = '' WHERE nick = ?`, name); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO remote_nicks (origin, nick, pub) VALUES (?, ?, ?)`, e.Origin, nick, pub); err != nil {
		return nil, err
	}
	if e.Kind == "nick.register" {
		return []store.Record{{T: "nick", Nick: name, Pub: pub}}, nil
	}
	return []store.Record{{T: "nick_key", Nick: name, Pub: pub}}, nil
}

func remoteRoom(tx *sql.Tx, e events.Event, self string) ([]store.Record, error) {
	p, err := decodeMap(e.Payload)
	if err != nil {
		return nil, fmt.Errorf("%s: payload: %w", e.Kind, err)
	}
	room := str(p, "room")
	if !proto.ValidName(room) || room == proto.RoomDirect {
		return nil, errors.New("a room of another node: a malformed name")
	}
	switch e.Kind {
	case "room.create":
		by := ""
		if c := str(p, "created_by"); c != "" {
			by = remoteName(tx, self, e.Origin, c)
		}
		if room == proto.RoomCommon {
			return nil, nil
		}
		if err := upsertRoom(tx, room, str(p, "topic"), by, e.Lamport, e.Origin); err != nil {
			return nil, err
		}
		var l int64
		var o string
		if err := tx.QueryRow(`SELECT lamport, origin FROM rooms WHERE room = ?`, room).Scan(&l, &o); err != nil {
			return nil, err
		}
		if l != e.Lamport || o != e.Origin { // another creation is the one that stands
			return nil, nil
		}
		return []store.Record{{T: "room", Room: room, Topic: str(p, "topic"), Nick: by}}, nil
	case "room.join", "room.leave":
		nick := str(p, "nick")
		if nick == "" || strings.Contains(nick, "@") {
			return nil, errors.New("a member of another node: a malformed name")
		}
		name := remoteName(tx, self, e.Origin, nick)
		if e.Kind == "room.leave" {
			if _, err := tx.Exec(`DELETE FROM room_members WHERE room = ? AND nick = ?`, room, name); err != nil {
				return nil, err
			}
			return []store.Record{{T: "out", Room: room, Nick: name}}, nil
		}
		recs, err := ensureRoom(tx, room)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO room_members (room, nick) VALUES (?, ?)`, room, name); err != nil {
			return nil, err
		}
		return append(recs, store.Record{T: "in", Room: room, Nick: name}), nil
	}
	return nil, nil
}

// upsertRoom writes the creation of a room. Of two creations of one room (two nodes made it) the
// one with the smaller (lamport, origin) stands, whichever arrives first.
func upsertRoom(tx *sql.Tx, room, topic, by string, lamport int64, origin string) error {
	_, err := tx.Exec(`INSERT INTO rooms (room, topic, created_by, lamport, origin) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (room) DO UPDATE SET topic = excluded.topic, created_by = excluded.created_by, lamport = excluded.lamport, origin = excluded.origin
		WHERE rooms.origin <> '' AND (excluded.lamport, excluded.origin) < (rooms.lamport, rooms.origin)`,
		room, topic, by, lamport, origin)
	return err
}

// derivedRefs fills what is derived from the events of a chat written before there were events of
// other nodes: the global names of messages and the stamps of rooms. It runs once for such a chat,
// after the rows that predate the journal are in it.
func derivedRefs(tx *sql.Tx) error {
	if _, err := tx.Exec(`INSERT OR IGNORE INTO entity_refs (kind, id, origin, seq, num)
		SELECT 'msg', CAST(json_extract(assigned, '$.id') AS INTEGER), origin, seq, CAST(json_extract(assigned, '$.id') AS INTEGER) FROM events
		WHERE kind = 'msg' AND remote = 0 AND hidden = '' AND json_extract(assigned, '$.id') IS NOT NULL`); err != nil {
		return err
	}
	if err := deriveBoardRefs(tx); err != nil {
		return err
	}
	if err := deriveDocRefs(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE rooms SET
		lamport = COALESCE((SELECT e.lamport FROM events e WHERE e.kind = 'room.create' AND json_extract(e.payload, '$.room') = rooms.room ORDER BY e.ord LIMIT 1), 0),
		origin = COALESCE((SELECT e.origin FROM events e WHERE e.kind = 'room.create' AND json_extract(e.payload, '$.room') = rooms.room ORDER BY e.ord LIMIT 1), '')`); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)`, metaRefs, refsDone)
	return err
}

// upgradeRefs runs derivedRefs for a chat that has not had it.
func (c *chatDB) upgradeRefs() error {
	var v string
	err := c.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, metaRefs).Scan(&v)
	if err == nil && v == refsDone {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return c.inTx(derivedRefs)
}
