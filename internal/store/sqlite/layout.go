package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/migrate"
)

// The single database of earlier versions is laid out into the node database and one database per
// chat, once, by the broker at its start (it needs the node key to sign the events the old rows
// become). Every step can be interrupted: a chat is written into a temporary file, its journal is
// made inside it, and only then is the file renamed into place; the mark that the chat is done
// goes into node.db after that. Starting again finishes what is left and does nothing for what is
// done. The old file is kept, renamed to <name>.migrated, as a reserve.

const (
	metaLayout     = "layout"
	metaLayoutNode = "layout_node"
)

func metaLayoutChat(id string) string { return "layout:" + id }

func (d *DB) metaDone(key string) (bool, error) {
	var v string
	err := d.node.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return v == "done", err
}

func (d *DB) setMeta(key string) error {
	_, err := d.node.Exec(`INSERT OR REPLACE INTO meta (key, value) VALUES (?, 'done')`, key)
	return err
}

// layoutPending: there is a database of earlier versions that has not been laid out yet.
func (d *DB) layoutPending() bool {
	done, err := d.metaDone(metaLayout)
	return err != nil || !done
}

// BackfillEvents lays the database of earlier versions out into chat files (if there is one and
// it has not been done), making the journal of every chat. It reports how many chats and events
// this run wrote.
func (d *DB) BackfillEvents() (chats, events int, err error) {
	if !d.layoutPending() {
		return 0, 0, nil
	}
	key := d.nodeKey()
	if key == nil {
		return 0, 0, errors.New("sqlite: laying out the database of earlier versions needs the node key")
	}
	if _, err := os.Stat(d.legacy); errors.Is(err, fs.ErrNotExist) {
		return 0, 0, d.setMeta(metaLayout)
	}
	type chatRow struct {
		id, name, created string
		lamport           int64
	}
	var list []chatRow
	if err := d.readLegacy(func(old *sql.DB) error {
		if err := d.copyNodeTables(old); err != nil {
			return err
		}
		rows, err := old.Query(`SELECT id, name, created_at, lamport FROM chats ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c chatRow
			if err := rows.Scan(&c.id, &c.name, &c.created, &c.lamport); err != nil {
				return err
			}
			list = append(list, c)
		}
		return rows.Err()
	}); err != nil {
		return 0, 0, err
	}
	for i, c := range list {
		if done, err := d.metaDone(metaLayoutChat(c.id)); err != nil {
			return chats, events, err
		} else if !done {
			n, err := d.layoutChat(key, c.id, c.name, c.created, c.lamport)
			if err != nil {
				return chats, events, fmt.Errorf("chat %.8s: %w", c.id, err)
			}
			chats++
			events += n
		}
		if d.afterLayoutChat != nil {
			if err := d.afterLayoutChat(i); err != nil {
				return chats, events, err
			}
		}
	}
	if err := d.setMeta(metaLayout); err != nil {
		return chats, events, err
	}
	if err := d.retireLegacy(); err != nil {
		return chats, events, err
	}
	return chats, events, nil
}

// readLegacy opens the old database, brings it to the last schema of that format and runs fn.
func (d *DB) readLegacy(fn func(old *sql.DB) error) error {
	old, err := OpenRaw(d.legacy)
	if err != nil {
		return err
	}
	defer old.Close()
	ms, err := LegacyMigrations()
	if err == nil {
		_, err = migrate.Up(context.Background(), old, ms)
	}
	if err != nil {
		return fmt.Errorf("the database of earlier versions %s: %w", d.legacy, err)
	}
	return fn(old)
}

// copyNodeTables moves what belongs to the node: the owner key, origins, the administrator log and
// the broker-wide settings. Repeating it changes nothing.
func (d *DB) copyNodeTables(old *sql.DB) error {
	if done, err := d.metaDone(metaLayoutNode); err != nil || done {
		return err
	}
	tx, err := d.node.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	type copyStep struct{ read, write string }
	for _, s := range []copyStep{
		{`SELECT origin, pubkey, created_at FROM origins`, `INSERT OR IGNORE INTO origins (origin, pubkey, created_at) VALUES (?, ?, ?)`},
		{`SELECT id, pubkey, created_at FROM owner`, `INSERT OR IGNORE INTO owner (id, pubkey, created_at) VALUES (?, ?, ?)`},
		{`SELECT key, value FROM settings WHERE scope = ''`, `INSERT OR IGNORE INTO settings (key, value) VALUES (?, ?)`},
		{`SELECT ts, op, args, result, origin FROM admin_log ORDER BY id`, `INSERT INTO admin_log (ts, op, args, result, origin) VALUES (?, ?, ?, ?, ?)`},
	} {
		rows, err := old.Query(s.read)
		if err != nil {
			return err
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				return err
			}
			if _, err := tx.Exec(s.write, vals...); err != nil {
				rows.Close()
				return err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO meta (key, value) VALUES (?, 'done')`, metaLayoutNode); err != nil {
		return err
	}
	return tx.Commit()
}

func removeDB(path string) {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		_ = os.Remove(path + suffix)
	}
}

// layoutChat makes the database of one chat from the old one and puts it in place.
func (d *DB) layoutChat(key *nodeKey, id, name, created string, lamport int64) (int, error) {
	final := d.chatPath(id)
	if _, err := os.Stat(final); err == nil {
		// A file that is in place already: finished by an earlier run that was cut off before it
		// could say so. If it is complete, the chat is done; otherwise it is made again.
		if ok, _ := fileComplete(final, id); ok {
			return 0, d.markChat(id, name, created)
		}
		removeDB(final)
	}
	tmp := final + ".tmp"
	removeDB(tmp)
	cdb, err := openMigrated(tmp, ChatMigrations)
	if err != nil {
		return 0, err
	}
	ok := false
	defer func() {
		if !ok {
			cdb.Close()
			removeDB(tmp)
		}
	}()
	if err := copyChatRows(cdb, d.legacy, key.origin, id, name, created, lamport); err != nil {
		return 0, err
	}
	c := &chatDB{s: d, id: id, path: tmp, db: cdb}
	n, err := c.backfillEvents(key)
	if err != nil {
		return 0, err
	}
	// One file when it is closed: no -wal beside it to be left behind by the rename.
	if _, err := cdb.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return 0, err
	}
	if _, err := cdb.Exec(`PRAGMA journal_mode = DELETE`); err != nil {
		return 0, err
	}
	if err := cdb.Close(); err != nil {
		return 0, err
	}
	ok = true
	if err := os.Rename(tmp, final); err != nil {
		removeDB(tmp)
		return 0, err
	}
	return n, d.markChat(id, name, created)
}

