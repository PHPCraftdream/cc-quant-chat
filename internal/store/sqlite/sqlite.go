// Package sqlite: store.Backend on a SQLite database in WAL mode (pure Go driver, no cgo).
// The schema is created and upgraded by the embedded migrations on Open.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	_ "modernc.org/sqlite" // registers driver "sqlite"

	"github.com/PHPCraftdream/cc-quant-chat/internal/migrate"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migrations returns the embedded schema migrations.
func Migrations() ([]migrate.Migration, error) { return migrate.Load(migrationFS, "migrations") }

// DefaultPath: CCQC_DB or <user config dir>/ccqc/ccqc.db. Without a config dir it falls back to
// ~/.ccqc/ccqc.db, never to a shared temp folder; "" when no private location exists (Open then
// asks for CCQC_DB).
func DefaultPath() string {
	if p := os.Getenv("CCQC_DB"); p != "" {
		return p
	}
	if base, err := os.UserConfigDir(); err == nil {
		return filepath.Join(base, "ccqc", "ccqc.db")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".ccqc", "ccqc.db")
	}
	return ""
}

type DB struct {
	db   *sql.DB
	path string
	node *nodeKey // signs events; see SetNode

	afterBackfillChat func(i int) error // test hook: called after each chat is moved into the journal
}

var _ store.Backend = (*DB)(nil)

