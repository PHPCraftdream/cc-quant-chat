package sqlite

import (
	"bytes"
	"database/sql"
	"errors"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

var _ store.OwnerStore = (*DB)(nil)

// Owner returns the registered owner key.
func (d *DB) Owner() ([]byte, string, bool, error) {
	var pub []byte
	var since string
	err := d.db.QueryRow(`SELECT pubkey, created_at FROM owner WHERE id = 1`).Scan(&pub, &since)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", false, nil
	}
	return pub, since, err == nil, err
}

// SetOwner registers the first owner key.
func (d *DB) SetOwner(pub []byte) error {
	res, err := d.db.Exec(`INSERT OR IGNORE INTO owner (id, pubkey, created_at) VALUES (1, ?, ?)`,
		pub, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrOwnerExists
	}
	return nil
}

// ReplaceOwner swaps the key only if the stored one is old.
func (d *DB) ReplaceOwner(old, pub []byte) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var cur []byte
	if err := tx.QueryRow(`SELECT pubkey FROM owner WHERE id = 1`).Scan(&cur); err != nil || !bytes.Equal(cur, old) {
		return store.ErrOwnerChanged
	}
	if _, err := tx.Exec(`UPDATE owner SET pubkey = ?, created_at = ? WHERE id = 1`, pub, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	return tx.Commit()
}
