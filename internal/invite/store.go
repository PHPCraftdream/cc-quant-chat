package invite

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"errors"
	"time"
)

// States of an invitation.
const (
	StateOpen    = "open"
	StateUsed    = "used"
	StateBurnt   = "burnt" // MaxTries bad proofs
	StateRevoked = "revoked"
)

// Record is what the broker keeps of an invitation: never the secret.
type Record struct {
	ID        string
	Pub       ed25519.PublicKey
	Kind      string
	Scope     string // chat invitation: the nick it is for
	ExpiresAt time.Time
	Tries     int
	State     string
	IssuedBy  string // the key of the owner who issued it (base64)
	IssuedAt  time.Time
	UsedAt    time.Time
	UsedBy    string // the key that the use bound
}

// NewRecord is the record of a fresh invitation.
func NewRecord(in Invitation, scope, issuedBy string, now time.Time) Record {
	return Record{ID: in.ID(), Pub: in.Public(), Kind: in.Kind, Scope: scope, ExpiresAt: time.Unix(in.Exp, 0),
		State: StateOpen, IssuedBy: issuedBy, IssuedAt: now}
}

// ErrInvalid is the one answer for every way an invitation can fail to work: unknown, expired, used,
// burnt, revoked, for another nick. Nothing tells which, so nothing can be probed.
var ErrInvalid = errors.New("invite_invalid")

// BadProof: the invitation exists and is open but the proof is wrong. It is an ErrInvalid too; it
// carries what the chat is told (the invitation is named by its record, and it may have burnt).
type BadProof struct {
	Record Record
	Tries  int
	Burnt  bool
}

func (*BadProof) Error() string        { return ErrInvalid.Error() }
func (*BadProof) Is(target error) bool { return target == ErrInvalid }

// Store keeps the records. Burn and Fail are atomic: with many callers at once, one Burn wins.
type Store interface {
	Insert(r Record) error
	Get(id string) (Record, bool, error)
	// Burn marks an open, unexpired invitation with tries left as used by usedBy; false if it was not.
	Burn(id string, now time.Time, usedBy string) (bool, error)
	// Fail counts a bad proof; the MaxTries-th burns the invitation.
	Fail(id string) (tries int, burnt bool, err error)
	Revoke(id string) (bool, error)
	// List: the invitations of a kind, newest first.
	List(kind string) ([]Record, error)
}

// Redeem uses an invitation. proofOK says whether the proof the caller holds is right for the
// record (it checks the signature, the nick, whatever the invitation is for). A right proof burns
// the invitation in one atomic step; a wrong one counts against it. Every failure is ErrInvalid; a
// wrong proof on an open invitation is the more specific *BadProof.
func Redeem(st Store, id string, now time.Time, usedBy string, proofOK func(Record) bool) (Record, error) {
	rec, ok, err := st.Get(id)
	if err != nil {
		return Record{}, err
	}
	if !ok || rec.State != StateOpen || !now.Before(rec.ExpiresAt) || rec.Tries >= MaxTries {
		return Record{}, ErrInvalid
	}
	if !proofOK(rec) {
		tries, burnt, err := st.Fail(id)
		if err != nil {
			return Record{}, err
		}
		return rec, &BadProof{Record: rec, Tries: tries, Burnt: burnt}
	}
	won, err := st.Burn(id, now, usedBy)
	if err != nil {
		return Record{}, err
	}
	if !won {
		return Record{}, ErrInvalid // another client used it first
	}
	rec.State, rec.UsedAt, rec.UsedBy = StateUsed, now, usedBy
	return rec, nil
}

// Schema is the table of an SQLite store (the migrations of the database that holds it say the same).
const Schema = `CREATE TABLE invites (
    id         TEXT    PRIMARY KEY,
    pub        TEXT    NOT NULL,
    kind       TEXT    NOT NULL,
    scope      TEXT    NOT NULL DEFAULT '',
    expires_at INTEGER NOT NULL,
    tries      INTEGER NOT NULL DEFAULT 0,
    state      TEXT    NOT NULL DEFAULT 'open',
    issued_by  TEXT    NOT NULL DEFAULT '',
    issued_at  INTEGER NOT NULL,
    used_at    INTEGER NOT NULL DEFAULT 0,
    used_by    TEXT    NOT NULL DEFAULT ''
) WITHOUT ROWID`

// DBTX is what an SQLite store runs on: a database or a transaction.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// SQLStore is a Store on the table `invites`.
type SQLStore struct{ DB DBTX }

const cols = `id, pub, kind, scope, expires_at, tries, state, issued_by, issued_at, used_at, used_by`

func scan(row interface{ Scan(...any) error }) (Record, error) {
	var r Record
	var pub string
	var exp, issued, used int64
	if err := row.Scan(&r.ID, &pub, &r.Kind, &r.Scope, &exp, &r.Tries, &r.State, &r.IssuedBy, &issued, &used, &r.UsedBy); err != nil {
		return r, err
	}
	raw, err := base64.StdEncoding.DecodeString(pub)
	if err != nil {
		return r, err
	}
	r.Pub = raw
	r.ExpiresAt, r.IssuedAt = time.Unix(exp, 0), time.Unix(issued, 0)
	if used != 0 {
		r.UsedAt = time.Unix(used, 0)
	}
	return r, nil
}

func (s SQLStore) Insert(r Record) error {
	_, err := s.DB.ExecContext(context.Background(), `INSERT INTO invites (`+cols+`) VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?, 0, '')`,
		r.ID, base64.StdEncoding.EncodeToString(r.Pub), r.Kind, r.Scope, r.ExpiresAt.Unix(), StateOpen, r.IssuedBy, r.IssuedAt.Unix())
	return err
}

func (s SQLStore) Get(id string) (Record, bool, error) {
	r, err := scan(s.DB.QueryRowContext(context.Background(), `SELECT `+cols+` FROM invites WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	return r, err == nil, err
}

func (s SQLStore) Burn(id string, now time.Time, usedBy string) (bool, error) {
	res, err := s.DB.ExecContext(context.Background(), `UPDATE invites SET state = 'used', used_at = ?, used_by = ?
		WHERE id = ? AND state = 'open' AND expires_at > ? AND tries < ?`, now.Unix(), usedBy, id, now.Unix(), MaxTries)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s SQLStore) Fail(id string) (int, bool, error) {
	if _, err := s.DB.ExecContext(context.Background(), `UPDATE invites SET tries = tries + 1,
		state = CASE WHEN tries + 1 >= ? THEN 'burnt' ELSE state END WHERE id = ? AND state = 'open'`, MaxTries, id); err != nil {
		return 0, false, err
	}
	r, ok, err := s.Get(id)
	if err != nil || !ok {
		return 0, false, err
	}
	return r.Tries, r.State == StateBurnt, nil
}

func (s SQLStore) Revoke(id string) (bool, error) {
	res, err := s.DB.ExecContext(context.Background(), `UPDATE invites SET state = 'revoked' WHERE id = ? AND state = 'open'`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s SQLStore) List(kind string) ([]Record, error) {
	rows, err := s.DB.QueryContext(context.Background(), `SELECT `+cols+` FROM invites WHERE kind = ? ORDER BY issued_at DESC, id`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
