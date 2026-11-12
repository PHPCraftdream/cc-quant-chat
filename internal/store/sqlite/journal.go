package sqlite

import (
	"database/sql"

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
