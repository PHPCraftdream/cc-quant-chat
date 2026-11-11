package sqlite

import (
	"database/sql"
	"errors"

	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

var _ store.Limits = (*DB)(nil)

func (d *DB) Quarantine(q store.QuarantineInfo) (bool, error) {
	if q.Since == "" {
		q.Since = now()
	}
	res, err := d.node.Exec(`INSERT OR IGNORE INTO quarantine (origin, reason, since, by, via) VALUES (?, ?, ?, ?, ?)`,
		q.Origin, q.Reason, q.Since, q.By, q.Via)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (d *DB) ReleaseSource(origin string) (bool, error) {
	tx, err := d.node.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	was := false
	for _, q := range []string{`DELETE FROM quarantine WHERE origin = ?`, `DELETE FROM flagged_origins WHERE origin = ?`} {
		res, err := tx.Exec(q, origin)
		if err != nil {
			return false, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			was = true
		}
	}
	if _, err := tx.Exec(`UPDATE peer_stats SET violations = 0 WHERE origin = ?`, origin); err != nil {
		return false, err
	}
	return was, tx.Commit()
}

func (d *DB) Quarantined() ([]store.QuarantineInfo, error) {
	rows, err := d.node.Query(`SELECT origin, reason, since, by, via FROM quarantine
		UNION ALL SELECT origin, 'fork: ' || reason, at, 'auto', '' FROM flagged_origins
			WHERE origin NOT IN (SELECT origin FROM quarantine)
		ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.QuarantineInfo
	for rows.Next() {
		var q store.QuarantineInfo
		if err := rows.Scan(&q.Origin, &q.Reason, &q.Since, &q.By, &q.Via); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func (d *DB) DayStats(origin, day string) (store.DayStats, error) {
	var s store.DayStats
	err := d.node.QueryRow(`SELECT events, bytes, violations FROM peer_stats WHERE origin = ? AND day = ?`, origin, day).Scan(&s.Events, &s.Bytes, &s.Violations)
	if errors.Is(err, sql.ErrNoRows) {
		return store.DayStats{}, nil
	}
	return s, err
}

func (d *DB) AddDayStats(origin, day string, s store.DayStats) error {
	_, err := d.node.Exec(`INSERT INTO peer_stats (origin, day, events, bytes, violations) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (origin, day) DO UPDATE SET events = events + excluded.events, bytes = bytes + excluded.bytes,
			violations = violations + excluded.violations`, origin, day, s.Events, s.Bytes, s.Violations)
	return err
}
