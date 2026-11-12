package main

import (
	"fmt"

	"github.com/PHPCraftdream/cc-quant-chat/internal/client"
	"github.com/PHPCraftdream/cc-quant-chat/internal/local"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// runKeysMigrate moves the nicks of this machine that hold a plain key (or an old secret) to protected
// keys (docs/design-keys.md section 9). Each nick joins its chat and leaves; a nick that is online moves by
// itself at its next join. Copies of nick-keys.json, in backups or elsewhere, are dead once a nick moved.
func runKeysMigrate() error {
	refs, err := local.NickKeyRefs()
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		fmt.Println("nothing to move: nick-keys.json holds no nick key")
		return nil
	}
	moved, failed := 0, 0
	for _, r := range refs {
		short := r.ChatID
		if len(short) > 8 {
			short = short[:8]
		}
		token := ""
		if !r.Seed { // only an old secret: a chat of tokens, joined by its token
			t, ok := local.TokenOfChat(r.ChatID)
			if !ok {
				fmt.Printf("  %s  @%s  skipped: it has only an old secret and no session of yours remembers the chat's token; it moves at the next join of its session\n", short, r.Nick)
				continue
			}
			token = t
		}
		sid := local.NickSession(r.ChatID, r.Nick) // the session that uses the nick: the broker binds it there
		c, err := client.Connect()
		if err != nil {
			return err
		}
		var w proto.Welcome
		kj, err := client.JoinWithKeys(c.Call, client.KeyJoinInput{ChatID: r.ChatID, Token: token, Nick: r.Nick, SID: sid, IDs: client.SessionIDs{SID: sid}}, &w)
		if err == nil {
			_ = c.Call("leave", nil, nil)
		}
		c.Close()
		switch {
		case err != nil && proto.IsCode(err, "name_online"):
			fmt.Printf("  %s  @%s  online now: it moves by itself at its next join\n", short, r.Nick)
		case err != nil:
			failed++
			fmt.Printf("  %s  @%s  NOT moved: %v\n", short, r.Nick, err)
		case !kj.Protected:
			failed++
			fmt.Printf("  %s  @%s  stays plain: %s\n", short, r.Nick, firstNoteLine(kj.Note))
		default:
			moved++
			if sid != "" {
				if err := local.BindNick(r.ChatID, r.Nick); err != nil {
					fmt.Printf("warning: the binding of @%s to its session could not be recorded (%v)\n", r.Nick, err)
				}
			}
			fmt.Printf("  %s  @%s  moved to a protected key\n", short, r.Nick)
		}
	}
	fmt.Printf("%d moved, %d not moved. Delete old backup folders that hold nick-keys.json: the keys in them no longer open the nicks that moved.\n", moved, failed)
	return nil
}

func firstNoteLine(note string) string {
	for i, r := range note {
		if r == '\n' {
			return note[:i]
		}
	}
	return note
}
