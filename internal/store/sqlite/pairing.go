package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/invite"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

var _ store.Pairing = (*DB)(nil)

// maxWrongCodes: wrong codes entered for a pending node after which it is dropped.
const maxWrongCodes = 3

func (d *DB) invites() invite.SQLStore { return invite.SQLStore{DB: d.node} }

func (d *DB) IssueNodeInvite(rec invite.Record) error { return d.invites().Insert(rec) }

func (d *DB) RevokeNodeInvite(id string) (bool, error) { return d.invites().Revoke(id) }

func (d *DB) ListNodeInvites() ([]invite.Record, error) { return d.invites().List(invite.KindNode) }

// AcceptNodeInvite: see store.Pairing. A wrong proof is counted and kept (the transaction commits);
// a good one burns the invitation and lists the node together.
func (d *DB) AcceptNodeInvite(id string, now time.Time, p store.Pending, proofOK func(invite.Record) bool) (invite.Record, error) {
	tx, err := d.node.Begin()
	if err != nil {
		return invite.Record{}, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	rec, err := invite.Redeem(invite.SQLStore{DB: tx}, id, now, p.Origin, proofOK)
	if err != nil {
		var bad *invite.BadProof
		if errors.As(err, &bad) {
			if cerr := tx.Commit(); cerr != nil {
				return invite.Record{}, cerr
			}
		}
		return rec, err
	}
	if rec.Kind != invite.KindNode {
		return invite.Record{}, invite.ErrInvalid
	}
	scope, err := store.ParseNodeScope(rec.Scope)
	if err != nil || !store.ValidTrust(scope.Trust) {
		return invite.Record{}, invite.ErrInvalid
	}
	p.Trust, p.Chats, p.Until = scope.Trust, scope.Chats, now.Add(store.PendingTTL)
	if err := insertPending(tx, p); err != nil {
		return invite.Record{}, err
	}
	for _, c := range scope.Chats { // offered for exchange; nothing moves while the node is pending
		if _, err := tx.Exec(`INSERT OR IGNORE INTO peer_chats (origin, chat_id) VALUES (?, ?)`, p.Origin, c.ID); err != nil {
			return invite.Record{}, err
		}
	}
	return rec, tx.Commit()
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func insertPending(tx execer, p store.Pending) error {
	if _, err := tx.Exec(`INSERT INTO peers (origin, addr, trust, state, pending_until, pubkey, added_at, pending_code)
		VALUES (?, ?, ?, 'pending', ?, ?, ?, ?)
		ON CONFLICT (origin) DO UPDATE SET addr = excluded.addr, trust = excluded.trust, state = 'pending',
			pending_until = excluded.pending_until, pending_code = excluded.pending_code, pending_tries = 0,
			confirmed_local = 0, confirmed_remote = 0`,
		p.Origin, p.Addr, p.Trust, p.Until.UTC().Format(time.RFC3339), p.Pub, now(), p.Code); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM pending_chats WHERE origin = ?`, p.Origin); err != nil {
		return err
	}
	return nil
}

// AddPendingPeer: see store.Pairing. The chats it offers wait in pending_chats.
func (d *DB) AddPendingPeer(p store.Pending) error {
	tx, err := d.node.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if err := insertPending(tx, p); err != nil {
		return err
	}
	for _, c := range p.Chats {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO pending_chats (origin, chat_id, name) VALUES (?, ?, ?)`, p.Origin, c.ID, c.Name); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) PendingCode(origin string) (string, bool, error) {
	var code string
	err := d.node.QueryRow(`SELECT pending_code FROM peers WHERE origin = ? AND state = 'pending'`, origin).Scan(&code)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return code, err == nil, err
}

func (d *DB) PendingChats(origin string) ([]store.ChatRef, error) {
	rows, err := d.node.Query(`SELECT chat_id, name FROM pending_chats WHERE origin = ? ORDER BY chat_id`, origin)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.ChatRef
	for rows.Next() {
		var c store.ChatRef
		if err := rows.Scan(&c.ID, &c.Name); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// dropPending forgets a pending node and everything held for it.
func dropPending(tx execer, origin string) error {
	for _, q := range []string{`DELETE FROM peer_chats WHERE origin = ?`, `DELETE FROM pending_chats WHERE origin = ?`,
		`DELETE FROM peers WHERE origin = ? AND state = 'pending'`} {
		if _, err := tx.Exec(q, origin); err != nil {
			return err
		}
	}
	return nil
}

// Confirm: see store.Pairing. The comparison is constant in what it tells: a wrong code only counts.
func (d *DB) Confirm(origin, code string) (store.ConfirmResult, error) {
	tx, err := d.node.Begin()
	if err != nil {
		return store.ConfirmResult{}, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var want string
	var tries, remote int
	err = tx.QueryRow(`SELECT pending_code, pending_tries, confirmed_remote FROM peers WHERE origin = ? AND state = 'pending'`, origin).Scan(&want, &tries, &remote)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ConfirmResult{}, fmt.Errorf("no pending node %s", origin)
	} else if err != nil {
		return store.ConfirmResult{}, err
	}
	if code != want || want == "" {
		tries++
		res := store.ConfirmResult{Tries: tries}
		if tries >= maxWrongCodes {
			res.Gone = true
			err = dropPending(tx, origin)
		} else {
			_, err = tx.Exec(`UPDATE peers SET pending_tries = ? WHERE origin = ?`, tries, origin)
		}
		if err != nil {
			return store.ConfirmResult{}, err
		}
		return res, tx.Commit()
	}
	res := store.ConfirmResult{Right: true, Tries: tries}
	if _, err := tx.Exec(`UPDATE peers SET confirmed_local = 1 WHERE origin = ?`, origin); err != nil {
		return res, err
	}
	if remote == 1 {
		res.Active = true
		if err := activate(tx, origin); err != nil {
			return res, err
		}
	}
	return res, tx.Commit()
}

func activate(tx execer, origin string) error {
	_, err := tx.Exec(`UPDATE peers SET state = 'active', pending_until = '', pending_code = '', pending_tries = 0 WHERE origin = ?`, origin)
	return err
}

func (d *DB) ConfirmedRemote(origin string) (bool, error) {
	tx, err := d.node.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var state string
	var local int
	err = tx.QueryRow(`SELECT state, confirmed_local FROM peers WHERE origin = ?`, origin).Scan(&state, &local)
	if errors.Is(err, sql.ErrNoRows) || state != store.StatePending {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if _, err := tx.Exec(`UPDATE peers SET confirmed_remote = 1 WHERE origin = ?`, origin); err != nil {
		return false, err
	}
	if local == 1 {
		if err := activate(tx, origin); err != nil {
			return false, err
		}
	}
	return local == 1, tx.Commit()
}

func (d *DB) ConfirmedHere(origin string) (bool, error) {
	var local int
	err := d.node.QueryRow(`SELECT confirmed_local FROM peers WHERE origin = ?`, origin).Scan(&local)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return local == 1, err
}

// DropExpired: see store.Pairing.
func (d *DB) DropExpired(at time.Time) ([]store.Peer, error) {
	peers, err := d.Peers()
	if err != nil {
		return nil, err
	}
	var gone []store.Peer
	for _, p := range peers {
		until, err := time.Parse(time.RFC3339, p.PendingUntil)
		if p.State != store.StatePending || err != nil || at.Before(until) {
			continue
		}
		tx, err := d.node.Begin()
		if err != nil {
			return gone, err
		}
		err = dropPending(tx, p.Origin)
		if err == nil {
			err = tx.Commit()
		} else {
			tx.Rollback() //nolint:errcheck // already failing
		}
		if err != nil {
			return gone, err
		}
		gone = append(gone, p)
	}
	return gone, nil
}