// markChat lists the chat in node.db and records that its file is in place.
func (d *DB) markChat(id, name, created string) error {
	tx, err := d.node.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.Exec(`INSERT OR REPLACE INTO chats (chat_id, name, file, created_at) VALUES (?, ?, ?, ?)`, id, name, id+".db", created); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO meta (key, value) VALUES (?, 'done')`, metaLayoutChat(id)); err != nil {
		return err
	}
	return tx.Commit()
}

// fileComplete: the file is the database of this chat and its journal is complete.
func fileComplete(path, id string) (bool, error) {
	raw, err := OpenRaw(path)
	if err != nil {
		return false, err
	}
	defer raw.Close()
	var have string
	if err := raw.QueryRow(`SELECT chat_id FROM chat WHERE id = 1`).Scan(&have); err != nil || have != id {
		return false, err
	}
	var v string
	if err := raw.QueryRow(`SELECT value FROM meta WHERE key = ?`, metaBackfill).Scan(&v); err != nil {
		return false, err
	}
	return v == "done", nil
}

// copyChatRows copies the rows of one chat from the old database into a new chat database (the
// chat_id column is not carried over). Rows that had no origin get this node's.
func copyChatRows(cdb *sql.DB, legacy, origin, id, name, created string, lamport int64) error {
	if _, err := cdb.Exec(`ATTACH DATABASE ? AS old`, legacy); err != nil {
		return err
	}
	defer cdb.Exec(`DETACH DATABASE old`) //nolint:errcheck // best effort
	tx, err := cdb.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.Exec(`INSERT INTO chat (id, chat_id, name, created_at, lamport) VALUES (1, ?, ?, ?, ?)`, id, name, created, lamport); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO rooms (room) VALUES ('common')`); err != nil {
		return err
	}
	org := `CASE WHEN origin = '' THEN ? ELSE origin END`
	for _, s := range []struct {
		q    string
		args []any
	}{
		{`INSERT OR IGNORE INTO rooms (room, topic, created_by) SELECT room, topic, created_by FROM old.rooms WHERE chat_id = ?`, []any{id}},
		{`INSERT INTO room_members (room, nick) SELECT room, nick FROM old.room_members WHERE chat_id = ?`, []any{id}},
		{`INSERT INTO nicks (nick, key_hash, seen, pinned_role, pubkey) SELECT nick, key_hash, seen, pinned_role, pubkey FROM old.nicks WHERE chat_id = ?`, []any{id}},
		{`INSERT INTO messages (id, thread, room, from_nick, from_role, to_nick, text, reply_to, ts, client_msg_id, owner, origin, lamport, author_pub, sig, payload)
			SELECT id, thread, room, from_nick, from_role, to_nick, text, reply_to, ts, client_msg_id, owner, ` + org + `, lamport, author_pub, sig, payload FROM old.messages WHERE chat_id = ?`, []any{origin, id}},
		{`INSERT INTO board_sprints (id, name, goal, state, created_at, started_at, closed_at, deleted)
			SELECT id, name, goal, state, created_at, started_at, closed_at, deleted FROM old.board_sprints WHERE chat_id = ?`, []any{id}},
		{`INSERT INTO board_tasks (id, title, description, status, sprint, assignee, priority, created_by, created_at, updated_at, deleted)
			SELECT id, title, description, status, sprint, assignee, priority, created_by, created_at, updated_at, deleted FROM old.board_tasks WHERE chat_id = ?`, []any{id}},
		{`INSERT INTO board_workflow (id, json, updated_at) SELECT 1, json, updated_at FROM old.board_workflow WHERE chat_id = ?`, []any{id}},
		{`INSERT INTO board_log (id, ts, actor, actor_owner, kind, entity, entity_id, before_json, after_json, reverts, note, origin, lamport, actor_grant, author_pub, sig, payload)
			SELECT id, ts, actor, actor_owner, kind, entity, entity_id, before_json, after_json, reverts, note, ` + org + `, lamport, actor_grant, author_pub, sig, payload FROM old.board_log WHERE chat_id = ?`, []any{origin, id}},
		{`INSERT INTO board_comments (id, task_id, ts, author, author_owner, text, deleted, origin, lamport, author_pub, sig)
			SELECT id, task_id, ts, author, author_owner, text, deleted, ` + org + `, lamport, author_pub, sig FROM old.board_comments WHERE chat_id = ?`, []any{origin, id}},
		{`INSERT INTO docs (path, title, body, version, author, author_owner, ts, deleted)
			SELECT path, title, body, version, author, author_owner, ts, deleted FROM old.docs WHERE chat_id = ?`, []any{id}},
		{`INSERT INTO doc_versions (path, version, ts, author, author_owner, title, body, note, deleted, origin, lamport, author_pub, sig, payload)
			SELECT path, version, ts, author, author_owner, title, body, note, deleted, ` + org + `, lamport, author_pub, sig, payload FROM old.doc_versions WHERE chat_id = ?`, []any{origin, id}},
		{`INSERT INTO settings (key, value) SELECT key, value FROM old.settings WHERE scope = ?`, []any{id}},
	} {
		if _, err := tx.Exec(s.q, s.args...); err != nil {
			return fmt.Errorf("copy: %w", err)
		}
	}
	return tx.Commit()
}

// retireLegacy renames the old database (the reserve) once everything is laid out.
func (d *DB) retireLegacy() error {
	if _, err := os.Stat(d.legacy); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if old, err := OpenRaw(d.legacy); err == nil { // fold its -wal in, so one file is left
		_, _ = old.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
		old.Close()
	}
	target := d.legacy + ".migrated"
	if _, err := os.Stat(target); err == nil {
		target = fmt.Sprintf("%s.migrated-%d", d.legacy, time.Now().Unix())
	}
	if err := os.Rename(d.legacy, target); err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(d.legacy + suffix)
	}
	return nil
}
