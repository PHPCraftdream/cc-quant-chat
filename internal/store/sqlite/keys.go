package sqlite

import (
	"database/sql"
	"errors"

	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

var _ store.NickSecrets = (*DB)(nil)

const secretCols = `chat_id, nick, state, b, auth_pub, sign_pub, sid_hash, created_at, unlocked_at, fails, fail_until`

type rowScanner interface{ Scan(dest ...any) error }

func scanSecret(r rowScanner) (store.NickSecret, error) {
	var s store.NickSecret
	err := r.Scan(&s.ChatID, &s.Nick, &s.State, &s.B, &s.AuthPub, &s.SignPub, &s.SIDHash, &s.CreatedAt, &s.UnlockedAt, &s.Fails, &s.FailUntil)
	return s, err
}

func (d *DB) PutPendingNickSecret(s store.NickSecret) error {
	_, err := d.node.Exec(`INSERT OR REPLACE INTO nick_secrets (`+secretCols+`) VALUES (?, ?, 'pending', ?, ?, ?, ?, ?, 0, 0, 0)`,
		s.ChatID, s.Nick, s.B, s.AuthPub, s.SignPub, s.SIDHash, s.CreatedAt)
	return err
}

func (d *DB) ActivateNickSecret(chatID, nick string) (bool, error) {
	tx, err := d.node.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM nick_secrets WHERE chat_id = ? AND nick = ? AND state = 'pending'`, chatID, nick).Scan(&n); err != nil || n == 0 {
		return false, err
	}
	if _, err := tx.Exec(`DELETE FROM nick_secrets WHERE chat_id = ? AND nick = ? AND state = 'active'`, chatID, nick); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`UPDATE nick_secrets SET state = 'active' WHERE chat_id = ? AND nick = ? AND state = 'pending'`, chatID, nick); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (d *DB) DropPendingNickSecret(chatID, nick string) error {
	_, err := d.node.Exec(`DELETE FROM nick_secrets WHERE chat_id = ? AND nick = ? AND state = 'pending'`, chatID, nick)
	return err
}

func (d *DB) ActiveNickSecret(chatID, nick string) (*store.NickSecret, error) {
	s, err := scanSecret(d.node.QueryRow(`SELECT `+secretCols+` FROM nick_secrets WHERE chat_id = ? AND nick = ? AND state = 'active'`, chatID, nick))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (d *DB) PendingNickSecrets() ([]store.NickSecret, error) {
	rows, err := d.node.Query(`SELECT ` + secretCols + ` FROM nick_secrets WHERE state = 'pending' ORDER BY chat_id, nick`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.NickSecret
	for rows.Next() {
		s, err := scanSecret(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *DB) DeleteNickSecrets(chatID, nick string) (bool, error) {
	res, err := d.node.Exec(`DELETE FROM nick_secrets WHERE chat_id = ? AND nick = ?`, chatID, nick)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (d *DB) NickSecretUnlocked(chatID, nick string, at int64) error {
	_, err := d.node.Exec(`UPDATE nick_secrets SET unlocked_at = ?, fails = 0, fail_until = 0
		WHERE chat_id = ? AND nick = ? AND state = 'active'`, at, chatID, nick)
	return err
}

func (d *DB) NickSecretFailed(chatID, nick string, fails int, until int64) error {
	_, err := d.node.Exec(`UPDATE nick_secrets SET fails = ?, fail_until = ?
		WHERE chat_id = ? AND nick = ? AND state = 'active'`, fails, until, chatID, nick)
	return err
}
