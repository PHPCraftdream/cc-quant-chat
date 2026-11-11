package sqlite

import (
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

var _ store.Sources = (*DB)(nil)

// NoteSource: see store.Sources. One transaction: the count and the insert do not race.
func (d *DB) NoteSource(origin, via string, limit int) (allowed, fresh bool, err error) {
	tx, err := d.node.Begin()
	if err != nil {
		return false, false, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM origin_sources WHERE origin = ? AND via = ?`, origin, via).Scan(&n); err != nil {
		return false, false, err
	}
	if n > 0 {
		return true, false, nil
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM origin_sources WHERE via = ?`, via).Scan(&n); err != nil {
		return false, false, err
	}
	if n >= limit {
		return false, false, nil
	}
	if _, err := tx.Exec(`INSERT INTO origin_sources (origin, via, first_seen) VALUES (?, ?, ?)`, origin, via, now()); err != nil {
		return false, false, err
	}
	return true, true, tx.Commit()
}

func (d *DB) SourcesVia(via string) ([]string, error) {
	return d.column(`SELECT origin FROM origin_sources WHERE via = ? ORDER BY origin`, via)
}
