package main

import (
	"errors"
	"fmt"

	"github.com/PHPCraftdream/cc-quant-chat/internal/local"
)

// runKeys: ccqc keys status. Where the client secret is kept, and what the nicks of this machine
// are protected by (docs/design-keys.md). It reads, it makes nothing.
func runKeys(args []string) error {
	if len(args) != 1 || args[0] != "status" {
		return errors.New("usage: ccqc keys status")
	}
	st, err := local.Info()
	if err != nil {
		return err
	}
	switch {
	case !st.Set:
		fmt.Printf("key store: not set up yet (the first protected join makes the client secret; it will be kept in: %s)\n", st.Planned)
	case st.Present:
		fmt.Printf("key store: %s; the client secret is there\n", st.Kind)
	default:
		fmt.Printf("key store: %s; THE CLIENT SECRET IS GONE: the nicks it protected need new invitations from the owner\n", st.Kind)
	}
	if st.Warning != "" {
		fmt.Println("warning:", st.Warning)
	}
	refs, err := local.NickKeyRefs()
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		fmt.Println("nicks: none in nick-keys.json")
		return nil
	}
	fmt.Println("nicks in nick-keys.json (chat id, nick):")
	for _, r := range refs {
		what := "old secret only"
		if r.Seed {
			what = "private key in the clear: not protected"
			if r.Secret {
				what += ", and the old secret"
			}
		}
		id := r.ChatID
		if len(id) > 8 {
			id = id[:8]
		}
		fmt.Printf("  %s  @%s  %s\n", id, r.Nick, what)
	}
	return nil
}
