package sqlite

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/PHPCraftdream/cc-quant-chat/internal/events"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// Rows of a chat that were written before it had a journal (it was laid out from the single
// database of earlier versions) are moved into the journal once, inside the chat's new file and
// before it is put in place (layout.go). The move is mechanical. Payload, author key and
// signature are taken as they are (an unsigned row gets an empty signature). Order: the nicks,
// rooms and settings first, then messages, board changes and document versions by (lamport, kind
// of row, id). Rows that were never written one change at a time (tasks, sprints, the process,
// comments, documents) come in as one snapshot event each, at the end.

const backfillBatch = 5000

type chainWriter struct {
	c     *chatDB
	tx    *sql.Tx
	key   *nodeKey
	seq   int64
	prev  string
	count int
}

func (w *chainWriter) add(e events.Event) error {
	e.ChatID, e.Origin = w.c.id, w.key.origin
	e.Seq, e.Prev = w.seq+1, w.prev
	e.Sign(w.key.priv)
	if err := putEvent(w.tx, e); err != nil {
		return err
	}
	w.seq, w.prev = e.Seq, e.ID()
	w.count++
	return nil
}

func (w *chainWriter) simple(kind, ts string, lamport int64, payload, assigned any) error {
	p, err := jsonText(payload)
	if err != nil {
		return err
	}
	a, err := jsonText(assigned)
	if err != nil {
		return err
	}
	return w.add(events.Event{Lamport: lamport, Kind: kind, Payload: p, Assigned: a, TS: ts})
}

// backfillEvents writes the events of the rows the chat has and marks the chat complete. It does
// nothing for a chat that is complete already. It returns the number of events written.
func (c *chatDB) backfillEvents(key *nodeKey) (int, error) {
	tx, err := c.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var done, have int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM meta WHERE key = ? AND value = 'done'`, metaBackfill).Scan(&done); err != nil {
		return 0, err
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&have); err != nil {
		return 0, err
	}
	if done > 0 || have > 0 { // already in the journal: never twice
		if _, err := tx.Exec(`INSERT OR REPLACE INTO meta (key, value) VALUES (?, 'done')`, metaBackfill); err != nil {
			return 0, err
		}
		return 0, tx.Commit()
	}
	var created string
	if err := tx.QueryRow(`SELECT created_at FROM chat WHERE id = 1`).Scan(&created); err != nil {
		return 0, err
	}
	w := &chainWriter{c: c, tx: tx, key: key}
	if err := c.backfillState(w, created); err != nil {
		return 0, err
	}
	if err := c.backfillTimeline(w); err != nil {
		return 0, err
	}
	if err := c.backfillSnapshots(w); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO meta (key, value) VALUES (?, 'done')`, metaBackfill); err != nil {
		return 0, err
	}
	return w.count, tx.Commit()
}

