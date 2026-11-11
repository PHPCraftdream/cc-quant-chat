package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PHPCraftdream/cc-quant-chat/internal/events"
)

// projection is a table the events can rebuild. chatCol names the column that holds the chat id;
// skip lists columns that are the node's own state, not derived from events (the read cursor).
type projection struct {
	table   string
	chatCol string
	skip    string
}

// In insert order (parents first); a swap deletes in the reverse order.
var projections = []projection{
	{"rooms", "chat_id", ""}, {"room_members", "chat_id", ""}, {"nicks", "chat_id", "seen"},
	{"messages", "chat_id", ""}, {"board_sprints", "chat_id", ""}, {"board_tasks", "chat_id", ""},
	{"board_workflow", "chat_id", ""}, {"board_log", "chat_id", ""}, {"board_comments", "chat_id", ""},
	{"docs", "chat_id", ""}, {"doc_versions", "chat_id", ""}, {"settings", "scope", ""},
}

// RebuildReport: what a rebuild found. Diffs counts, per table, the rows that are in only one of
// the current and the rebuilt projection ("chats.lamport" counts chats whose clock differs).
type RebuildReport struct {
	Chats    int
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

// BackfillDone reports whether every row that predates the journal has been moved into it.
func (d *DB) BackfillDone() (bool, error) {
	var v string
	err := d.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, metaBackfill).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return v == "done", err
}

