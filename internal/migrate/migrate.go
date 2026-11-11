// Package migrate: forward-only, versioned SQL migrations.
//
// Migrations are files "NNNN_name.sql" (NNNN = version, >= 1). Each pending migration runs
// in its own transaction together with its row in schema_migrations, so a failed migration
// leaves no trace. Applied migrations are pinned by checksum: editing one after release is
// an error, as is a database migrated by a newer binary. There are no down migrations;
// to roll back, restore a backup of the database file.
//
// A migration that must rewrite tables other tables point to (the SQLite "12-step" ALTER)
// starts with the line "-- ccqc:foreign-keys=off". It then runs with foreign key enforcement
// off on one pinned connection, and "PRAGMA foreign_key_check" must come back empty before commit.
//
// Checksums are taken over the file with CRLF turned into LF, so a checkout with other
// line endings still matches; the raw and CRLF forms of a file are accepted for databases that
// recorded them before that.
package migrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Migration struct {
	Version int
	Name    string
	SQL     string
	Sum     string   // hex SHA-256 of SQL
	Alt     []string // other sums accepted for the same file (raw bytes, CRLF form)
	NoFK    bool     // "-- ccqc:foreign-keys=off"
}

// Applied: a row of schema_migrations.
type Applied struct {
	Version   int
	Name      string
	Sum       string
	AppliedAt string
}

var fileRe = regexp.MustCompile(`^(\d{4,})_([a-z0-9_]+)\.sql$`)

// Load reads migrations from dir in fsys, sorted by version. Other files are an error,
// so a misnamed migration can't be silently skipped.
func Load(fsys fs.FS, dir string) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var ms []Migration
	seen := map[int]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := fileRe.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("migrate: %s: want NNNN_name.sql", e.Name())
		}
		v, _ := strconv.Atoi(m[1])
		if v < 1 {
			return nil, fmt.Errorf("migrate: %s: version must be >= 1", e.Name())
		}
		if prev, dup := seen[v]; dup {
			return nil, fmt.Errorf("migrate: version %d used by both %s and %s", v, prev, e.Name())
		}
		seen[v] = e.Name()
		body, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		ms = append(ms, newMigration(v, m[2], body))
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].Version < ms[j].Version })
	return ms, nil
}

const (
	crlf = "\r\n"
	lfs  = "\n"
)

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func newMigration(version int, name string, raw []byte) Migration {
	lf := strings.ReplaceAll(string(raw), crlf, lfs)
	m := Migration{Version: version, Name: name, SQL: lf, Sum: sum([]byte(lf))}
	for _, alt := range []string{string(raw), strings.ReplaceAll(lf, lfs, crlf)} {
		if a := sum([]byte(alt)); a != m.Sum && !slices.Contains(m.Alt, a) {
			m.Alt = append(m.Alt, a)
		}
	}
	for _, line := range strings.Split(lf, lfs) {
		line = strings.TrimSpace(line)
		if line == "-- ccqc:foreign-keys=off" {
			m.NoFK = true
		}
		if line != "" && !strings.HasPrefix(line, "--") {
			break // directives live in the leading comment block
		}
	}
	return m
}

func (m Migration) matches(recorded string) bool {
	return recorded == m.Sum || slices.Contains(m.Alt, recorded)
}

const createTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
	version    INTEGER PRIMARY KEY,
	name       TEXT NOT NULL,
	checksum   TEXT NOT NULL,
	applied_at TEXT NOT NULL
)`

// Status returns applied migrations, oldest first. It only reads: a database without
// the table yet (fresh file) has none.
func Status(ctx context.Context, db *sql.DB) ([]Applied, error) {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT version, name, checksum, applied_at FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Applied
	for rows.Next() {
		var a Applied
		if err := rows.Scan(&a.Version, &a.Name, &a.Sum, &a.AppliedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Check verifies applied migrations against the known ones and returns the pending ones.
func Check(applied []Applied, ms []Migration) ([]Migration, error) {
	known := map[int]Migration{}
	for _, m := range ms {
		known[m.Version] = m
	}
	done := map[int]bool{}
	for _, a := range applied {
		m, ok := known[a.Version]
		if !ok {
			return nil, fmt.Errorf("migrate: database has migration %d (%s) unknown to this binary; it was migrated by a newer version", a.Version, a.Name)
		}
		if !m.matches(a.Sum) {
			return nil, fmt.Errorf("migrate: migration %d (%s) changed after it was applied; add a new migration instead of editing it", a.Version, a.Name)
		}
		done[a.Version] = true
	}
	var pending []Migration
	for _, m := range ms {
		if !done[m.Version] {
			pending = append(pending, m)
		}
	}
	return pending, nil
}

// Up applies all pending migrations in order and returns them.
// A migration with a version lower than an applied one is still applied (late merge),
// since versions only order the files; checksums keep history honest.
func Up(ctx context.Context, db *sql.DB, ms []Migration) ([]Migration, error) {
	if _, err := db.ExecContext(ctx, createTable); err != nil {
		return nil, err
	}
	applied, err := Status(ctx, db)
	if err != nil {
		return nil, err
	}
	pending, err := Check(applied, ms)
	if err != nil {
		return nil, err
	}
	for i, m := range pending {
		if err := apply(ctx, db, m); err != nil {
			return pending[:i], fmt.Errorf("migrate: %04d_%s: %w", m.Version, m.Name, err)
		}
	}
	return pending, nil
}

func apply(ctx context.Context, db *sql.DB, m Migration) (err error) {
	// One connection for the whole migration: PRAGMA foreign_keys is per connection.
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if m.NoFK {
		if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
			return err
		}
		defer func() {
			if _, e := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`); e != nil && err == nil {
				err = e
			}
		}()
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	// Another process may have applied it meanwhile: re-check inside the transaction.
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version = ?`, m.Version).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
		return err
	}
	if m.NoFK {
		rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
		if err != nil {
			return err
		}
		broken := rows.Next()
		rowsErr := rows.Err()
		rows.Close()
		if rowsErr != nil {
			return rowsErr
		}
		if broken {
			return errors.New("foreign_key_check failed after the migration")
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
		m.Version, m.Name, m.Sum, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	return tx.Commit()
}
