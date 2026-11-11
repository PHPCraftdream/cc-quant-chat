package sqlite

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/PHPCraftdream/cc-quant-chat/internal/events"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

var _ store.Federation = (*DB)(nil)

// ---- the nodes (node.db)

// AddPeer lists a node (or changes where to reach it). One added by hand is active and trusted;
// its level is changed with SetPeerTrust.
func (d *DB) AddPeer(origin, addr string) error {
	_, err := d.node.Exec(`INSERT INTO peers (origin, addr, added_at) VALUES (?, ?, ?)
		ON CONFLICT (origin) DO UPDATE SET addr = excluded.addr`, origin, addr, now())
	return err
}

// SetPeerTrust changes what this node accepts from a node.
func (d *DB) SetPeerTrust(origin, level string) (string, error) {
	if !store.ValidTrust(level) {
		return "", fmt.Errorf("trust is read, member or trusted, not %q", level)
	}
	var old string
	if err := d.node.QueryRow(`SELECT trust FROM peers WHERE origin = ?`, origin).Scan(&old); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("the node %s is not listed", origin)
		}
		return "", err
	}
	_, err := d.node.Exec(`UPDATE peers SET trust = ? WHERE origin = ?`, level, origin)
	return old, err
}

// RemovePeer forgets a node and what is shared with it.
func (d *DB) RemovePeer(origin string) error {
	tx, err := d.node.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	for _, q := range []string{`DELETE FROM peer_chats WHERE origin = ?`, `DELETE FROM peers WHERE origin = ?`} {
		if _, err := tx.Exec(q, origin); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Peers lists the nodes with the chats shared with each.
func (d *DB) Peers() ([]store.Peer, error) {
	rows, err := d.node.Query(`SELECT origin, addr, trust, state, pending_until, COALESCE(pubkey, x''), added_at FROM peers ORDER BY origin`)
	if err != nil {
		return nil, err
	}
	var out []store.Peer
	for rows.Next() {
		var p store.Peer
		if err := rows.Scan(&p.Origin, &p.Addr, &p.Trust, &p.State, &p.PendingUntil, &p.Pubkey, &p.AddedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Chats, err = d.chatsSharedWith(out[i].Origin); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (d *DB) chatsSharedWith(origin string) ([]string, error) {
	return d.column(`SELECT chat_id FROM peer_chats WHERE origin = ? ORDER BY chat_id`, origin)
}

func (d *DB) column(q string, args ...any) ([]string, error) {
	rows, err := d.node.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SharePeerChat shares a chat with a node that has been added.
func (d *DB) SharePeerChat(chatID, origin string) error {
	var one int
	if err := d.node.QueryRow(`SELECT 1 FROM peers WHERE origin = ?`, origin).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("no peer %s: add it first", origin)
	} else if err != nil {
		return err
	}
	_, err := d.node.Exec(`INSERT OR IGNORE INTO peer_chats (origin, chat_id) VALUES (?, ?)`, origin, chatID)
	return err
}

func (d *DB) UnsharePeerChat(chatID, origin string) error {
	_, err := d.node.Exec(`DELETE FROM peer_chats WHERE origin = ? AND chat_id = ?`, origin, chatID)
	return err
}

func (d *DB) SharedWith(chatID string) ([]string, error) {
	return d.column(`SELECT origin FROM peer_chats WHERE chat_id = ? ORDER BY origin`, chatID)
}

// FlagOrigin marks a node whose events are no longer accepted, with the reason and the proof.
func (d *DB) FlagOrigin(origin, reason, evidence string) error {
	_, err := d.node.Exec(`INSERT OR IGNORE INTO flagged_origins (origin, reason, evidence, at) VALUES (?, ?, ?, ?)`, origin, reason, evidence, now())
	return err
}

func (d *DB) Flagged(origin string) (string, bool, error) {
	var reason string
	err := d.node.QueryRow(`SELECT reason FROM flagged_origins WHERE origin = ?`, origin).Scan(&reason)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return reason, err == nil, err
}

func (d *DB) OriginKey(origin string) ([]byte, bool, error) {
	var pub []byte
	err := d.node.QueryRow(`SELECT pubkey FROM origins WHERE origin = ?`, origin).Scan(&pub)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return pub, err == nil, err
}

// RememberOrigin records the key of a node. The key names its origin (the caller has checked that),
// so a node cannot be given another's.
func (d *DB) RememberOrigin(origin string, pub []byte) error { return d.RegisterOrigin(origin, pub) }

// ---- the journal of a chat

func (d *DB) openedChat(chatID string) (*chatDB, func(), error) {
	c, done, err := d.acquire(chatID, "", false)
	if err != nil {
		return nil, done, err
	}
	if c == nil {
		return nil, done, fmt.Errorf("no chat %.8s", chatID)
	}
	return c, done, nil
}

func (d *DB) Vector(chatID string) (map[string]int64, error) {
	c, done, err := d.openedChat(chatID)
	defer done()
	if err != nil {
		return nil, err
	}
	rows, err := c.db.Query(`SELECT origin, MAX(seq) FROM events GROUP BY origin`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var o string
		var n int64
		if err := rows.Scan(&o, &n); err != nil {
			return nil, err
		}
		out[o] = n
	}
	return out, rows.Err()
}

func (d *DB) EventsAfter(chatID, origin string, after int64, limit int) ([]events.Event, error) {
	c, done, err := d.openedChat(chatID)
	defer done()
	if err != nil {
		return nil, err
	}
	rows, err := c.db.Query(`SELECT `+eventCols+` FROM events WHERE origin = ? AND seq > ? ORDER BY seq LIMIT ?`, origin, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []events.Event
	for rows.Next() {
		e, err := scanEvent(chatID, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// OnEvents: see store.Federation.
func (d *DB) OnEvents(fn func(chatID string)) {
	if fn == nil {
		d.onEvents.Store(nil)
		return
	}
	d.onEvents.Store(&fn)
}

func (d *DB) notify(chatID string) {
	if fn := d.onEvents.Load(); fn != nil {
		(*fn)(chatID)
	}
}

func (d *DB) MaxLamport(chatID string) (int64, error) {
	c, done, err := d.openedChat(chatID)
	defer done()
	if err != nil {
		return 0, err
	}
	var n int64
	err = c.db.QueryRow(`SELECT COALESCE(MAX(lamport), 0) FROM events`).Scan(&n)
	return n, err
}

// AcceptEvent stores an event of another node as the next of its origin and applies it to the
// projections in the same transaction (remote.go); it returns what the broker has to learn of.
// An event that the rules of application refuse is not stored.
func (d *DB) AcceptEvent(chatID string, e events.Event) ([]store.Record, error) {
	c, done, err := d.openedChat(chatID)
	defer done()
	if err != nil {
		return nil, err
	}
	key := d.nodeKey()
	if key == nil {
		return nil, errors.New("this node has no identity: it cannot take events of others")
	}
	e.ChatID = chatID
	var effects []store.Record
	err = c.inTx(func(tx *sql.Tx) error {
		seq, tailID, err := c.chainTail(tx, e.Origin)
		if err != nil {
			return err
		}
		switch {
		case e.Seq <= seq:
			stored, err := scanEvent(chatID, tx.QueryRow(`SELECT `+eventCols+` FROM events WHERE origin = ? AND seq = ?`, e.Origin, e.Seq))
			if err != nil {
				return err
			}
			if stored.ID() == e.ID() {
				return store.ErrDuplicate
			}
			return &store.ForkError{Stored: stored}
		case e.Seq > seq+1:
			return store.ErrGap
		case e.Prev != tailID:
			return store.ErrChain
		}
		if err := putEvent(tx, e); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE events SET remote = 1 WHERE origin = ? AND seq = ?`, e.Origin, e.Seq); err != nil {
			return err
		}
		// This node's clock never falls behind the events it holds.
		if _, err := tx.Exec(`UPDATE chat SET lamport = MAX(lamport, ?)`, e.Lamport); err != nil {
			return err
		}
		effects, err = applyRemote(tx, e, key.origin)
		return err
	})
	if err != nil {
		return nil, err
	}
	return effects, nil
}
