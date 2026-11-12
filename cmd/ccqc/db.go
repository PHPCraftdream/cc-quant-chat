package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/PHPCraftdream/cc-quant-chat/internal/migrate"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store/sqlite"
)

const dbUsage = "usage: ccqc db path|status|migrate|verify|rebuild [--chat <name or id>]"

// runDB: ccqc db path | status | migrate | verify | rebuild.
func runDB(args []string) error {
	if len(args) == 0 {
		return errors.New(dbUsage)
	}
	chat, err := parseChatFlag(args[1:])
	if err != nil {
		return err
	}
	switch args[0] {
	case "path":
		if len(args) != 1 {
			return errors.New(dbUsage)
		}
		fmt.Println(sqlite.DefaultDir())
		return nil
	case "status":
		return dbStatus(chat)
	case "migrate":
		return dbMigrate(chat)
	case "verify":
		return runDBVerify(chat)
	case "rebuild":
		return runDBRebuild(chat)
	}
	return errors.New(dbUsage)
}

var chatFile = regexp.MustCompile(`^[0-9a-f]{64}\.db$`)

// dbMigrate opens the data directory (node.db is migrated by that) and every chat file.
func dbMigrate(chat string) error {
	if err := stopped(); err != nil {
		return err
	}
	db, err := sqlite.Open(sqlite.DefaultPath())
	if err != nil {
		return err
	}
	ids, err := chatsOf(db, chat)
	if err != nil {
		db.Close()
		return err
	}
	for _, id := range ids {
		if err := db.MigrateChat(id); err != nil {
			fmt.Printf("  chat %.8s: %v\n", id, err)
		}
	}
	db.Close()
	return dbStatus(chat)
}

// dbStatus reports the schema state of node.db and of every chat file, without creating or
// changing anything.
func dbStatus(only string) error {
	dir := sqlite.DefaultDir()
	fmt.Println("data directory:", dir)
	if err := fileStatus("node.db", filepath.Join(dir, "node.db"), sqlite.NodeMigrations); err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(dir, "chats"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !chatFile.MatchString(e.Name()) {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".db")
		path := filepath.Join(dir, "chats", e.Name())
		name := chatFileName(path)
		if only != "" && only != name && !strings.HasPrefix(id, only) {
			continue
		}
		n++
		if err := fileStatus(fmt.Sprintf("chat %s (%.8s)", name, id), path, sqlite.ChatMigrations); err != nil {
			return err
		}
	}
	if n == 0 {
		fmt.Println("no chat databases yet")
	}
	return nil
}

// chatFileName reads the name of the chat from its database ("?" when it cannot be read).
func chatFileName(path string) string {
	raw, err := sqlite.OpenRaw(path)
	if err != nil {
		return "?"
	}
	defer raw.Close()
	var name string
	if raw.QueryRow(`SELECT name FROM chat WHERE id = 1`).Scan(&name) != nil || name == "" {
		return "?"
	}
	return name
}

func fileStatus(label, path string, set func() ([]migrate.Migration, error)) error {
	ms, err := set()
	if err != nil {
		return err
	}
	var applied []migrate.Applied
	if _, err := os.Stat(path); err == nil { // a report must not create the database
		raw, err := sqlite.OpenRaw(path)
		if err != nil {
			return err
		}
		applied, err = statusOf(raw)
		raw.Close()
		if err != nil {
			return err
		}
		fmt.Printf("%s: %s\n", label, path)
	} else if errors.Is(err, fs.ErrNotExist) {
		fmt.Printf("%s: %s (not created yet; the broker creates it on first use)\n", label, path)
	} else {
		return err
	}
	for _, a := range applied {
		fmt.Printf("  applied  %04d_%s  %s\n", a.Version, a.Name, a.AppliedAt)
	}
	pending, err := migrate.Check(applied, ms)
	if err != nil {
		fmt.Printf("  ! %v\n", err)
		return nil
	}
	for _, m := range pending {
		fmt.Printf("  pending  %04d_%s\n", m.Version, m.Name)
	}
	if len(pending) == 0 {
		fmt.Println("  up to date")
	}
	return nil
}

func statusOf(raw *sql.DB) ([]migrate.Applied, error) {
	return migrate.Status(context.Background(), raw)
}
