package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// chatDB is the database of one chat: the journal of its events and the projections built from
// it. It has one connection, so one transaction at a time; chats do not wait for each other.
type chatDB struct {
	s    *DB
	id   string
	path string
	db   *sql.DB
}

// openChat opens (creating if needed) and migrates the database of a chat and lists it in node.db.
// A file migrated by a newer version of ccqc is refused: only this chat is unavailable.
func (d *DB) openChat(id, name, path string) (*chatDB, error) {
	db, err := openMigrated(path, ChatMigrations)
	if err != nil {
		return nil, fmt.Errorf("chat %.8s: %w", id, err)
	}
	c := &chatDB{s: d, id: id, path: path, db: db}
	created := now()
	res, err := db.Exec(`INSERT OR IGNORE INTO chat (id, chat_id, name, created_at) VALUES (1, ?, ?, ?)`, id, name, created)
	if err != nil {
		db.Close()
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 1 { // a new chat: every change it will have is an event, in the second version of the journal
		if _, err := db.Exec(`INSERT OR IGNORE INTO meta (key, value) VALUES (?, 'done'), (?, ?)`, metaBackfill, metaJournal, journalVersion); err != nil {
			db.Close()
			return nil, err
		}
	} else if name != "" {
		if _, err := db.Exec(`UPDATE chat SET name = ? WHERE id = 1 AND name = ''`, name); err != nil {
			db.Close()
			return nil, err
		}
	}
	var have string
	if err := db.QueryRow(`SELECT chat_id FROM chat WHERE id = 1`).Scan(&have); err != nil || have != id {
		db.Close()
		return nil, fmt.Errorf("chat %.8s: the file belongs to another chat (%q, %v)", id, have, err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO rooms (room) VALUES (?)`, proto.RoomCommon); err != nil {
		db.Close()
		return nil, err
	}
	if err := c.upgradeJournal(d.key); err != nil { // d.mu is held by the caller: read the key without it
		db.Close()
		return nil, fmt.Errorf("chat %.8s: %w", id, err)
	}
	if err := db.QueryRow(`SELECT name, created_at FROM chat WHERE id = 1`).Scan(&name, &created); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := d.node.Exec(`INSERT INTO chats (chat_id, name, file, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (chat_id) DO UPDATE SET name = excluded.name WHERE excluded.name <> ''`, id, name, id+".db", created); err != nil {
		db.Close()
		return nil, err
	}
	return c, nil
}

func (c *chatDB) inTx(fn func(*sql.Tx) error) error {
	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	c.s.notify(c.id)
	return nil
}

func (c *chatDB) each(query string, args []any, fn func(*sql.Rows) error) error {
	rows, err := c.db.Query(query, args...)
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

// load returns the chat's state as records in replay order: rooms, nicks, memberships, messages.
func (c *chatDB) load() ([]store.Record, error) {
	var recs []store.Record
	err := c.each(`SELECT room, topic, created_by FROM rooms`, nil, func(rows *sql.Rows) error {
		r := store.Record{T: "room"}
		if err := rows.Scan(&r.Room, &r.Topic, &r.Nick); err != nil {
			return err
		}
		recs = append(recs, r)
		return nil
	})
	if err == nil {
		err = c.each(`SELECT nick, key_hash, seen, pinned_role, pubkey FROM nicks`, nil, func(rows *sql.Rows) error {
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
		err = c.each(`SELECT room, nick FROM room_members`, nil, func(rows *sql.Rows) error {
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
		err = c.each(`SELECT DISTINCT thread FROM messages`, nil, func(rows *sql.Rows) error {
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
			err = c.each(`SELECT id, thread, room, from_nick, from_role, to_nick, text, reply_to, ts, owner, author_pub, sig, payload
				FROM messages WHERE thread = ? ORDER BY id DESC LIMIT ?`,
				[]any{th, store.ThreadWindow}, func(rows *sql.Rows) error {
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
		if err = c.db.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM messages`).Scan(&top); err == nil {
			recs = append(recs, store.Record{T: "maxid", ID: top})
		}
	}
	return recs, err
}

// append applies one record: as an event of the chat's journal and, in the same transaction, to
// the projections. The read cursor ("seen") is the node's own state, not an event.
func (c *chatDB) append(r store.Record) error {
	if r.T == "seen" {
		_, err := c.db.Exec(`UPDATE nicks SET seen = ? WHERE nick = ? AND seen < ?`, r.ID, r.Nick, r.ID)
		if err != nil {
			return fmt.Errorf("sqlite: %s: %w", r.T, err)
		}
		return nil
	}
	if _, ok, _ := recordEvent(c.id, store.Record{T: r.T, Msg: &proto.Message{}}, 0); !ok {
		return fmt.Errorf("sqlite: unknown record kind %q", r.T)
	}
	if r.T == "msg" && r.Msg == nil {
		return fmt.Errorf("sqlite: msg record without message")
	}
	err := c.inTx(func(tx *sql.Tx) error {
		var lamport int64
		var err error
		if r.T == "msg" { // the chat's logical clock advances with every message (and journal row)
			lamport, err = tick(tx)
		} else { // other events carry the clock as it is
			lamport, err = chatClock(tx)
		}
		if err != nil {
			return err
		}
		e, _, err := recordEvent(c.id, r, lamport)
		if err != nil {
			return err
		}
		if e, err = c.appendEvent(tx, e); err != nil {
			return err
		}
		if err := applyEvent(tx, e); err != nil {
			return err
		}
		return applyLocalState(tx, r)
	})
	if err != nil && r.T == "msg" && r.ClientID != "" {
		// A repeat of an earlier send? Then the earlier message is the answer.
		var id int64
		if q := c.db.QueryRow(`SELECT id FROM messages WHERE from_nick = ? AND client_msg_id = ?`, r.Msg.From, r.ClientID).Scan(&id); q == nil {
			return &store.DuplicateError{ID: id}
		}
	}
	if err != nil {
		return fmt.Errorf("sqlite: %s: %w", r.T, err)
	}
	return nil
}

// Lamport returns the chat's logical clock: the value of the last write (0 before the first, and
// for a chat that does not exist).
func (d *DB) Lamport(chatID string) (int64, error) {
	c, err := d.chat(chatID, "", false)
	if err != nil || c == nil {
		return 0, err
	}
	var n int64
	err = c.db.QueryRow(`SELECT lamport FROM chat WHERE id = 1`).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

// MigrateChat opens the database of a chat (which migrates its schema) and closes it again.
func (d *DB) MigrateChat(chatID string) error {
	if _, err := d.chat(chatID, "", false); err != nil {
		return err
	}
	d.Release(chatID)
	return nil
}
