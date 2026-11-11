package sqlite

import (
	"database/sql"
	"errors"

	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

var _ store.Clock = (*DB)(nil)

// Lamport returns the chat's logical clock: the value of the last write (0 before the first).
func (d *DB) Lamport(chatID string) (int64, error) {
	var n int64
	err := d.db.QueryRow(`SELECT lamport FROM chats WHERE id = ?`, chatID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}
