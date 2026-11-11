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

// ccqc db verify | rebuild [--chat <name or id>]: the journal of events and the projections built
// from it, per chat. Without --chat every chat of the node is done. Both work on the chat files
// with the broker stopped.

func parseChatFlag(args []string) (string, error) {
	switch {
	case len(args) == 0:
		return "", nil
	case len(args) == 2 && args[0] == "--chat" && args[1] != "":
		return args[1], nil
	}
	return "", errors.New(dbUsage)
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

// chatsOf: the ids of the chats to work on: the one named, or all.
func chatsOf(db *sqlite.DB, chat string) ([]string, error) {
	if chat != "" {
		id, err := db.ResolveChat(chat)
		return []string{id}, err
	}
	return db.ChatIDs()
}

func openJournal(chat string) (*sqlite.DB, []string, error) {
	db, err := sqlite.Open(sqlite.DefaultPath())
	if err != nil {
		return nil, nil, err
	}
	ids, err := chatsOf(db, chat)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return db, ids, nil
}

func runDBVerify(chat string) error {
	if err := stopped(); err != nil {
		return err
	}
	db, ids, err := openJournal(chat)
	if err != nil {
		return err
	}
	defer db.Close()
	events, signed, problems := 0, 0, 0
	for _, id := range ids {
		rep, err := db.Verify(id)
		if err != nil {
			fmt.Printf("chat %.8s: %v\n", id, err)
			problems++
			continue
		}
		for _, i := range rep.Issues {
			fmt.Println(i)
		}
		events, signed, problems = events+rep.Events, signed+rep.Signed, problems+len(rep.Issues)
	}
	fmt.Printf("checked %d event(s) in %d chat(s); %d carry a verified author's signature; %d problem(s)\n", events, len(ids), signed, problems)
	if problems > 0 {
		return fmt.Errorf("the journal does not verify")
	}
	return nil
}

func runDBRebuild(chat string) error {
	if err := stopped(); err != nil {
		return err
	}
	db, ids, err := openJournal(chat)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := confirmOwner(db); err != nil {
		return err
	}
	failed := 0
	for _, id := range ids {
		rep, err := db.Rebuild(id, false)
		if err != nil {
			fmt.Printf("chat %.8s: %v\n", id, err)
			failed++
			continue
		}
		fmt.Printf("chat %.8s: rebuilt from %d event(s)", id, rep.Events)
		if rep.Total() == 0 {
			fmt.Println("; the projections already match the journal, nothing was changed")
			continue
		}
		fmt.Println()
		var tables []string
		for t := range rep.Diffs {
			tables = append(tables, t)
		}
		sort.Strings(tables)
		for _, t := range tables {
			fmt.Printf("  %-16s %d differing row(s)\n", t, rep.Diffs[t])
		}
		if rep.Replaced {
			fmt.Println("  the projections were replaced by the rebuilt ones")
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d chat(s) could not be rebuilt", failed)
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