func (d *DB) chatIDs(only string) ([][3]string, error) {
	q := `SELECT id, name, created_at FROM chats`
	var args []any
	if only != "" {
		q += ` WHERE id = ?`
		args = append(args, only)
	}
	rows, err := d.db.Query(q+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][3]string
	for rows.Next() {
		var c [3]string
		if err := rows.Scan(&c[0], &c[1], &c[2]); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ResolveChat turns an id or a unique prefix of one into the id of a chat.
func (d *DB) ResolveChat(idOrPrefix string) (string, error) {
	rows, err := d.db.Query(`SELECT id FROM chats WHERE id LIKE ? ESCAPE '\' ORDER BY id`, strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(idOrPrefix)+"%")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		ids = append(ids, id)
	}
	switch {
	case len(ids) == 1:
		return ids[0], rows.Err()
	case len(ids) == 0:
		return "", fmt.Errorf("no chat %q", idOrPrefix)
	}
	for _, id := range ids {
		if id == idOrPrefix {
			return id, nil
		}
	}
	return "", fmt.Errorf("%q is the start of %d chats; give more of the id", idOrPrefix, len(ids))
}

func (d *DB) columns(schema, table, skip string) ([]string, error) {
	rows, err := d.db.Query(`SELECT name FROM pragma_table_info(?, ?)`, table, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		if c != skip {
			cols = append(cols, c)
		}
	}
	return cols, rows.Err()
}

// Rebuild builds the projections of one chat (all chats when chatID is "") again from the events,
// compares them with the current ones and, unless dryRun, replaces the current ones that differ.
// The node's own state (the read cursors) is kept.
func (d *DB) Rebuild(chatID string, dryRun bool) (RebuildReport, error) {
	rep := RebuildReport{Diffs: map[string]int{}}
	if done, err := d.BackfillDone(); err != nil || !done {
		return rep, fmt.Errorf("the rows that predate the journal have not been moved into it yet: start the broker once, then rebuild (%v)", err)
	}
	chats, err := d.chatIDs(chatID)
	if err != nil {
		return rep, err
	}
	rep.Chats = len(chats)
	dir, err := os.MkdirTemp("", "ccqc-rebuild-")
	if err != nil {
		return rep, err
	}
	defer os.RemoveAll(dir)
	shadow := filepath.Join(dir, "shadow.db")
	if rep.Events, err = d.replayInto(shadow, chats); err != nil {
		return rep, err
	}
	if _, err := d.db.Exec(`ATTACH DATABASE ? AS shadow`, shadow); err != nil {
		return rep, err
	}
	defer d.db.Exec(`DETACH DATABASE shadow`) //nolint:errcheck // best effort; the connection is dropped with the process anyway

	if len(chats) == 0 {
		return rep, nil
	}
	ids := make([]any, len(chats))
	for i, c := range chats {
		ids[i] = c[0]
	}
	in := `(?` + strings.Repeat(", ?", len(ids)-1) + `)`
	for _, p := range projections {
		cols, err := d.columns("main", p.table, p.skip)
		if err != nil {
			return rep, err
		}
		list := strings.Join(cols, ", ")
		pred := p.chatCol + ` IN ` + in
		both := append(append([]any{}, ids...), ids...)
		for _, dir := range [][2]string{{"main", "shadow"}, {"shadow", "main"}} {
			var n int
			q := `SELECT COUNT(*) FROM (SELECT ` + list + ` FROM ` + dir[0] + `.` + p.table + ` WHERE ` + pred +
				` EXCEPT SELECT ` + list + ` FROM ` + dir[1] + `.` + p.table + ` WHERE ` + pred + `)`
			if err := d.db.QueryRow(q, both...).Scan(&n); err != nil {
				return rep, fmt.Errorf("compare %s: %w", p.table, err)
			}
			if n > 0 {
				rep.Diffs[p.table] += n
			}
		}
	}
	var clocks int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM main.chats m JOIN shadow.chats s ON s.id = m.id WHERE m.id IN `+in+` AND m.lamport != s.lamport`, ids...).Scan(&clocks); err != nil {
		return rep, err
	}
	if clocks > 0 {
		rep.Diffs["chats.lamport"] = clocks
	}
	if rep.Total() == 0 || dryRun {
		return rep, nil
	}
	if err := d.swapProjections(ids, in); err != nil {
		return rep, err
	}
	rep.Replaced = true
	return rep, nil
}

// replayInto writes a database at path with the chats and the projections their events give.
func (d *DB) replayInto(path string, chats [][3]string) (int, error) {
	sh, err := Open(path)
	if err != nil {
		return 0, err
	}
	defer sh.Close()
	total := 0
	for _, c := range chats {
		n, err := d.replayChat(sh, c)
		total += n
		if err != nil {
			return total, fmt.Errorf("chat %s: %w", c[0][:min(8, len(c[0]))], err)
		}
	}
	return total, nil
}

func (d *DB) replayChat(sh *DB, c [3]string) (int, error) {
	tx, err := sh.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.Exec(`INSERT INTO chats (id, name, created_at) VALUES (?, ?, ?)`, c[0], c[1], c[2]); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`INSERT INTO rooms (chat_id, room) VALUES (?, 'common')`, c[0]); err != nil {
		return 0, err
	}
	rows, err := d.db.Query(`SELECT origin, seq, prev, lamport, kind, payload, author_pub, sig, assigned, ts, node_sig FROM events
		WHERE chat_id = ? ORDER BY lamport, origin, seq`, c[0])
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		e := events.Event{ChatID: c[0]}
		if err := rows.Scan(&e.Origin, &e.Seq, &e.Prev, &e.Lamport, &e.Kind, &e.Payload, &e.AuthorPub, &e.Sig, &e.Assigned, &e.TS, &e.NodeSig); err != nil {
			return n, err
		}
		if err := applyEvent(tx, e); err != nil {
			return n, fmt.Errorf("event %s#%d (%s): %w", e.Origin, e.Seq, e.Kind, err)
		}
		if _, err := tx.Exec(`UPDATE chats SET lamport = MAX(lamport, ?) WHERE id = ?`, e.Lamport, c[0]); err != nil {
			return n, err
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return n, err
	}
	return n, tx.Commit()
}

// swapProjections replaces the current projections of the chats with the rebuilt ones (attached
// as "shadow"), keeping the nicks' read cursors, in one transaction.
func (d *DB) swapProjections(ids []any, in string) error {
	lists := map[string]string{} // read before the transaction: the database has one connection
	for _, p := range projections {
		cols, err := d.columns("main", p.table, "")
		if err != nil {
			return err
		}
		lists[p.table] = strings.Join(cols, ", ")
	}
	return d.inTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`CREATE TEMP TABLE keep_seen AS SELECT chat_id, nick, seen FROM main.nicks WHERE chat_id IN `+in, ids...); err != nil {
			return err
		}
		for i := len(projections) - 1; i >= 0; i-- {
			p := projections[i]
			if _, err := tx.Exec(`DELETE FROM main.`+p.table+` WHERE `+p.chatCol+` IN `+in, ids...); err != nil {
				return fmt.Errorf("clear %s: %w", p.table, err)
			}
		}
		for _, p := range projections {
			list := lists[p.table]
			if _, err := tx.Exec(`INSERT INTO main.`+p.table+` (`+list+`) SELECT `+list+` FROM shadow.`+p.table+` WHERE `+p.chatCol+` IN `+in, ids...); err != nil {
				return fmt.Errorf("write %s: %w", p.table, err)
			}
		}
		if _, err := tx.Exec(`UPDATE main.nicks SET seen = COALESCE((SELECT k.seen FROM keep_seen k WHERE k.chat_id = main.nicks.chat_id AND k.nick = main.nicks.nick), seen)
			WHERE chat_id IN `+in, ids...); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE main.chats SET lamport = (SELECT s.lamport FROM shadow.chats s WHERE s.id = main.chats.id) WHERE id IN `+in, ids...); err != nil {
			return err
		}
		_, err := tx.Exec(`DROP TABLE keep_seen`)
		return err
	})
}
