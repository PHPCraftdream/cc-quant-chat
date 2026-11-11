package sqlite

import (
	"crypto/ed25519"
	"database/sql"
	"errors"
	"fmt"
)

// Issue is one thing Verify found wrong.
type Issue struct {
	Chat   string
	Origin string
	Seq    int64
	What   string
}

func (i Issue) String() string {
	short := i.Chat
	if len(short) > 8 {
		short = short[:8]
	}
	if i.Seq == 0 {
		return fmt.Sprintf("chat %s: %s", short, i.What)
	}
	return fmt.Sprintf("chat %s, event %s#%d: %s", short, i.Origin, i.Seq, i.What)
}

// VerifyReport: what Verify read and what it found.
type VerifyReport struct {
	Events int
	Signed int // events with an author's signature that verified
	Issues []Issue
}

// Verify checks the journal of a chat: every origin's chain has no gaps and each event names its
// predecessor (prev), the node's signature verifies against the key recorded for the origin, the
// author's signature verifies, and the chat's clock is neither behind nor ahead of its events.
// Nothing is changed.
func (d *DB) Verify(chatID string) (VerifyReport, error) {
	var rep VerifyReport
	c, err := d.chat(chatID, "", false)
	if err != nil {
		return rep, err
	}
	if c == nil {
		return rep, fmt.Errorf("no chat %.8s", chatID)
	}
	keys, err := d.originKeys()
	if err != nil {
		return rep, err
	}
	return rep, c.verify(keys, &rep)
}

func (c *chatDB) verify(keys map[string][]byte, rep *VerifyReport) error {
	var version string
	if err := c.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, metaJournal).Scan(&version); err != nil || version != journalVersion {
		rep.Issues = append(rep.Issues, Issue{Chat: c.id, What: "the journal is in the format of an earlier version: start the broker once, it brings it up to date"})
		return nil
	}
	rows, err := c.db.Query(`SELECT ` + eventCols + ` FROM events ORDER BY origin, seq`)
	if err != nil {
		return err
	}
	var maxLamport int64
	chatEvents := 0
	lastOrigin, lastSeq, lastID := "", int64(0), ""
	for rows.Next() {
		e, err := scanEvent(c.id, rows)
		if err != nil {
			rows.Close()
			return err
		}
		rep.Events++
		chatEvents++
		bad := func(what string) {
			rep.Issues = append(rep.Issues, Issue{Chat: c.id, Origin: e.Origin, Seq: e.Seq, What: what})
		}
		if e.Origin != lastOrigin {
			lastOrigin, lastSeq, lastID = e.Origin, 0, ""
		}
		switch {
		case e.Seq != lastSeq+1:
			bad(fmt.Sprintf("the chain has a gap: seq %d follows %d", e.Seq, lastSeq))
		case e.Prev != lastID:
			bad("the chain is broken: prev is not the previous event")
		}
		lastSeq, lastID = e.Seq, e.ID()
		maxLamport = max(maxLamport, e.Lamport)
		pub, known := keys[e.Origin]
		switch {
		case !known:
			bad("no key is recorded for the origin of this event")
		case e.NodeSig == "":
			bad("the node did not sign this event")
		case !e.VerifyNode(ed25519.PublicKey(pub)):
			bad("the node's signature does not verify")
		}
		if ok, signed := e.VerifyAuthor(); !ok {
			bad("the author's signature does not verify")
		} else if signed {
			rep.Signed++
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var clock int64
	switch err := c.db.QueryRow(`SELECT lamport FROM chat WHERE id = 1`).Scan(&clock); {
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return err
	case chatEvents > 0 && clock < maxLamport:
		rep.Issues = append(rep.Issues, Issue{Chat: c.id, What: fmt.Sprintf("the chat's clock %d is behind its events (%d)", clock, maxLamport)})
	case chatEvents > 0 && clock > maxLamport:
		rep.Issues = append(rep.Issues, Issue{Chat: c.id, What: fmt.Sprintf("the chat's clock %d is ahead of its events (%d): rows were written without events (an older broker?); do not rebuild", clock, maxLamport)})
	}
	return nil
}