func eachRow(tx *sql.Tx, q string, args []any, fn func(*sql.Rows) error) error {
	rows, err := tx.Query(q, args...)
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

// backfillState: nicks (with their pinned roles), rooms, memberships, settings of the chat.
func (c *chatDB) backfillState(w *chainWriter, ts string) error {
	type nickRow struct {
		nick, hash, role, pub string
		seen                  int64
	}
	var nicks []nickRow
	err := eachRow(w.tx, `SELECT nick, key_hash, seen, pinned_role, pubkey FROM nicks ORDER BY nick`, nil, func(rows *sql.Rows) error {
		var n nickRow
		if err := rows.Scan(&n.nick, &n.hash, &n.seen, &n.role, &n.pub); err != nil {
			return err
		}
		nicks = append(nicks, n)
		return nil
	})
	if err != nil {
		return err
	}
	for _, n := range nicks {
		if err := w.simple("nick.register", ts, 0, map[string]any{"nick": n.nick, "pub": n.pub}, map[string]any{"hash": n.hash, "seen": n.seen}); err != nil {
			return err
		}
		if n.role != "" {
			if err := w.simple("nick.pin", ts, 0, map[string]any{"nick": n.nick, "role": n.role}, map[string]any{}); err != nil {
				return err
			}
		}
	}
	type roomRow struct{ room, topic, by string }
	var rooms []roomRow
	err = eachRow(w.tx, `SELECT room, topic, created_by FROM rooms WHERE room != ? ORDER BY room`, []any{proto.RoomCommon}, func(rows *sql.Rows) error {
		var r roomRow
		if err := rows.Scan(&r.room, &r.topic, &r.by); err != nil {
			return err
		}
		rooms = append(rooms, r)
		return nil
	})
	if err != nil {
		return err
	}
	for _, r := range rooms {
		if err := w.simple("room.create", ts, 0, map[string]any{"room": r.room, "topic": r.topic, "created_by": r.by}, map[string]any{}); err != nil {
			return err
		}
	}
	var members [][2]string
	err = eachRow(w.tx, `SELECT room, nick FROM room_members WHERE room != ? ORDER BY room, nick`, []any{proto.RoomCommon}, func(rows *sql.Rows) error {
		var m [2]string
		if err := rows.Scan(&m[0], &m[1]); err != nil {
			return err
		}
		members = append(members, m)
		return nil
	})
	if err != nil {
		return err
	}
	for _, m := range members {
		if err := w.simple("room.join", ts, 0, map[string]any{"room": m[0], "nick": m[1]}, map[string]any{}); err != nil {
			return err
		}
	}
	var settings [][2]string
	err = eachRow(w.tx, `SELECT key, value FROM settings ORDER BY key`, nil, func(rows *sql.Rows) error {
		var s [2]string
		if err := rows.Scan(&s[0], &s[1]); err != nil {
			return err
		}
		settings = append(settings, s)
		return nil
	})
	if err != nil {
		return err
	}
	for _, s := range settings {
		if err := w.simple("config.set", ts, 0, map[string]any{"key": s[0], "value": s[1]}, map[string]any{}); err != nil {
			return err
		}
	}
	return nil
}

// A row of the board's journal or of a page's versions waiting to become an event.
type legacyRow struct {
	lamport                 int64
	class                   int // 1 board, 2 document: messages (0) come first at the same clock value
	id                      string
	ts, actor, grant        string
	owner                   bool
	authorPub, sig, payload string
	note                    string
	kind                    string
	touch                   touch
}

func (c *chatDB) backfillTimeline(w *chainWriter) error {
	var rest []legacyRow
	err := eachRow(w.tx, `SELECT id, lamport, ts, actor, actor_owner, actor_grant, kind, author_pub, sig, payload FROM board_log`, nil, func(rows *sql.Rows) error {
		r := legacyRow{class: 1}
		var id int64
		var owner int
		if err := rows.Scan(&id, &r.lamport, &r.ts, &r.actor, &owner, &r.grant, &r.kind, &r.authorPub, &r.sig, &r.payload); err != nil {
			return err
		}
		r.owner, r.id, r.touch = owner != 0, fmt.Sprintf("%020d", id), touch{"board_log", []any{id}}
		rest = append(rest, r)
		return nil
	})
	if err != nil {
		return err
	}
	err = eachRow(w.tx, `SELECT path, version, lamport, ts, author, author_owner, note, deleted, author_pub, sig, payload FROM doc_versions`, nil, func(rows *sql.Rows) error {
		r := legacyRow{class: 2}
		var path string
		var version int64
		var owner, deleted int
		if err := rows.Scan(&path, &version, &r.lamport, &r.ts, &r.actor, &owner, &r.note, &deleted, &r.authorPub, &r.sig, &r.payload); err != nil {
			return err
		}
		r.owner, r.id, r.touch = owner != 0, fmt.Sprintf("%s|%020d", path, version), touch{"doc_versions", []any{path, version}}
		r.kind = docKind(r.note, deleted != 0)
		rest = append(rest, r)
		return nil
	})
	if err != nil {
		return err
	}
	sort.SliceStable(rest, func(i, j int) bool {
		a, b := rest[i], rest[j]
		if a.lamport != b.lamport {
			return a.lamport < b.lamport
		}
		if a.class != b.class {
			return a.class < b.class
		}
		return a.id < b.id
	})

	rec := &recorder{c: c, tx: w.tx}
	next := 0
	flush := func(upTo int64) error { // rows of the board and documents up to a clock value
		for next < len(rest) && rest[next].lamport <= upTo {
			n := 1
			if rest[next].class == 2 && rest[next].sig != "" && next+1 < len(rest) && rest[next+1].class == 2 &&
				rest[next+1].lamport == rest[next].lamport && rest[next+1].sig == rest[next].sig {
				n = 2 // the two versions a move writes are one event
			}
			if err := c.legacyEvent(w, rec, rest[next:next+n]); err != nil {
				return err
			}
			next += n
		}
		return nil
	}
	// Messages in pages by (lamport, id); the other rows are put in between at their clock value.
	var lamport, id int64 = -1, 0
	for {
		type msgRow struct {
			rec store.Record
			lam int64
		}
		var page []msgRow
		err := eachRow(w.tx, `SELECT id, thread, room, from_nick, from_role, to_nick, text, reply_to, ts, client_msg_id, owner, lamport, author_pub, sig, payload
			FROM messages WHERE (lamport, id) > (?, ?) ORDER BY lamport, id LIMIT ?`, []any{lamport, id, backfillBatch}, func(rows *sql.Rows) error {
			m := &proto.Message{}
			r := store.Record{T: "msg", Msg: m}
			var owner int
			var lam int64
			if err := rows.Scan(&m.ID, &r.Key, &m.Room, &m.From, &m.FromRole, &m.To, &m.Text, &m.ReplyTo, &m.TS, &r.ClientID, &owner, &lam, &r.AuthorPub, &r.Sig, &r.Payload); err != nil {
				return err
			}
			m.Owner = owner != 0
			page = append(page, msgRow{r, lam})
			return nil
		})
		if err != nil {
			return err
		}
		for _, m := range page {
			if err := flush(m.lam - 1); err != nil { // rows of earlier clock values first
				return err
			}
			e, _, err := recordEvent(c.id, m.rec, m.lam)
			if err != nil {
				return err
			}
			if err := w.add(e); err != nil {
				return err
			}
		}
		if len(page) < backfillBatch {
			break
		}
		last := page[len(page)-1]
		lamport, id = last.lam, last.rec.Msg.ID
	}
	return flush(1<<62 - 1)
}

// docKind guesses the operation of a page version from what the writer left in it.
func docKind(note string, deleted bool) string {
	switch {
	case strings.HasPrefix(note, "moved "):
		return "doc.move"
	case note == "deleted" && deleted:
		return "doc.delete"
	case note == "append":
		return "doc.append"
	case strings.HasPrefix(note, "revert to v"):
		return "doc.revert"
	}
	return "doc.put"
}

// legacyEvent writes one event for one row of the board's journal, or one or two versions of a
// page: the row images are the rows as they are now.
func (c *chatDB) legacyEvent(w *chainWriter, rec *recorder, rows []legacyRow) error {
	r := rows[0]
	imgs := []image{}
	for _, x := range rows {
		img, ok, err := rec.row(x.touch)
		if err != nil {
			return err
		}
		if ok {
			imgs = append(imgs, img)
		}
	}
	assigned, err := jsonText(map[string]any{"actor": r.actor, "owner": r.owner, "grant": r.grant, "rows": imgs, "changes": changesOf(imgs)})
	if err != nil {
		return err
	}
	e := events.Event{Lamport: r.lamport, Kind: r.kind, Payload: r.payload, AuthorPub: r.authorPub, Sig: r.sig, Assigned: assigned, TS: r.ts}
	if r.sig != "" { // the kind the author signed is the operation, not what the journal row calls it
		e.ChatID, e.Origin = c.id, w.key.origin
		cands := []string{r.kind, "board.revert", "board.workflow_set"}
		if r.class == 2 {
			cands = []string{r.kind, "doc.put", "doc.append", "doc.move", "doc.delete", "doc.revert"}
		}
		for _, k := range cands {
			e.Kind = k
			if ok, _ := e.VerifyAuthor(); ok {
				break
			}
			e.Kind = r.kind
		}
	}
	if e.Payload == "" {
		e.Payload = "{}"
	}
	return w.add(e)
}

// backfillSnapshots: one event with images of all rows of the board that are not rows of its
// journal, one for the documents.
func (c *chatDB) backfillSnapshots(w *chainWriter) error {
	rec := &recorder{c: c, tx: w.tx}
	var lamport int64
	if err := w.tx.QueryRow(`SELECT lamport FROM chat WHERE id = 1`).Scan(&lamport); err != nil {
		return err
	}
	for _, s := range []struct {
		table  string
		keyCol string
		keyOf  func(*sql.Rows) ([]any, error)
	}{
		{"board_sprints", "id", scanIntKey},
		{"board_tasks", "id", scanIntKey},
		{"board_workflow", "", nil},
		{"board_comments", "id", scanIntKey},
		{"docs", "path", scanStrKey},
	} {
		if err := collectKeys(w, rec, s.table, s.keyCol, s.keyOf); err != nil {
			return err
		}
	}
	byKind := map[string][]image{}
	for _, t := range rec.touched {
		img, ok, err := rec.row(t)
		if err != nil {
			return err
		}
		if ok {
			k := "import.board"
			if t.table == "docs" {
				k = "import.docs"
			}
			byKind[k] = append(byKind[k], img)
		}
	}
	ts := now()
	for _, kind := range []string{"import.board", "import.docs"} {
		if len(byKind[kind]) == 0 {
			continue
		}
		a, err := jsonText(map[string]any{"actor": "", "owner": false, "grant": "", "rows": byKind[kind]})
		if err != nil {
			return err
		}
		if err := w.add(events.Event{Lamport: lamport, Kind: kind, Payload: "{}", Assigned: a, TS: ts}); err != nil {
			return err
		}
	}
	return nil
}

func scanIntKey(rows *sql.Rows) ([]any, error) {
	var k int64
	err := rows.Scan(&k)
	return []any{k}, err
}

func scanStrKey(rows *sql.Rows) ([]any, error) {
	var k string
	err := rows.Scan(&k)
	return []any{k}, err
}

// collectKeys notes every row of a table in rec.
func collectKeys(w *chainWriter, rec *recorder, table, keyCol string, keyOf func(*sql.Rows) ([]any, error)) error {
	if keyCol == "" {
		var one int
		err := w.tx.QueryRow(`SELECT 1 FROM ` + table + ` LIMIT 1`).Scan(&one)
		if err == nil {
			rec.touch(table)
			return nil
		}
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}
	return eachRow(w.tx, `SELECT `+keyCol+` FROM `+table+` ORDER BY `+keyCol, nil, func(rows *sql.Rows) error {
		k, err := keyOf(rows)
		if err != nil {
			return err
		}
		rec.touch(table, k...)
		return nil
	})
}
