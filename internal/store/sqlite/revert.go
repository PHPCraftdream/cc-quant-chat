package sqlite

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/events"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

var _ store.Reverts = (*DB)(nil)

// A revert is an event of the chat's journal (peer.revert), signed by the owner, and withdrawing it is
// another (peer.unrevert). Which reverts are in force is read from the journal every time the
// projections are built; nothing else holds it. The events of the node stay in the journal (its chain
// and signatures verify); the projections are built without them.

const (
	kindRevert   = "peer.revert"
	kindUnrevert = "peer.unrevert"
	metaRevert   = "revert:" // meta key prefix: revert:<origin> = "<skipped> <own>"
)

type revertRule struct {
	id     string
	origin string
	since  time.Time // zero: every event of the node
	undone bool
}

type queryer interface {
	Query(string, ...any) (*sql.Rows, error)
}

// loadRules reads the reverts of the chat from its journal, in order.
func loadRules(q queryer) ([]revertRule, error) {
	rows, err := q.Query(`SELECT kind, assigned FROM events WHERE kind IN ('peer.revert', 'peer.unrevert') AND hidden = '' AND remote = 0 ORDER BY ord`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []revertRule
	for rows.Next() {
		var kind, assigned string
		if err := rows.Scan(&kind, &assigned); err != nil {
			return nil, err
		}
		var a struct {
			Revert *struct {
				ID     string `json:"id"`
				Origin string `json:"origin"`
				Since  string `json:"since"`
			} `json:"revert"`
			Unrevert string `json:"unrevert"`
		}
		if err := json.Unmarshal([]byte(assigned), &a); err != nil {
			return nil, fmt.Errorf("%s: assigned: %w", kind, err)
		}
		switch {
		case kind == kindRevert && a.Revert != nil:
			r := revertRule{id: a.Revert.ID, origin: a.Revert.Origin}
			if a.Revert.Since != "" {
				if r.since, err = time.Parse(time.RFC3339, a.Revert.Since); err != nil {
					return nil, fmt.Errorf("%s: since: %w", kind, err)
				}
			}
			out = append(out, r)
		case kind == kindUnrevert:
			for i := range out {
				if out[i].id == a.Unrevert {
					out[i].undone = true
				}
			}
		}
	}
	return out, rows.Err()
}

// skipper decides, event by event and in the order of the journal, what the projections leave out:
// the events of a reverted node from the time given, and the events that name a thing those made
// (a task, a page, a comment): they do not stand without it. A message that replies to a skipped one
// stays; the reply link is let go (it finds nothing to point at).
type skipper struct {
	self    string
	rules   []revertRule // in force
	skipped map[string]string
	count   map[string]*store.RevertReport // by the node whose revert is the cause
}

func newSkipper(rules []revertRule, self string) *skipper {
	s := &skipper{self: self, skipped: map[string]string{}, count: map[string]*store.RevertReport{}}
	for _, r := range rules {
		if !r.undone {
			s.rules = append(s.rules, r)
		}
	}
	return s
}

func (s *skipper) active() bool { return s != nil && len(s.rules) > 0 }

func key(origin string, seq int64) string { return origin + "#" + strconv.FormatInt(seq, 10) }

// cause: the node whose revert applies to the event, or "".
func (s *skipper) cause(e events.Event) string {
	for _, r := range s.rules {
		if e.Origin != r.origin {
			continue
		}
		if r.since.IsZero() {
			return r.origin
		}
		if t, err := time.Parse(time.RFC3339, e.TS); err != nil || !t.Before(r.since) {
			return r.origin
		}
	}
	return ""
}

func (s *skipper) note(e events.Event, cause string) {
	s.skipped[key(e.Origin, e.Seq)] = cause
	c := s.count[cause]
	if c == nil {
		c = &store.RevertReport{}
		s.count[cause] = c
	}
	c.Skipped++
	if e.Origin == s.self {
		c.SkippedOwn++
	}
}

// skipsOrigin: the event is of a node reverted from this time. It is what the live path asks: the
// event is stored and not applied.
func (s *skipper) skipsOrigin(e events.Event) bool { return s.active() && s.cause(e) != "" }

// skip decides for the replay.
func (s *skipper) skip(e events.Event) bool {
	if !s.active() {
		return false
	}
	if c := s.cause(e); c != "" {
		s.note(e, c)
		return true
	}
	if e.Kind == "msg" || e.Assigned == "" {
		return false
	}
	for _, r := range refsIn(e.Assigned) {
		if c, ok := s.skipped[key(r.Origin, r.Seq)]; ok {
			s.note(e, c)
			return true
		}
	}
	return false
}

// refsIn finds the global names an event mentions: objects {"o": origin, "s": seq} anywhere in what
// the node assigned (the references of the board, of pages and of replies).
func refsIn(assigned string) []eventRef {
	dec := json.NewDecoder(bytes.NewReader([]byte(assigned)))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return nil
	}
	var out []eventRef
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case map[string]any:
			o, hasO := t["o"].(string)
			s, hasS := t["s"].(json.Number)
			if hasO && hasS && len(t) == 2 {
				if n, err := s.Int64(); err == nil {
					out = append(out, eventRef{Origin: o, Seq: n})
					return
				}
			}
			for _, y := range t {
				walk(y)
			}
		case []any:
			for _, y := range t {
				walk(y)
			}
		}
	}
	walk(v)
	return out
}

