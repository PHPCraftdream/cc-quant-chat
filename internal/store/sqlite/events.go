package sqlite

import (
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/events"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// Every change of a chat is written as an event, and the event is applied to the projections by
// applyEvent: the same function a rebuild uses, so a rebuilt projection is the live one. Rows of
// the board and of documents are written by their own code; their events carry images of the
// rows (see rows.go) and a rebuild writes the images back.

type nodeKey struct {
	origin string
	priv   ed25519.PrivateKey
}

// SetNode gives the database the identity events are signed with. Without it events are written
// unsigned under the origin their writer names (unit tests).
func (d *DB) SetNode(origin string, priv ed25519.PrivateKey) { d.node = &nodeKey{origin, priv} }

func (d *DB) originOr(fallback string) string {
	if d.node != nil {
		return d.node.origin
	}
	return fallback
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// jsonText is the canonical JSON of v (sorted keys, no spaces), as stored in payload/assigned.
func jsonText(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	c, err := proto.CanonicalJSON(raw)
	return string(c), err
}

func putEvent(tx *sql.Tx, e events.Event) error {
	_, err := tx.Exec(`INSERT INTO events (chat_id, origin, seq, prev, lamport, kind, payload, author_pub, sig, assigned, ts, node_sig)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ChatID, e.Origin, e.Seq, e.Prev, e.Lamport, e.Kind, e.Payload, e.AuthorPub, e.Sig, e.Assigned, e.TS, e.NodeSig)
	return err
}

// chainTail: the last event of (chat, origin): its seq and ID (0 and "" for none).
func chainTail(tx *sql.Tx, chatID, origin string) (int64, string, error) {
	e := events.Event{ChatID: chatID, Origin: origin}
	err := tx.QueryRow(`SELECT seq, prev, lamport, kind, payload, author_pub, sig, assigned, ts FROM events
		WHERE chat_id = ? AND origin = ? ORDER BY seq DESC LIMIT 1`, chatID, origin).
		Scan(&e.Seq, &e.Prev, &e.Lamport, &e.Kind, &e.Payload, &e.AuthorPub, &e.Sig, &e.Assigned, &e.TS)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil
	}
	return e.Seq, e.ID(), err
}

// appendEvent chains the event to the node's journal of its chat, signs it and stores it.
func (d *DB) appendEvent(tx *sql.Tx, e events.Event) (events.Event, error) {
	e.Origin = d.originOr(e.Origin)
	seq, prev, err := chainTail(tx, e.ChatID, e.Origin)
	if err != nil {
		return e, err
	}
	e.Seq, e.Prev = seq+1, prev
	if d.node != nil {
		e.Sign(d.node.priv)
	}
	return e, putEvent(tx, e)
}

func chatClock(tx *sql.Tx, chatID string) (int64, error) {
	var n int64
	err := tx.QueryRow(`SELECT lamport FROM chats WHERE id = ?`, chatID).Scan(&n)
	return n, err
}

// tick advances the chat's logical clock and returns the new value.
func tick(tx *sql.Tx, chatID string) (int64, error) {
	if _, err := tx.Exec(`UPDATE chats SET lamport = lamport + 1 WHERE id = ?`, chatID); err != nil {
		return 0, err
	}
	return chatClock(tx, chatID)
}

// msgPayload is the canonical request of a message: what a signing client signed, rebuilt for an
// unsigned one from the fields it sent.
func msgPayload(m *proto.Message, clientID string) (string, error) {
	v := map[string]any{"room": m.Room, "text": m.Text}
	if m.To != "" {
		v["to"] = m.To
	}
	if m.ReplyTo != 0 {
		v["reply_to"] = m.ReplyTo
	}
	if clientID != "" {
		v["client_msg_id"] = clientID
	}
	return jsonText(v)
}

type msgAssigned struct {
	ID       int64  `json:"id"`
	Thread   string `json:"thread"`
	From     string `json:"from"`
	FromRole string `json:"from_role,omitempty"`
	Owner    bool   `json:"owner,omitempty"`
}

// recordEvent turns a record into the event it stands for. ok is false for records that are not
// events (the read cursor). lamport is the chat's clock for events that do not advance it.
func recordEvent(chatID string, r store.Record, lamport int64) (e events.Event, ok bool, err error) {
	e = events.Event{ChatID: chatID, Origin: r.Origin, Lamport: lamport, TS: now()}
	var payload, assigned any
	switch r.T {
	case "nick":
		e.Kind, payload, assigned = "nick.register", map[string]any{"nick": r.Nick, "pub": r.Pub}, map[string]any{"hash": r.Hash, "seen": r.ID}
	case "nick_key":
		e.Kind, payload, assigned = "nick.key", map[string]any{"nick": r.Nick, "pub": r.Pub}, map[string]any{"hash": r.Hash}
		if r.Hash == "" && r.Pub == "" {
			e.Kind, payload = "nick.reset", map[string]any{"nick": r.Nick}
		}
	case "pin":
		e.Kind, payload = "nick.pin", map[string]any{"nick": r.Nick, "role": r.Role}
	case "room":
		e.Kind, payload = "room.create", map[string]any{"room": r.Room, "topic": r.Topic, "created_by": r.Nick}
	case "in":
		e.Kind, payload = "room.join", map[string]any{"room": r.Room, "nick": r.Nick}
	case "out":
		e.Kind, payload = "room.leave", map[string]any{"room": r.Room, "nick": r.Nick}
	case "msg":
		if r.Msg == nil {
			return e, false, fmt.Errorf("msg record without message")
		}
		m := r.Msg
		e.Kind, e.TS, e.AuthorPub, e.Sig = "msg", m.TS, r.AuthorPub, r.Sig
		if r.Payload != "" {
			e.Payload = r.Payload
		} else if e.Payload, err = msgPayload(m, r.ClientID); err != nil {
			return e, false, err
		}
		assigned = msgAssigned{ID: m.ID, Thread: r.Key, From: m.From, FromRole: m.FromRole, Owner: m.Owner}
	default:
		return e, false, nil
	}
	if r.T != "msg" {
		if e.Payload, err = jsonText(payload); err != nil {
			return e, false, err
		}
	}
	if assigned == nil {
		assigned = map[string]any{}
	}
	if a := r.Admin; a != nil && r.T != "msg" { // the owner's signature of the operation travels with the event
		e.Payload, e.AuthorPub, e.Sig = a.Args, a.Pub, a.Sig
		m, _ := assigned.(map[string]any)
		if m == nil {
			m = map[string]any{}
		}
		m["admin"] = map[string]any{"op": a.Op, "nonce": a.Nonce}
		assigned = m
	}
	if e.Assigned, err = jsonText(assigned); err != nil {
		return e, false, err
	}
	return e, true, nil
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func decodeMap(s string) (map[string]any, error) {
	var m map[string]any
	if s == "" {
		return map[string]any{}, nil
	}
	return m, json.Unmarshal([]byte(s), &m)
}

// applyEvent writes the projections of one event. It does not touch the chat's clock.
func applyEvent(tx *sql.Tx, e events.Event) error {
	switch {
	case e.Kind == "msg":
		return applyMsg(tx, e)
	case strings.HasPrefix(e.Kind, "nick."), strings.HasPrefix(e.Kind, "room."), e.Kind == "config.set":
		return applySimple(tx, e)
	}
	return applyImages(tx, e) // the board and documents
}

func applyMsg(tx *sql.Tx, e events.Event) error {
	var a msgAssigned
	if err := json.Unmarshal([]byte(e.Assigned), &a); err != nil {
		return fmt.Errorf("message event %d: assigned: %w", e.Seq, err)
	}
	p, err := decodeMap(e.Payload)
	if err != nil {
		return fmt.Errorf("message event %d: payload: %w", e.Seq, err)
	}
	var replyTo int64
	if f, ok := p["reply_to"].(float64); ok {
		replyTo = int64(f)
	}
	payload := ""
	if e.Sig != "" {
		payload = e.Payload // kept with the row the way the signature needs it
	}
	_, err = tx.Exec(`INSERT INTO messages (chat_id, id, thread, room, from_nick, from_role, to_nick, text, reply_to, ts, client_msg_id, owner, origin, lamport, author_pub, sig, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ChatID, a.ID, a.Thread, str(p, "room"), a.From, a.FromRole, str(p, "to"), str(p, "text"), replyTo, e.TS,
		str(p, "client_msg_id"), a.Owner, e.Origin, e.Lamport, e.AuthorPub, e.Sig, payload)
	return err
}

func applySimple(tx *sql.Tx, e events.Event) error {
	p, err := decodeMap(e.Payload)
	if err != nil {
		return fmt.Errorf("%s event %d: payload: %w", e.Kind, e.Seq, err)
	}
	a, err := decodeMap(e.Assigned)
	if err != nil {
		return fmt.Errorf("%s event %d: assigned: %w", e.Kind, e.Seq, err)
	}
	switch e.Kind {
	case "nick.register":
		// Plain INSERT: an existing nick keeps its key; a clash is an error, never an overwrite.
		// The nick and its place in @common are written together.
		var seen int64
		if f, ok := a["seen"].(float64); ok {
			seen = int64(f)
		}
		if _, err := tx.Exec(`INSERT INTO nicks (chat_id, nick, key_hash, seen, pubkey) VALUES (?, ?, ?, ?, ?)`,
			e.ChatID, str(p, "nick"), str(a, "hash"), seen, str(p, "pub")); err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT OR IGNORE INTO room_members (chat_id, room, nick) VALUES (?, ?, ?)`, e.ChatID, proto.RoomCommon, str(p, "nick"))
		return err
	case "nick.key", "nick.reset": // the nick's credentials as they are now
		_, err = tx.Exec(`UPDATE nicks SET key_hash = ?, pubkey = ? WHERE chat_id = ? AND nick = ?`, str(a, "hash"), str(p, "pub"), e.ChatID, str(p, "nick"))
	case "nick.pin":
		_, err = tx.Exec(`UPDATE nicks SET pinned_role = ? WHERE chat_id = ? AND nick = ?`, str(p, "role"), e.ChatID, str(p, "nick"))
	case "room.create":
		_, err = tx.Exec(`INSERT OR IGNORE INTO rooms (chat_id, room, topic, created_by) VALUES (?, ?, ?, ?)`,
			e.ChatID, str(p, "room"), str(p, "topic"), str(p, "created_by"))
	case "room.join":
		_, err = tx.Exec(`INSERT OR IGNORE INTO room_members (chat_id, room, nick) VALUES (?, ?, ?)`, e.ChatID, str(p, "room"), str(p, "nick"))
	case "room.leave":
		_, err = tx.Exec(`DELETE FROM room_members WHERE chat_id = ? AND room = ? AND nick = ?`, e.ChatID, str(p, "room"), str(p, "nick"))
	case "config.set": // the stored value may be the normalized form of the one that was signed
		value := str(p, "value")
		if v, ok := a["value"].(string); ok {
			value = v
		}
		_, err = tx.Exec(`INSERT OR REPLACE INTO settings (scope, key, value) VALUES (?, ?, ?)`, e.ChatID, str(p, "key"), value)
	default:
		return fmt.Errorf("unknown event kind %q", e.Kind)
	}
	return err
}

func (d *DB) setChatSetting(chatID, key, value string) error {
	return d.chatSetting(chatID, key, value, nil)
}

// SetChatSettingAttested is setChatSetting for a setting changed by a signed administrator
// operation: the event carries the owner's signature.
func (d *DB) SetChatSettingAttested(chatID, key, value string, a store.AdminAttest) error {
	return d.chatSetting(chatID, key, value, &a)
}

// chatSetting stores a chat-scoped setting as an event of that chat.
func (d *DB) chatSetting(chatID, key, value string, a *store.AdminAttest) error {
	return d.inTx(func(tx *sql.Tx) error {
		var one int
		if err := tx.QueryRow(`SELECT 1 FROM chats WHERE id = ?`, chatID).Scan(&one); err != nil {
			return err
		}
		lam, err := chatClock(tx, chatID)
		if err != nil {
			return err
		}
		e := events.Event{ChatID: chatID, Lamport: lam, Kind: "config.set", TS: now()}
		assigned := map[string]any{"value": value}
		if a != nil {
			e.Payload, e.AuthorPub, e.Sig = a.Args, a.Pub, a.Sig
			assigned["admin"] = map[string]any{"op": a.Op, "nonce": a.Nonce}
		} else if e.Payload, err = jsonText(map[string]any{"key": key, "value": value}); err != nil {
			return err
		}
		if e.Assigned, err = jsonText(assigned); err != nil {
			return err
		}
		if e, err = d.appendEvent(tx, e); err != nil {
			return err
		}
		return applyEvent(tx, e)
	})
}
