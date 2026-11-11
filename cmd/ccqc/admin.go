package main

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/PHPCraftdream/cc-quant-chat/internal/client"
	"github.com/PHPCraftdream/cc-quant-chat/internal/local"
	"github.com/PHPCraftdream/cc-quant-chat/internal/owner"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// Administrator operations (role give/take, config set, nick reset once an owner exists) are
// signed with the owner key: the password is typed on the console, the broker verifies the
// signature over the operation, its arguments and a one-time challenge.

// adminDo asks for the owner password, signs the operation and sends it. The reply text is
// returned.
func adminDo(c *client.Conn, op string, args any) (string, error) {
	f, err := owner.Load(local.OwnerKeyPath())
	if err != nil {
		return "", fmt.Errorf("no owner key file at %s (run `ccqc owner init`): %w", local.OwnerKeyPath(), err)
	}
	if err := matchBroker(c, f); err != nil {
		return "", err
	}
	pw, err := owner.ReadSecret("Owner password: ")
	if err != nil {
		return "", err
	}
	priv, err := f.Open(pw)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	canon, err := proto.CanonicalJSON(raw)
	if err != nil {
		return "", err
	}
	var ch proto.Challenge
	if err := c.Call("challenge", nil, &ch); err != nil {
		return "", err
	}
	sig := owner.Encode(ed25519.Sign(priv, proto.AdminMessage(op, canon, ch.Nonce)))
	var r proto.BoardReply
	if err := c.Call("admin", proto.AdminArgs{Op: op, Args: raw, Sig: sig}, &r); err != nil {
		return "", err
	}
	return r.Text, nil
}

// chatID finds the id of a chat by its name: the broker is asked; an older chat the broker does not
// know by that name is found by the token from the user's sessions, else the token is asked for on
// the console.
func chatID(name string) (string, error) {
	if c, err := client.Connect(); err == nil { // the broker knows its chats by name; no token is needed
		id, ferr := findChat(c, name)
		c.Close()
		if ferr == nil {
			return id, nil
		}
		if errors.Is(ferr, errSeveralChats) {
			return "", ferr
		}
	}
	token, ok := local.FindChatToken(name)
	if !ok {
		var err error
		if token, err = owner.ReadSecret(fmt.Sprintf("Token of chat /%s (not stored): ", name)); err != nil {
			return "", err
		}
	}
	_, id, err := proto.ParseToken(strings.TrimSpace(token))
	return id, err
}

// runRole: ccqc role give <chat> <nick> <role> | take <chat> <nick>.
func runRole(args []string) error {
	usage := errors.New("usage: ccqc role give <chat> <nick> <role> | take <chat> <nick>")
	if len(args) < 3 || (args[0] == "give" && len(args) != 4) || (args[0] == "take" && len(args) != 3) || (args[0] != "give" && args[0] != "take") {
		return usage
	}
	id, err := chatID(strings.Trim(args[1], "/"))
	if err != nil {
		return err
	}
	c, err := client.Connect()
	if err != nil {
		return err
	}
	defer c.Close()
	a := proto.AdminRole{Chat: id, Nick: strings.TrimPrefix(args[2], "@")}
	op := "role_take"
	if args[0] == "give" {
		op, a.Role = "role_give", args[3]
	}
	text, err := adminDo(c, op, a)
	if err != nil {
		return err
	}
	fmt.Println(text)
	return nil
}

// runConfig: ccqc config list [--chat NAME] | get <key> [--chat NAME] | set <key> <value> [--chat NAME].
func runConfig(args []string) error {
	usage := errors.New("usage: ccqc config list|get <key>|set <key> <value> [--chat NAME]")
	var rest []string
	chatName := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--chat" && i+1 < len(args) {
			chatName = strings.Trim(args[i+1], "/")
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	if len(rest) == 0 {
		return usage
	}
	id := ""
	if chatName != "" {
		var err error
		if id, err = chatID(chatName); err != nil {
			return err
		}
	}
	c, err := client.Connect()
	if err != nil {
		return err
	}
	defer c.Close()
	switch rest[0] {
	case "list", "get":
		var list proto.ConfigList
		if err := c.Call("config_list", proto.AdminConfig{Chat: id}, &list); err != nil {
			if proto.IsCode(err, "unknown_op") || proto.IsCode(err, "not_joined") {
				return errors.New("the running broker is older and has no settings: run `ccqc broker restart`")
			}
			return err
		}
		for _, it := range list.Items {
			if rest[0] == "get" && (len(rest) != 2 || rest[1] != it.Key) {
				continue
			}
			scope := ""
			if it.Chat {
				scope = " (per chat: use --chat)"
			}
			fmt.Printf("%s = %q  [%s; default %q]%s\n  %s\n", it.Key, it.Value, it.Source, it.Default, scope, it.Help)
		}
		return nil
	case "set":
		if len(rest) != 3 {
			return usage
		}
		text, err := adminDo(c, "config_set", proto.AdminConfig{Chat: id, Key: rest[1], Value: rest[2]})
		if err != nil {
			return err
		}
		fmt.Println(text)
		return nil
	}
	return usage
}
