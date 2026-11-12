package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/events"
	"github.com/PHPCraftdream/cc-quant-chat/internal/invite"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

var _ store.Invites = (*DB)(nil)

// NewChat makes a chat. The id is random; a chat of that id is an error.
func (d *DB) NewChat(chatID, name string) error {
	var have int
	if err := d.node.QueryRow(`SELECT COUNT(*) FROM chats WHERE chat_id = ?`, chatID).Scan(&have); err != nil {
		return err
	}
	if have > 0 {
		return fmt.Errorf("sqlite: a chat with that id exists already")
	}
	_, done, err := d.acquire(chatID, name, true)
	defer done()
	return err
}

func (d *DB) ChatName(chatID string) (string, error) {
	var name string
	err := d.node.QueryRow(`SELECT name FROM chats WHERE chat_id = ?`, chatID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return name, err
}

func (d *DB) FindChats(name string) ([]store.ChatRef, error) {
	rows, err := d.node.Query(`SELECT chat_id, name FROM chats WHERE name = ? ORDER BY created_at, chat_id`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.ChatRef
	for rows.Next() {
		var r store.ChatRef
		if err := rows.Scan(&r.ID, &r.Name); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// inviteEvent writes the event of an administrator operation on an invitation: the owner's
// signature of the operation travels with it; the invitation is named by its id, never by anything
// that could be used.
func (c *chatDB) inviteEvent(tx *sql.Tx, kind string, a *store.AdminAttest, fields map[string]any) error {
	lam, err := chatClock(tx)
	if err != nil {
		return err
	}
	assigned := map[string]any{}
	e := events.Event{Lamport: lam, Kind: kind, TS: now()}
	if a != nil { // the owner's signature of the operation travels with the event
		assigned["admin"] = map[string]any{"op": a.Op, "nonce": a.Nonce}
		e.Payload, e.AuthorPub, e.Sig = a.Args, a.Pub, a.Sig
	} else {
		e.Payload = "{}"
	}
	for k, v := range fields {
		assigned[k] = v
	}
	if e.Assigned, err = jsonText(assigned); err != nil {
		return err
	}
	_, err = c.appendEvent(tx, e)
	return err
}

func (d *DB) IssueInvite(chatID string, rec invite.Record, a store.AdminAttest) error {
	c, done, err := d.acquire(chatID, "", false)
	defer done()
	if err != nil {
		return err
	}
	if c == nil {
		return fmt.Errorf("sqlite: no chat %.8s", chatID)
	}
	return c.inTx(func(tx *sql.Tx) error {
		if err := (invite.SQLStore{DB: tx}).Insert(rec); err != nil {
			return err
		}
		return c.inviteEvent(tx, "invite.issue", &a, map[string]any{"invite": map[string]any{"id": rec.ID, "nick": rec.Scope, "exp": rec.ExpiresAt.Unix()}})
	})
}

func (d *DB) RevokeInvite(chatID, id string, a store.AdminAttest) (bool, error) {
	c, done, err := d.acquire(chatID, "", false)
	defer done()
	if err != nil {
		return false, err
	}
	if c == nil {
		return false, fmt.Errorf("sqlite: no chat %.8s", chatID)
	}
	var revoked bool
	err = c.inTx(func(tx *sql.Tx) error {
		var err error
		if revoked, err = (invite.SQLStore{DB: tx}).Revoke(id); err != nil || !revoked {
			return err
		}
		return c.inviteEvent(tx, "invite.revoke", &a, map[string]any{"invite": map[string]any{"id": id}})
	})
	return revoked, err
}

func (d *DB) ListInvites(chatID string) ([]invite.Record, error) {
	c, done, err := d.acquire(chatID, "", false)
	defer done()
	if err != nil || c == nil {
		return nil, err
	}
	return invite.SQLStore{DB: c.db}.List(invite.KindChat)
}

// JoinByInvite uses an invitation to register a nick (or replace its key): r is the record that does
// it. Using the invitation, the nick's record and the events that say so are one transaction; a
// wrong proof counts against the invitation and is journaled (invite.fail), anything else that makes
// the invitation unusable writes nothing. See invite.Redeem for the errors.
func (d *DB) JoinByInvite(chatID, id string, now time.Time, usedBy string, proofOK func(invite.Record) bool, r store.Record) (invite.Record, error) {
	c, done, err := d.acquire(chatID, "", false)
	defer done()
	if err != nil {
		return invite.Record{}, err
	}
	if c == nil {
		return invite.Record{}, fmt.Errorf("sqlite: no chat %.8s", chatID)
	}
	var rec invite.Record
	var used error
	err = c.inTx(func(tx *sql.Tx) error {
		rec, used = invite.Redeem(invite.SQLStore{DB: tx}, id, now, usedBy, proofOK)
		var bad *invite.BadProof
		switch {
		case used == nil:
			return c.appendIn(tx, r, map[string]any{"invite": map[string]any{"id": rec.ID, "issued_by": rec.IssuedBy}})
		case errors.As(used, &bad):
			return c.inviteEvent(tx, "invite.fail", nil, map[string]any{"invite": map[string]any{"id": bad.Record.ID, "nick": bad.Record.Scope, "tries": bad.Tries, "burnt": bad.Burnt}})
		case errors.Is(used, invite.ErrInvalid):
			return nil // nothing to write: nothing is known about what was tried
		}
		return used
	})
	if err != nil {
		return invite.Record{}, err
	}
	return rec, used
}
