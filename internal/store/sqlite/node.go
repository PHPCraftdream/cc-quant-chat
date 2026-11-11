package sqlite

import (
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

var _ store.NodeStore = (*DB)(nil)

// RegisterOrigin records this node's identity (origin and public key) in node.db. Rows that
// predate it get it when a database of earlier versions is laid out into chat files.
func (d *DB) RegisterOrigin(origin string, pub []byte) error {
	_, err := d.node.Exec(`INSERT OR IGNORE INTO origins (origin, pubkey, created_at) VALUES (?, ?, ?)`, origin, pub, now())
	return err
}

// originKeys: the public key recorded for every origin this node knows.
func (d *DB) originKeys() (map[string][]byte, error) {
	rows, err := d.node.Query(`SELECT origin, pubkey FROM origins`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := map[string][]byte{}
	for rows.Next() {
		var o string
		var pub []byte
		if err := rows.Scan(&o, &pub); err != nil {
			return nil, err
		}
		keys[o] = pub
	}
	return keys, rows.Err()
}
