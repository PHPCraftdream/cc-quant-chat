package sqlite

import (
	"crypto/ed25519"
	"database/sql"
	"fmt"

	"github.com/PHPCraftdream/cc-quant-chat/internal/events"
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
	Chats  int
	Events int
	Signed int // events with an author's signature that verified
	Issues []Issue
}

// Verify checks the journal of one chat (all chats when chatID is ""): every origin's chain has
// no gaps and each event names its predecessor (prev), the node's signature verifies against the
// key recorded for the origin, the author's signature verifies, and the chat's clock is not
// behind its events. Nothing is changed.
func (d *DB) Verify(chatID string) (VerifyReport, error) {
	var rep VerifyReport
	chats, err := d.chatIDs(chatID)
	if err != nil {
		return rep, err
	}
	keys, err := d.originKeys()
	if err != nil {
		return rep, err
	}
	for _, c := range chats {
		rep.Chats++
		if err := d.verifyChat(c[0], keys, &rep); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

func (d *DB) originKeys() (map[string]ed25519.PublicKey, error) {
	rows, err := d.db.Query(`SELECT origin, pubkey FROM origins`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := map[string]ed25519.PublicKey{}
	for rows.Next() {
		var o string
		var pub []byte
		if err := rows.Scan(&o, &pub); err != nil {
			return nil, err
		}
		keys[o] = ed25519.PublicKey(pub)
	}
	return keys, rows.Err()
}

func (d *DB) verifyChat(chatID string, keys map[string]ed25519.PublicKey, rep *VerifyReport) error {
	rows, err := d.db.Query(`SELECT origin, seq, prev, lamport, kind, payload, author_pub, sig, assigned, ts, node_sig FROM events
		WHERE chat_id = ? ORDER BY origin, seq`, chatID)
	if err != nil {
		return err
	}
	var maxLamport int64
	chatEvents := 0
	lastOrigin, lastSeq, lastID := "", int64(0), ""
	for rows.Next() {
		e := events.Event{ChatID: chatID}
		if err := rows.Scan(&e.Origin, &e.Seq, &e.Prev, &e.Lamport, &e.Kind, &e.Payload, &e.AuthorPub, &e.Sig, &e.Assigned, &e.TS, &e.NodeSig); err != nil {
			rows.Close()
			return err
		}
		rep.Events++
		chatEvents++
		bad := func(what string) {
			rep.Issues = append(rep.Issues, Issue{Chat: chatID, Origin: e.Origin, Seq: e.Seq, What: what})
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
		case !e.VerifyNode(pub):
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
	if clock, err := d.chatClockNow(chatID); err != nil {
		return err
	} else if chatEvents > 0 && clock < maxLamport {
		rep.Issues = append(rep.Issues, Issue{Chat: chatID, What: fmt.Sprintf("the chat's clock %d is behind its events (%d)", clock, maxLamport)})
	} else if chatEvents > 0 && clock > maxLamport {
		rep.Issues = append(rep.Issues, Issue{Chat: chatID, What: fmt.Sprintf("the chat's clock %d is ahead of its events (%d): rows were written without events (an older broker?); do not rebuild", clock, maxLamport)})
	}
	return nil
}

func (d *DB) chatClockNow(chatID string) (int64, error) {
	var n int64
	err := d.db.QueryRow(`SELECT lamport FROM chats WHERE id = ?`, chatID).Scan(&n)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return n, err
}
