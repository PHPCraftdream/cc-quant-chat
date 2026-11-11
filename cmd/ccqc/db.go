package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/PHPCraftdream/cc-quant-chat/internal/migrate"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store/sqlite"
)

// runDB: ccqc db path | status | migrate.
func runDB(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: ccqc db path|status|migrate")
	}
	path := sqlite.DefaultPath()
	switch args[0] {
	case "path":
		fmt.Println(path)
		return nil
	case "status":
		return dbStatus(path)
	case "migrate":
		db, err := sqlite.Open(path)
		if err != nil {
			return err
		}
		defer db.Close()
		return dbStatus(path)
	}
	return errors.New("usage: ccqc db path|status|migrate")
}

func dbStatus(path string) error {
	ms, err := sqlite.Migrations()
	if err != nil {
		return err
	}
	var applied []migrate.Applied
	if _, err := os.Stat(path); err == nil { // a report must not create the database
		db, err := sqlite.OpenRaw(path)
		if err != nil {
			return err
		}
		defer db.Close()
		if applied, err = migrate.Status(context.Background(), db); err != nil {
			return err
		}
		fmt.Println("database:", path)
	} else if errors.Is(err, fs.ErrNotExist) {
		fmt.Println("database:", path, "(not created yet; the broker creates it on first use)")
	} else {
		return err
	}
	for _, a := range applied {
		fmt.Printf("  applied  %04d_%s  %s\n", a.Version, a.Name, a.AppliedAt)
	}
	pending, err := migrate.Check(applied, ms)
	if err != nil {
		return err
	}
	for _, m := range pending {
		fmt.Printf("  pending  %04d_%s\n", m.Version, m.Name)
	}
	if len(pending) == 0 {
		fmt.Println("  up to date")
	}
	return nil
}
