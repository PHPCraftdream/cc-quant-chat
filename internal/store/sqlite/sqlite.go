// Package sqlite: store.Backend on SQLite databases in WAL mode (pure Go driver, no cgo).
//
// The data of a broker is a directory: node.db (what belongs to the node: owner, identity,
// administrator log, broker-wide settings, the index of chats) and chats/<chat id>.db, one
// database per chat with its journal of events and everything built from it. A chat is opened
// when it is first used and closed when the broker lets it go. Schemas are created and upgraded
// by the embedded migrations: one set for the node, one for a chat. docs/design-storage.md.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // registers driver "sqlite"

	"github.com/PHPCraftdream/cc-quant-chat/internal/migrate"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

//go:embed migrations_node/*.sql migrations_chat/*.sql
var migrationFS embed.FS

// NodeMigrations: the schema of node.db.
func NodeMigrations() ([]migrate.Migration, error) {
	return migrate.Load(migrationFS, "migrations_node")
}

// ChatMigrations: the schema of a chat's database.
func ChatMigrations() ([]migrate.Migration, error) {
	return migrate.Load(migrationFS, "migrations_chat")
}

// DefaultPath: CCQC_DB or <user config dir>/ccqc/ccqc.db. Without a config dir it falls back to
// ~/.ccqc/ccqc.db, never to a shared temp folder; "" when no private location exists (Open then
// asks for CCQC_DB). Its directory is the data directory.
func DefaultPath() string {
	if p := os.Getenv("CCQC_DB"); p != "" {
		return p
	}
	if base, err := os.UserConfigDir(); err == nil {
		return filepath.Join(base, "ccqc", "ccqc.db")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".ccqc", "ccqc.db")
	}
	return ""
}

// DefaultDir is the data directory: where node.db and chats/ live.
func DefaultDir() string {
	if p := DefaultPath(); p != "" {
		return filepath.Dir(p)
	}
	return ""
}

// DB is the store of a broker: node.db and the chat databases it has open.
type DB struct {
	dir  string
	node *sql.DB

	mu     sync.Mutex
	closed bool // Close was called: no chat file is opened or made any more (a late write must not bring one back)
	chats  map[string]*chatDB
	key    *nodeKey // signs events; see SetNode

	onEvents atomic.Pointer[func(chatID string)] // see OnEvents
	pending  map[*chatDB]struct{}                // databases of released chats that are waiting to be closed (under mu)
}

var (
	_ store.Backend = (*DB)(nil)
	_ store.Journal = (*DB)(nil)
)

