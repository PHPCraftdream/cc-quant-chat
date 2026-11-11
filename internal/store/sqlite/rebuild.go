package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// projection is a table the events can rebuild. skip names a column that is the node's own state,
// not derived from events (the read cursor).
type projection struct {
	table string
	skip  []string
}

// In insert order (parents first); a swap deletes in the reverse order.
var projections = []projection{
	{"rooms", nil}, {"room_members", nil}, {"nicks", []string{"seen", "key_hash"}}, {"messages", nil}, {"board_sprints", nil},
	{"board_tasks", nil}, {"board_workflow", nil}, {"board_log", nil}, {"board_comments", nil},
	{"docs", nil}, {"doc_versions", nil}, {"settings", nil}, {"remote_nicks", nil}, {"entity_refs", nil}, {"board_stamps", nil}, {"doc_vrefs", nil}, {"doc_heads", nil},
}

// RebuildReport: what a rebuild found. Diffs counts, per table, the rows that are in only one of
// the current and the rebuilt projection ("chat.lamport" is 1 when the clock differs).
type RebuildReport struct {
	Events   int
	Diffs    map[string]int
	Replaced bool
}

// Total is the number of differing rows over all tables.
func (r RebuildReport) Total() int {
	n := 0
	for _, v := range r.Diffs {
		n += v
	}
	return n
}

const metaBackfill = "events_backfill"

// complete reports whether every row of the chat that predates its journal is in the journal.
func (c *chatDB) complete() (bool, error) {
	var v string
	err := c.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, metaBackfill).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return v == "done", err
}

func (c *chatDB) columns(schema, table string, skip []string) ([]string, error) {
	rows, err := c.db.Query(`SELECT name FROM pragma_table_info(?, ?)`, table, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		if !slices.Contains(skip, n) {
			cols = append(cols, n)
		}
	}
	return cols, rows.Err()
}

// Rebuild builds the projections of a chat again from its events, compares them with the current
// ones and, unless dryRun, replaces the current ones that differ. The node's own state (the read
// cursors) is kept.
func (d *DB) Rebuild(chatID string, dryRun bool) (RebuildReport, error) {
	rep := RebuildReport{Diffs: map[string]int{}}
	c, err := d.chat(chatID, "", false)
	if err != nil {
		return rep, err
	}
	if c == nil {
		return rep, fmt.Errorf("no chat %.8s", chatID)
	}
	if done, err := c.complete(); err != nil || !done {
		return rep, fmt.Errorf("the rows that predate the journal have not been moved into it yet: start the broker once, then rebuild (%v)", err)
	}
	dir, err := os.MkdirTemp("", "ccqc-rebuild-")
	if err != nil {
		return rep, err
	}
	defer os.RemoveAll(dir)
	shadow := filepath.Join(dir, "shadow.db")
	if rep.Events, err = c.replayInto(shadow); err != nil {
		return rep, err
	}
	if _, err := c.db.Exec(`ATTACH DATABASE ? AS shadow`, shadow); err != nil {
		return rep, err
	}
	defer c.db.Exec(`DETACH DATABASE shadow`) //nolint:errcheck // best effort; the connection goes with the chat

	for _, p := range projections {
		cols, err := c.columns("main", p.table, p.skip)
		if err != nil {
			return rep, err
		}
		list := strings.Join(cols, ", ")
		for _, dir := range [][2]string{{"main", "shadow"}, {"shadow", "main"}} {
			var n int
			q := `SELECT COUNT(*) FROM (SELECT ` + list + ` FROM ` + dir[0] + `.` + p.table +
				` EXCEPT SELECT ` + list + ` FROM ` + dir[1] + `.` + p.table + `)`
			if err := c.db.QueryRow(q).Scan(&n); err != nil {
				return rep, fmt.Errorf("compare %s: %w", p.table, err)
			}
			if n > 0 {
				rep.Diffs[p.table] += n
			}
		}
	}
	var clocks int
	if err := c.db.QueryRow(`SELECT COUNT(*) FROM main.chat m JOIN shadow.chat s ON s.id = m.id WHERE m.lamport != s.lamport`).Scan(&clocks); err != nil {
		return rep, err
	}
	if clocks > 0 {
		rep.Diffs["chat.lamport"] = clocks
	}
	if rep.Total() == 0 || dryRun {
		return rep, nil
	}
	if err := c.swapProjections(); err != nil {
		return rep, err
	}
	rep.Replaced = true
	return rep, nil
}

