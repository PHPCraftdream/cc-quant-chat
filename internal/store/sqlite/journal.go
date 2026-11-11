package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/PHPCraftdream/cc-quant-chat/internal/events"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

const (
	metaJournal    = "journal_version"
	journalVersion = "2"
)

// Version 2 of the journal differs from the first in two things, both made so that events can be
// passed on to other nodes: the hash a node signs holds the four contents of an event as hashes of
// their own (so an event can travel redacted), and the events that say who a nick is (its name and
// key) no longer carry the hash of its secret or its read cursor: those are the state of this node
// and stay out of the journal.

// applyLocalState writes what a record says about this node's own state, which is not in its event:
// the hash of a nick's secret and its read cursor.
func applyLocalState(tx *sql.Tx, r store.Record) error {
	switch r.T {
	case "nick":
		_, err := tx.Exec(`UPDATE nicks SET key_hash = ?, seen = ? WHERE nick = ?`, r.Hash, r.ID, r.Nick)
		return err
	case "nick_key":
		_, err := tx.Exec(`UPDATE nicks SET key_hash = ? WHERE nick = ?`, r.Hash, r.Nick)
		return err
	}
	return nil
}

// upgradeJournal brings the journal of a chat written by the first version to the second: the hash
// and the cursor leave the events of nicks, and the chain of this node (prev, node_sig) is made
// again, since the hash it signs has changed. Only this node's own events are touched; nothing has
// been given to another node yet, so nothing is invalidated. It needs the node key; without it the
// chat stays as it is (verify says so) until the broker opens it.
func (c *chatDB) upgradeJournal(key *nodeKey) error {
	var v string
	err := c.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, metaJournal).Scan(&v)
	if err == nil && v == journalVersion {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if key == nil {
		return nil
	}
	const batch = 2000
	return c.inTx(func(tx *sql.Tx) error {
		prev := ""
		var after int64
		for {
			rows, err := tx.Query(`SELECT `+eventCols+` FROM events WHERE origin = ? AND seq > ? ORDER BY seq LIMIT ?`, key.origin, after, batch)
			if err != nil {
				return err
			}
			var evs []events.Event
			for rows.Next() {
				e, err := scanEvent(c.id, rows)
				if err != nil {
					rows.Close()
					return err
				}
				evs = append(evs, e)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			for _, e := range evs {
				if e.Kind == "nick.register" || e.Kind == "nick.key" {
					var a map[string]any
					if json.Unmarshal([]byte(e.Assigned), &a) == nil {
						delete(a, "hash")
						delete(a, "seen")
						if e.Assigned, err = jsonText(a); err != nil {
							return err
						}
					}
				}
				e.Prev = prev
				e.Sign(key.priv)
				if _, err := tx.Exec(`UPDATE events SET prev = ?, assigned = ?, node_sig = ? WHERE origin = ? AND seq = ?`,
					e.Prev, e.Assigned, e.NodeSig, e.Origin, e.Seq); err != nil {
					return err
				}
				prev, after = e.ID(), e.Seq
			}
			if len(evs) < batch {
				break
			}
		}
		_, err := tx.Exec(`INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)`, metaJournal, journalVersion)
		return err
	})
}
