package sqlite

import (
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

var _ store.AdminStore = (*DB)(nil)

// Settings returns every stored setting as scope → key → value.
func (d *DB) Settings() (map[string]map[string]string, error) {
	rows, err := d.db.Query(`SELECT scope, key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]string{}
	for rows.Next() {
		var scope, key, value string
		if err := rows.Scan(&scope, &key, &value); err != nil {
			return nil, err
		}
		if out[scope] == nil {
			out[scope] = map[string]string{}
		}
		out[scope][key] = value
	}
	return out, rows.Err()
}

// SetSetting stores a value, replacing an earlier one.
func (d *DB) SetSetting(scope, key, value string) error {
	_, err := d.db.Exec(`INSERT OR REPLACE INTO settings (scope, key, value) VALUES (?, ?, ?)`, scope, key, value)
	return err
}

// LogAdmin appends an administrator attempt to the log.
func (d *DB) LogAdmin(e store.AdminEntry) error {
	_, err := d.db.Exec(`INSERT INTO admin_log (ts, op, args, result, origin) VALUES (?, ?, ?, ?, ?)`, e.TS, e.Op, e.Args, e.Result, e.Origin)
	return err
}
