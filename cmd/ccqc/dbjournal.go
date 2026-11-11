package main

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/PHPCraftdream/cc-quant-chat/internal/client"
	"github.com/PHPCraftdream/cc-quant-chat/internal/local"
	"github.com/PHPCraftdream/cc-quant-chat/internal/owner"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store/sqlite"
)

// ccqc db verify | rebuild [--chat <id or its start>]: the journal of events and the
// projections built from it. Both work on the database file with the broker stopped.

func parseChatFlag(args []string) (string, error) {
	switch {
	case len(args) == 0:
		return "", nil
	case len(args) == 2 && args[0] == "--chat" && args[1] != "":
		return args[1], nil
	}
	return "", errors.New("usage: ccqc db verify|rebuild [--chat <id>]")
}

// stopped refuses to go on while a broker answers: it keeps chats in memory that a rebuild
// would not know about.
func stopped() error {
	if c, err := client.Dial(); err == nil {
		c.Close()
		return errors.New("the broker is running: stop it first (`ccqc broker stop`), then run this again")
	}
	return nil
}

func openJournal(chat string) (*sqlite.DB, string, error) {
	db, err := sqlite.Open(sqlite.DefaultPath())
	if err != nil {
		return nil, "", err
	}
	if chat != "" {
		if chat, err = db.ResolveChat(chat); err != nil {
			db.Close()
			return nil, "", err
		}
	}
	return db, chat, nil
}

func runDBVerify(args []string) error {
	chat, err := parseChatFlag(args)
	if err != nil {
		return err
	}
	if err := stopped(); err != nil {
		return err
	}
	db, chat, err := openJournal(chat)
	if err != nil {
		return err
	}
	defer db.Close()
	rep, err := db.Verify(chat)
	if err != nil {
		return err
	}
	for _, i := range rep.Issues {
		fmt.Println(i)
	}
	fmt.Printf("checked %d event(s) in %d chat(s); %d carry a verified author's signature; %d problem(s)\n", rep.Events, rep.Chats, rep.Signed, len(rep.Issues))
	if len(rep.Issues) > 0 {
		return fmt.Errorf("the journal does not verify")
	}
	return nil
}

func runDBRebuild(args []string) error {
	chat, err := parseChatFlag(args)
	if err != nil {
		return err
	}
	if err := stopped(); err != nil {
		return err
	}
	db, chat, err := openJournal(chat)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := confirmOwner(db); err != nil {
		return err
	}
	rep, err := db.Rebuild(chat, false)
	if err != nil {
		return err
	}
	fmt.Printf("rebuilt %d chat(s) from %d event(s)\n", rep.Chats, rep.Events)
	if rep.Total() == 0 {
		fmt.Println("the projections already match the journal; nothing was changed")
		return nil
	}
	var tables []string
	for t := range rep.Diffs {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	for _, t := range tables {
		fmt.Printf("  %-16s %d differing row(s)\n", t, rep.Diffs[t])
	}
	if rep.Replaced {
		fmt.Println("the projections were replaced by the rebuilt ones")
	}
	return nil
}

// confirmOwner: with an owner registered, a rebuild is an administrator operation: it asks for
// the owner's password at the console, as `ccqc owner` and `ccqc role` do.
func confirmOwner(db *sqlite.DB) error {
	pub, _, found, err := db.Owner()
	if err != nil || !found {
		return err
	}
	f, err := owner.Load(local.OwnerKeyPath())
	if err != nil {
		return fmt.Errorf("an owner is registered but its key file is missing (%s): %w", local.OwnerKeyPath(), err)
	}
	pw, err := owner.ReadSecret("Owner password: ")
	if err != nil {
		return err
	}
	priv, err := f.Open(pw)
	if err != nil {
		return err
	}
	if !bytes.Equal(priv.Public().(ed25519.PublicKey), pub) {
		return errors.New("this key file is not the key of the registered owner")
	}
	fmt.Fprintln(os.Stderr, "owner confirmed")
	return nil
}
