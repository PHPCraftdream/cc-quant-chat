package sqlite

import (
	"database/sql"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

var _ store.NodeStore = (*DB)(nil)

// RegisterOrigin records this node's identity and gives it to rows written before there was one.
func (d *DB) RegisterOrigin(origin string, pub []byte) error {
	return d.inTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO origins (origin, pubkey, created_at) VALUES (?, ?, ?)`,
			origin, pub, time.Now().UTC().Format(time.RFC3339)); err != nil {
			return err
		}
		for _, table := range []string{"messages", "board_log", "board_comments", "doc_versions"} {
			if _, err := tx.Exec(`UPDATE `+table+` SET origin = ? WHERE origin = ''`, origin); err != nil {
				return err
			}
		}
		return nil
	})
}