// OpenRaw opens the database without migrating (for status reports).
func OpenRaw(path string) (*sql.DB, error) {
	if path == "" {
		return nil, errors.New("no private place for the database: set CCQC_DB")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// Create the file private before SQLite does; its -wal and -shm files inherit the mode.
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600); err != nil {
		return nil, err
	} else {
		f.Close()
	}
	q := url.Values{}
	for _, p := range []string{"journal_mode(WAL)", "synchronous(NORMAL)", "foreign_keys(1)", "busy_timeout(5000)"} {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", "immediate") // writers take the lock at BEGIN: no upgrade deadlocks
	db, err := sql.Open("sqlite", "file:"+(&url.URL{Path: filepath.ToSlash(path)}).EscapedPath()+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer; the broker serializes access anyway
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return db, nil
}

// Open opens (creating if needed) and migrates the database.
func Open(path string) (*DB, error) {
	db, err := OpenRaw(path)
	if err != nil {
		return nil, err
	}
	ms, err := Migrations()
	if err == nil {
		_, err = migrate.Up(context.Background(), db, ms)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	// A database without chats has nothing that predates the journal: nothing to move into it.
	if _, err := db.Exec(`INSERT OR IGNORE INTO meta (key, value) SELECT 'events_backfill', 'done' WHERE NOT EXISTS (SELECT 1 FROM chats)`); err != nil {
		db.Close()
		return nil, err
	}
	return &DB{db: db, path: path}, nil
}

func (d *DB) Close() error { return d.db.Close() }

// Load returns a chat's state as records in replay order: rooms, nicks, memberships, messages.
func (d *DB) Load(chatID, name string) ([]store.Record, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := d.db.Exec(`INSERT OR IGNORE INTO chats (id, name, created_at) VALUES (?, ?, ?)`, chatID, name, now); err != nil {
		return nil, err
	}
	if _, err := d.db.Exec(`INSERT OR IGNORE INTO rooms (chat_id, room) VALUES (?, ?)`, chatID, proto.RoomCommon); err != nil {
		return nil, err
	}
	var recs []store.Record
	err := d.each(`SELECT room, topic, created_by FROM rooms WHERE chat_id = ?`, chatID, func(rows *sql.Rows) error {
		var r store.Record
		r.T = "room"
		if err := rows.Scan(&r.Room, &r.Topic, &r.Nick); err != nil {
			return err
		}
		recs = append(recs, r)
		return nil
	})
	if err == nil {
		err = d.each(`SELECT nick, key_hash, seen, pinned_role, pubkey FROM nicks WHERE chat_id = ?`, chatID, func(rows *sql.Rows) error {
			r := store.Record{T: "nick"}
			var pinned string
			if err := rows.Scan(&r.Nick, &r.Hash, &r.ID, &pinned, &r.Pub); err != nil {
				return err
			}
			recs = append(recs, r)
			if pinned != "" {
				recs = append(recs, store.Record{T: "pin", Nick: r.Nick, Role: pinned})
			}
			return nil
		})
	}
	if err == nil {
		err = d.each(`SELECT room, nick FROM room_members WHERE chat_id = ?`, chatID, func(rows *sql.Rows) error {
			r := store.Record{T: "in"}
			if err := rows.Scan(&r.Room, &r.Nick); err != nil {
				return err
			}
			recs = append(recs, r)
			return nil
		})
	}
	if err == nil { // the last store.ThreadWindow messages of every thread; older ones stay in the database
		var threads []string
		err = d.each(`SELECT DISTINCT thread FROM messages WHERE chat_id = ?`, chatID, func(rows *sql.Rows) error {
			var th string
			if err := rows.Scan(&th); err != nil {
				return err
			}
			threads = append(threads, th)
			return nil
		})
		var msgs []store.Record
		for _, th := range threads {
			if err != nil {
				break
			}
			first := len(msgs)
			err = d.eachArgs(`SELECT id, thread, room, from_nick, from_role, to_nick, text, reply_to, ts, owner, author_pub, sig, payload
				FROM messages WHERE chat_id = ? AND thread = ? ORDER BY id DESC LIMIT ?`,
				[]any{chatID, th, store.ThreadWindow}, func(rows *sql.Rows) error {
					var m proto.Message
					r := store.Record{T: "msg", Msg: &m}
					if err := rows.Scan(&m.ID, &r.Key, &m.Room, &m.From, &m.FromRole, &m.To, &m.Text, &m.ReplyTo, &m.TS, &m.Owner, &r.AuthorPub, &r.Sig, &r.Payload); err != nil {
						return err
					}
					msgs = append(msgs, r)
					return nil
				})
			slices.Reverse(msgs[first:]) // newest first from the query: back to ascending
		}
		sort.SliceStable(msgs, func(i, j int) bool { return msgs[i].Msg.ID < msgs[j].Msg.ID })
		recs = append(recs, msgs...)
	}
	if err == nil { // message ids continue after the newest one, even if it is outside the window
		var top int64
		if err = d.db.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM messages WHERE chat_id = ?`, chatID).Scan(&top); err == nil {
			recs = append(recs, store.Record{T: "maxid", ID: top})
		}
	}
	return recs, err
}

func (d *DB) each(query, chatID string, fn func(*sql.Rows) error) error {
	return d.eachArgs(query, []any{chatID}, fn)
}

func (d *DB) eachArgs(query string, args []any, fn func(*sql.Rows) error) error {
	rows, err := d.db.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (d *DB) inTx(fn func(*sql.Tx) error) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Append applies one record: as an event of the chat's journal and, in the same transaction, to
// the projections. The read cursor ("seen") is the node's own state, not an event.
func (d *DB) Append(chatID string, r store.Record) error {
	if r.T == "seen" {
		_, err := d.db.Exec(`UPDATE nicks SET seen = ? WHERE chat_id = ? AND nick = ? AND seen < ?`, r.ID, chatID, r.Nick, r.ID)
		if err != nil {
			return fmt.Errorf("sqlite: %s: %w", r.T, err)
		}
		return nil
	}
	if _, ok, _ := recordEvent(chatID, store.Record{T: r.T, Msg: &proto.Message{}}, 0); !ok {
		return fmt.Errorf("sqlite: unknown record kind %q", r.T)
	}
	if r.T == "msg" && r.Msg == nil {
		return fmt.Errorf("sqlite: msg record without message")
	}
	err := d.inTx(func(tx *sql.Tx) error {
		var lamport int64
		var err error
		if r.T == "msg" { // the chat's logical clock advances with every message (and journal row)
			lamport, err = tick(tx, chatID)
		} else { // other events carry the clock as it is
			lamport, err = chatClock(tx, chatID)
		}
		if err != nil {
			return err
		}
		e, _, err := recordEvent(chatID, r, lamport)
		if err != nil {
			return err
		}
		if e, err = d.appendEvent(tx, e); err != nil {
			return err
		}
		return applyEvent(tx, e)
	})
	if err != nil && r.T == "msg" && r.ClientID != "" {
		// A repeat of an earlier send? Then the earlier message is the answer.
		var id int64
		if q := d.db.QueryRow(`SELECT id FROM messages WHERE chat_id = ? AND from_nick = ? AND client_msg_id = ?`,
			chatID, r.Msg.From, r.ClientID).Scan(&id); q == nil {
			return &store.DuplicateError{ID: id}
		}
	}
	if err != nil {
		return fmt.Errorf("sqlite: %s: %w", r.T, err)
	}
	return nil
}