// RevertNode: see store.Reverts.
func (d *DB) RevertNode(chatID, id, origin, since string, a store.AdminAttest) (store.RevertReport, error) {
	if since != "" {
		if _, err := time.Parse(time.RFC3339, since); err != nil {
			return store.RevertReport{}, fmt.Errorf("since is a time like 2026-03-01T12:00:00Z: %w", err)
		}
	}
	return d.writeRevert(chatID, kindRevert, a, map[string]any{"revert": map[string]string{"id": id, "origin": origin, "since": since}})
}

// UndoRevert: see store.Reverts.
func (d *DB) UndoRevert(chatID, id string, a store.AdminAttest) (store.RevertReport, error) {
	return d.writeRevert(chatID, kindUnrevert, a, map[string]any{"unrevert": id})
}

func (d *DB) writeRevert(chatID, kind string, a store.AdminAttest, fields map[string]any) (store.RevertReport, error) {
	c, done, err := d.acquire(chatID, "", false)
	if err != nil || c == nil {
		done()
		if err == nil {
			err = fmt.Errorf("sqlite: no chat %.8s", chatID)
		}
		return store.RevertReport{}, err
	}
	err = c.inTx(func(tx *sql.Tx) error { return c.inviteEvent(tx, kind, &a, fields) })
	done()
	if err != nil {
		return store.RevertReport{}, err
	}
	rep, err := d.Rebuild(chatID, false)
	if err != nil {
		return store.RevertReport{}, err
	}
	return store.RevertReport{Skipped: rep.Skipped, SkippedOwn: rep.SkippedOwn}, nil
}

func (d *DB) Reverts(chatID string) ([]store.RevertInfo, error) {
	c, done, err := d.acquire(chatID, "", false)
	defer done()
	if err != nil || c == nil {
		return nil, err
	}
	rules, err := loadRules(c.db)
	if err != nil {
		return nil, err
	}
	var out []store.RevertInfo
	for _, r := range rules {
		in := store.RevertInfo{ID: r.id, Origin: r.origin, Undone: r.undone}
		if !r.since.IsZero() {
			in.Since = r.since.UTC().Format(time.RFC3339)
		}
		out = append(out, in)
	}
	return out, nil
}

func (d *DB) RevertStats(chatID string) (map[string]store.RevertReport, error) {
	c, done, err := d.acquire(chatID, "", false)
	defer done()
	if err != nil || c == nil {
		return nil, err
	}
	rows, err := c.db.Query(`SELECT key, value FROM meta WHERE key LIKE 'revert:%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]store.RevertReport{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		var r store.RevertReport
		if _, err := fmt.Sscanf(v, "%d %d", &r.Skipped, &r.SkippedOwn); err == nil {
			out[strings.TrimPrefix(k, metaRevert)] = r
		}
	}
	return out, rows.Err()
}

// saveRevertStats keeps what the reverts left out, per node, to be shown (peer list, db verify).
func (c *chatDB) saveRevertStats(sk *skipper) error {
	return c.inTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM meta WHERE key LIKE 'revert:%'`); err != nil {
			return err
		}
		for origin, cnt := range sk.count {
			if _, err := tx.Exec(`INSERT INTO meta (key, value) VALUES (?, ?)`, metaRevert+origin, fmt.Sprintf("%d %d", cnt.Skipped, cnt.SkippedOwn)); err != nil {
				return err
			}
		}
		return nil
	})
}
