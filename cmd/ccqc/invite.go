package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/client"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// ccqc chat new <name>; ccqc invite <chat> <nick> [--ttl 15m] | list <chat> | revoke <chat> <id>.
// Administrator operations (the owner's password is typed on the console).

const inviteUsage = "usage: ccqc chat new <name> | ccqc invite <chat> <nick> [--ttl 15m|2h|7d] | invite list <chat> | invite revoke <chat> <id>"

func runChatCmd(args []string) error {
	if len(args) != 2 || args[0] != "new" {
		return errors.New("usage: ccqc chat new <name>")
	}
	c, err := client.Connect()
	if err != nil {
		return err
	}
	defer c.Close()
	text, err := adminDo(c, "chat_new", proto.AdminChatNew{Name: args[1]})
	if err != nil {
		return err
	}
	fmt.Println(text)
	return nil
}

// parseTTL reads 90s, 15m, 2h, 7d.
func parseTTL(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("--ttl %q: like 15m, 2h or 7d", s)
		}
		return int64(n) * 86400, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("--ttl %q: like 15m, 2h or 7d", s)
	}
	return int64(d / time.Second), nil
}

// findChat gives the id of a chat by what the person typed: its id, or its name (the broker is
// asked; several chats of one name are listed, never picked from silently).
func findChat(c *client.Conn, what string) (string, error) {
	if len(what) == 64 && strings.Trim(what, "0123456789abcdef") == "" {
		return what, nil
	}
	name := strings.Trim(what, "/")
	var r proto.ChatFindReply
	if err := c.Call("chat_find", proto.ChatFindArgs{Name: name}, &r); err != nil {
		if proto.IsCode(err, "unknown_op") {
			return "", errors.New("the running broker is older and has no invitations: run `ccqc broker restart`")
		}
		return "", err
	}
	switch len(r.Chats) {
	case 0:
		return "", fmt.Errorf("this node has no chat named %q", name)
	case 1:
		return r.Chats[0].ID, nil
	}
	var sb strings.Builder
	for _, x := range r.Chats {
		fmt.Fprintf(&sb, "\n  %s", x.ID)
	}
	return "", fmt.Errorf("several chats are named %q; give the id:%s", name, sb.String())
}

func runInvite(args []string) error {
	var rest []string
	ttl := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--ttl" && i+1 < len(args) {
			ttl = args[i+1]
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	if len(rest) == 0 {
		return errors.New(inviteUsage)
	}
	c, err := client.Connect()
	if err != nil {
		return err
	}
	defer c.Close()
	switch {
	case rest[0] == "list" && len(rest) == 2:
		id, err := findChat(c, rest[1])
		if err != nil {
			return err
		}
		return printAdmin(c, "invite_list", proto.AdminInviteID{Chat: id})
	case rest[0] == "revoke" && len(rest) == 3:
		id, err := findChat(c, rest[1])
		if err != nil {
			return err
		}
		return printAdmin(c, "invite_revoke", proto.AdminInviteID{Chat: id, ID: rest[2]})
	case len(rest) == 2:
		secs, err := parseTTL(ttl)
		if err != nil {
			return err
		}
		id, err := findChat(c, rest[0])
		if err != nil {
			return err
		}
		return printAdmin(c, "invite_new", proto.AdminInviteNew{Chat: id, Nick: strings.TrimPrefix(rest[1], "@"), TTLSeconds: secs})
	}
	return errors.New(inviteUsage)
}

func printAdmin(c *client.Conn, op string, args any) error {
	text, err := adminDo(c, op, args)
	if err != nil {
		return err
	}
	fmt.Println(text)
	return nil
}