// replayInto writes a chat database at path with the projections the events give.
func (c *chatDB) replayInto(path string) (int, error) {
	sh, err := openMigrated(path, ChatMigrations)
	if err != nil {
		return 0, err
	}
	defer sh.Close()
	var name, created string
	if err := c.db.QueryRow(`SELECT name, created_at FROM chat WHERE id = 1`).Scan(&name, &created); err != nil {
		return 0, err
	}
	tx, err := sh.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.Exec(`INSERT INTO chat (id, chat_id, name, created_at) VALUES (1, ?, ?, ?)`, c.id, name, created); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`INSERT INTO rooms (room) VALUES ('common')`); err != nil {
		return 0, err
	}
	rows, err := c.db.Query(`SELECT ` + eventCols + `, remote FROM events ORDER BY ord`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	self := ""
	if key := c.s.nodeKey(); key != nil {
		self = key.origin
	}
	n := 0
	for rows.Next() {
		var remote bool
		e, err := scanEvent(c.id, withExtra{rows, &remote})
		if err != nil {
			return n, err
		}
		if e.Redacted() { // kept for the chain only
			n++
			continue
		}
		if remote {
			_, err = applyRemote(tx, e, self)
		} else {
			err = applyEvent(tx, e)
		}
		if err != nil {
			return n, fmt.Errorf("event %s#%d (%s): %w", e.Origin, e.Seq, e.Kind, err)
		}
		if _, err := tx.Exec(`UPDATE chat SET lamport = MAX(lamport, ?)`, e.Lamport); err != nil {
			return n, err
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return n, err
	}
	return n, tx.Commit()
}

// withExtra reads one more column after those of an event row.
type withExtra struct {
	row   interface{ Scan(...any) error }
	extra any
}

func (w withExtra) Scan(dest ...any) error { return w.row.Scan(append(dest, w.extra)...) }

// swapProjections replaces the current projections with the rebuilt ones (attached as "shadow"),
// keeping the nicks' read cursors, in one transaction.
func (c *chatDB) swapProjections() error {
	lists := map[string]string{} // read before the transaction: the database has one connection
	for _, p := range projections {
		cols, err := c.columns("main", p.table, nil)
		if err != nil {
			return err
		}
		lists[p.table] = strings.Join(cols, ", ")
	}
	return c.inTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`CREATE TEMP TABLE keep_seen AS SELECT nick, seen, key_hash FROM main.nicks`); err != nil {
			return err
		}
		for i := len(projections) - 1; i >= 0; i-- {
			if _, err := tx.Exec(`DELETE FROM main.` + projections[i].table); err != nil {
				return fmt.Errorf("clear %s: %w", projections[i].table, err)
			}
		}
		for _, p := range projections {
			if _, err := tx.Exec(`INSERT INTO main.` + p.table + ` (` + lists[p.table] + `) SELECT ` + lists[p.table] + ` FROM shadow.` + p.table); err != nil {
				return fmt.Errorf("write %s: %w", p.table, err)
			}
		}
		if _, err := tx.Exec(`UPDATE main.nicks SET seen = COALESCE((SELECT k.seen FROM keep_seen k WHERE k.nick = main.nicks.nick), seen),
			key_hash = COALESCE((SELECT k.key_hash FROM keep_seen k WHERE k.nick = main.nicks.nick), key_hash)`); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE main.chat SET lamport = (SELECT s.lamport FROM shadow.chat s WHERE s.id = main.chat.id)`); err != nil {
			return err
		}
		_, err := tx.Exec(`DROP TABLE keep_seen`)
		return err
	})
}