// OpenRaw opens one database file without migrating (reports, tests). The file is created private.
func OpenRaw(path string) (*sql.DB, error) {
	if path == "" {
		return nil, errors.New("no private place for the database: set CCQC_DB")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// Create the file private before SQLite does; its -wal and -shm files inherit the mode.
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600); err != nil {
		return nil, err
	} else {
		f.Close()
	}
	q := url.Values{}
	for _, p := range []string{"journal_mode(WAL)", "synchronous(NORMAL)", "foreign_keys(1)", "busy_timeout(5000)"} {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", "immediate") // writers take the lock at BEGIN: no upgrade deadlocks
	db, err := sql.Open("sqlite", "file:"+(&url.URL{Path: filepath.ToSlash(path)}).EscapedPath()+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer per database
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return db, nil
}

// openMigrated opens a database file and brings its schema up to date.
func openMigrated(path string, ms func() ([]migrate.Migration, error)) (*sql.DB, error) {
	db, err := OpenRaw(path)
	if err != nil {
		return nil, err
	}
	set, err := ms()
	if err == nil {
		_, err = migrate.Up(context.Background(), db, set)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Open opens the data directory of the broker: node.db and chats/ are beside path (only its folder is used).
func Open(path string) (*DB, error) {
	if path == "" {
		return nil, errors.New("no private place for the database: set CCQC_DB")
	}
	dir := filepath.Dir(path)
	for _, d := range []string{dir, filepath.Join(dir, "chats")} {
		_, statErr := os.Stat(d)
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
		// A directory this store makes is private. One that was there (CCQC_DB may name a file in
		// any folder) is left as it is, except chats/, which is ours.
		if statErr != nil || filepath.Base(d) == "chats" {
			_ = os.Chmod(d, 0o700)
		}
	}
	node, err := openMigrated(filepath.Join(dir, "node.db"), NodeMigrations)
	if err != nil {
		return nil, err
	}
	d := &DB{dir: dir, node: node, chats: map[string]*chatDB{}}
	if err := d.adoptStrays(); err != nil {
		node.Close()
		return nil, err
	}
	return d, nil
}

// Dir is the data directory.
func (d *DB) Dir() string { return d.dir }

func (d *DB) chatPath(id string) string { return filepath.Join(d.dir, "chats", id+".db") }

var chatFileRe = regexp.MustCompile(`^[0-9a-f]{64}\.db$`)

// adoptStrays lists a chat file the index does not know (the index is a convenience; the file
// is the chat): its name is read from the file itself.
func (d *DB) adoptStrays() error {
	entries, err := os.ReadDir(filepath.Join(d.dir, "chats"))
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !chatFileRe.MatchString(e.Name()) {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".db")
		var one int
		switch err := d.node.QueryRow(`SELECT 1 FROM chats WHERE chat_id = ?`, id).Scan(&one); {
		case err == nil:
			continue
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		raw, err := OpenRaw(filepath.Join(d.dir, "chats", e.Name()))
		if err != nil {
			log.Printf("sqlite: chat file %s: %v", e.Name(), err)
			continue
		}
		var name, created string
		err = raw.QueryRow(`SELECT name, created_at FROM chat WHERE id = 1 AND chat_id = ?`, id).Scan(&name, &created)
		raw.Close()
		if err != nil {
			log.Printf("sqlite: chat file %s is not a chat database of that chat: %v", e.Name(), err)
			continue
		}
		if _, err := d.node.Exec(`INSERT OR IGNORE INTO chats (chat_id, name, file, created_at) VALUES (?, ?, ?, ?)`, id, name, e.Name(), created); err != nil {
			return err
		}
	}
	return nil
}

// Close closes the chats that are open and node.db.
func (d *DB) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	var first error
	shut := func(c *chatDB) {
		c.life.Lock()
		defer c.life.Unlock()
		if c.closed {
			return
		}
		c.closed = true
		if err := c.db.Close(); err != nil && first == nil {
			first = err
		}
	}
	for c := range d.pending { // released, waiting for their readers: they are waited for here
		shut(c)
		delete(d.pending, c)
	}
	for id, c := range d.chats {
		shut(c)
		delete(d.chats, id)
	}
	if err := d.node.Close(); err != nil && first == nil {
		first = err
	}
	return first
}

// chat returns the open database of a chat, opening it (creating it when create is set and it does
// not exist yet). nil, nil: there is no such chat.
func (d *DB) chat(id, name string, create bool) (*chatDB, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c := d.chats[id]; c != nil {
		return c, nil
	}
	if d.closed {
		return nil, errors.New("sqlite: the store is closed")
	}
	if len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" {
		return nil, fmt.Errorf("sqlite: %q is not a chat id", id)
	}
	path := d.chatPath(id)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) && !create {
		return nil, nil
	}
	c, err := d.openChat(id, name, path)
	if err != nil {
		return nil, err
	}
	d.chats[id] = c
	return c, nil
}

// acquire is chat for a caller that goes on to use the database: it is not closed (by Release)
// until the caller has called done. A chat that does not exist comes back as nil with a done that
// does nothing.
func (d *DB) acquire(id, name string, create bool) (*chatDB, func(), error) {
	for {
		c, err := d.chat(id, name, create)
		if err != nil || c == nil {
			return nil, func() {}, err
		}
		c.life.RLock()
		if !c.closed {
			return c, c.life.RUnlock, nil
		}
		c.life.RUnlock() // released between the lookup and the lock: look again (it opens afresh)
	}
}

// Release lets go of the database of a chat the broker no longer keeps in memory. The chat is out
// of the map at once; the database is closed by a goroutine as soon as those who are using it (the
// sessions with other nodes read without the broker's lock) are done, so that a long read holds up
// nobody: the caller holds the broker's lock.
func (d *DB) Release(chatID string) {
	d.mu.Lock()
	c := d.chats[chatID]
	delete(d.chats, chatID)
	if c != nil {
		if d.pending == nil {
			d.pending = map[*chatDB]struct{}{}
		}
		d.pending[c] = struct{}{}
	}
	d.mu.Unlock()
	if c == nil {
		return
	}
	go func() {
		c.life.Lock()
		if !c.closed {
			c.closed = true
			_ = c.db.Close()
		}
		c.life.Unlock()
		d.mu.Lock()
		delete(d.pending, c)
		d.mu.Unlock()
	}()
}

// Load returns a chat's state as records in replay order: rooms, nicks, memberships, messages.
// The chat is created on first use.
func (d *DB) Load(chatID, name string) ([]store.Record, error) {
	c, done, err := d.acquire(chatID, name, true)
	defer done()
	if err != nil {
		return nil, err
	}
	return c.load()
}

// Append applies one record to the chat it belongs to.
func (d *DB) Append(chatID string, r store.Record) error {
	c, done, err := d.acquire(chatID, "", true)
	defer done()
	if err != nil {
		return err
	}
	return c.append(r)
}

// ChatIDs lists the chats of this node (the index of node.db).
func (d *DB) ChatIDs() ([]string, error) {
	rows, err := d.node.Query(`SELECT chat_id FROM chats ORDER BY chat_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ResolveChat turns an id, a unique prefix of one or a chat's name into the id of a chat.
func (d *DB) ResolveChat(idOrName string) (string, error) {
	rows, err := d.node.Query(`SELECT chat_id, name FROM chats ORDER BY chat_id`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return "", err
		}
		if id == idOrName {
			return id, nil
		}
		if name == idOrName || (len(idOrName) >= 8 && strings.HasPrefix(id, idOrName)) {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	switch len(ids) {
	case 1:
		return ids[0], nil
	case 0:
		return "", fmt.Errorf("no chat %q", idOrName)
	}
	return "", fmt.Errorf("%q fits %d chats; give more of the id", idOrName, len(ids))
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }
