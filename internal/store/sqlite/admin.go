package sqlite

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

var (
	_ store.AdminStore        = (*DB)(nil)
	_ store.AttestedSettings  = (*DB)(nil)
	_ store.ChatSettingsStore = (*DB)(nil)
)

// Settings returns the broker-wide settings as scope "" → key → value. The settings of a chat are
// in its own database (ChatSettings).
func (d *DB) Settings() (map[string]map[string]string, error) {
	rows, err := d.node.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]string{"": {}}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		out[""][key] = value
	}
	return out, rows.Err()
}

// ChatSettings returns the settings of one chat.
func (d *DB) ChatSettings(chatID string) (map[string]string, error) {
	c, done, err := d.acquire(chatID, "", false)
	defer done()
	if err != nil || c == nil {
		return map[string]string{}, err
	}
	out := map[string]string{}
	err = c.each(`SELECT key, value FROM settings`, nil, func(rows *sql.Rows) error {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		out[k] = v
		return nil
	})
	return out, err
}

// SetSetting stores a value, replacing an earlier one. A setting of a chat (scope is its id) is an
// event of that chat.
func (d *DB) SetSetting(scope, key, value string) error {
	if scope == "" {
		_, err := d.node.Exec(`INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)`, key, value)
		return err
	}
	return d.setChatSetting(scope, key, value, nil)
}

// SetChatSettingAttested is SetSetting for a setting changed by a signed administrator operation:
// the event carries the owner's signature.
func (d *DB) SetChatSettingAttested(chatID, key, value string, a store.AdminAttest) error {
	return d.setChatSetting(chatID, key, value, &a)
}

func (d *DB) setChatSetting(chatID, key, value string, a *store.AdminAttest) error {
	c, done, err := d.acquire(chatID, "", false)
	defer done()
	if err != nil {
		return err
	}
	if c == nil {
		return fmt.Errorf("sqlite: no chat %.8s for the setting %s", chatID, key)
	}
	return c.chatSetting(key, value, a)
}

// LogAdmin appends an administrator attempt to the log.
func (d *DB) LogAdmin(e store.AdminEntry) error {
	_, err := d.node.Exec(`INSERT INTO admin_log (ts, op, args, result, origin) VALUES (?, ?, ?, ?, ?)`, e.TS, e.Op, e.Args, e.Result, e.Origin)
	return err
}

var errNoChat = errors.New("no such chat")
