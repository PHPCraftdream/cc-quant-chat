// Package sqlite: store.Backend on a SQLite database in WAL mode (pure Go driver, no cgo).
// The schema is created and upgraded by the embedded migrations on Open.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
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

// DefaultPath: CCQC_DB or <user config dir>/ccqc/ccqc.db.
func DefaultPath() string {
	if p := os.Getenv("CCQC_DB"); p != "" {
		return p
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "ccqc", "ccqc.db")
}

type DB struct {
	db *sql.DB
}

var _ store.Backend = (*DB)(nil)

// OpenRaw opens the database without migrating (for status reports).
func OpenRaw(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	q := url.Values{}
	for _, p := range []string{"journal_mode(WAL)", "synchronous(NORMAL)", "foreign_keys(1)", "busy_timeout(5000)"} {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", "immediate") // writers take the lock at BEGIN: no upgrade deadlocks
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?"+q.Encode())
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
	return &DB{db: db}, nil
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
		err = d.each(`SELECT nick, key_hash, seen FROM nicks WHERE chat_id = ?`, chatID, func(rows *sql.Rows) error {
			r := store.Record{T: "nick"}
			if err := rows.Scan(&r.Nick, &r.Hash, &r.ID); err != nil {
				return err
			}
			recs = append(recs, r)
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
	if err == nil {
		err = d.each(`SELECT id, thread, room, from_nick, from_role, to_nick, text, reply_to, ts
			FROM messages WHERE chat_id = ? ORDER BY id`, chatID, func(rows *sql.Rows) error {
			var m proto.Message
			r := store.Record{T: "msg", Msg: &m}
			if err := rows.Scan(&m.ID, &r.Key, &m.Room, &m.From, &m.FromRole, &m.To, &m.Text, &m.ReplyTo, &m.TS); err != nil {
				return err
			}
			recs = append(recs, r)
			return nil
		})
	}
	return recs, err
}

func (d *DB) each(query, chatID string, fn func(*sql.Rows) error) error {
	rows, err := d.db.Query(query, chatID)
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

// Append applies one record.
func (d *DB) Append(chatID string, r store.Record) error {
	var err error
	switch r.T {
	case "nick":
		_, err = d.db.Exec(`INSERT INTO nicks (chat_id, nick, key_hash, seen) VALUES (?, ?, ?, ?)
			ON CONFLICT (chat_id, nick) DO UPDATE SET key_hash = excluded.key_hash, seen = excluded.seen`,
			chatID, r.Nick, r.Hash, r.ID)
	case "seen":
		_, err = d.db.Exec(`UPDATE nicks SET seen = ? WHERE chat_id = ? AND nick = ?`, r.ID, chatID, r.Nick)
	case "room":
		_, err = d.db.Exec(`INSERT OR IGNORE INTO rooms (chat_id, room, topic, created_by) VALUES (?, ?, ?, ?)`,
			chatID, r.Room, r.Topic, r.Nick)
	case "in":
		_, err = d.db.Exec(`INSERT OR IGNORE INTO room_members (chat_id, room, nick) VALUES (?, ?, ?)`, chatID, r.Room, r.Nick)
	case "out":
		_, err = d.db.Exec(`DELETE FROM room_members WHERE chat_id = ? AND room = ? AND nick = ?`, chatID, r.Room, r.Nick)
	case "msg":
		if r.Msg == nil {
			return fmt.Errorf("sqlite: msg record without message")
		}
		m := r.Msg
		_, err = d.db.Exec(`INSERT INTO messages (chat_id, id, thread, room, from_nick, from_role, to_nick, text, reply_to, ts)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			chatID, m.ID, r.Key, m.Room, m.From, m.FromRole, m.To, m.Text, m.ReplyTo, m.TS)
	default:
		return fmt.Errorf("sqlite: unknown record kind %q", r.T)
	}
	if err != nil {
		return fmt.Errorf("sqlite: %s: %w", r.T, err)
	}
	return nil
}
