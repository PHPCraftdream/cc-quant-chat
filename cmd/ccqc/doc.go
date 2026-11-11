package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/PHPCraftdream/cc-quant-chat/internal/client"
	"github.com/PHPCraftdream/cc-quant-chat/internal/local"
	"github.com/PHPCraftdream/cc-quant-chat/internal/owner"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

const docUsage = "usage: ccqc doc tree <chat> [prefix] | get <chat> <path> | put <chat> <path> <file|-> | export <chat> <dir>   [--nick NAME | --invite LINE]"

// runDoc: the chat's document tree from a terminal. With an owner on this machine it enters with the
// owner's key (the password is asked for on the console) under --nick, "owner" by default; or with
// --invite, an invitation of its own for another nick. Before there is an owner it is an ordinary
// member by the chat's token (from your sessions' config, or asked for on the console).
func runDoc(args []string) error {
	nick, invitation := "", ""
	var rest []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--nick" && i+1 < len(args) {
			nick = strings.TrimPrefix(args[i+1], "@")
			i++
			continue
		}
		if args[i] == "--invite" && i+1 < len(args) {
			invitation = args[i+1]
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	if len(rest) < 2 {
		return errors.New(docUsage)
	}
	cmd, chat := rest[0], strings.Trim(rest[1], "/")
	c, leave, err := joinForDoc(chat, nick, invitation)
	if err != nil {
		return err
	}
	defer leave()
	call := func(a proto.DocArgs) (proto.DocReply, error) {
		var r proto.DocReply
		err := c.Call("doc", a, &r)
		if proto.IsCode(err, "unknown_op") || proto.IsCode(err, "not_joined") {
			err = errors.New("the running broker is older and has no document tree: run `ccqc broker restart`")
		}
		return r, err
	}
	switch cmd {
	case "tree":
		if len(rest) > 3 {
			return errors.New(docUsage)
		}
		prefix := ""
		if len(rest) == 3 {
			prefix = rest[2]
		}
		r, err := call(proto.DocArgs{Op: "tree", Prefix: prefix})
		if err != nil {
			return err
		}
		fmt.Print(r.Text)
		return nil
	case "get":
		if len(rest) != 3 {
			return errors.New(docUsage)
		}
		r, err := call(proto.DocArgs{Op: "get", Path: rest[2], Full: true})
		if err != nil {
			return err
		}
		fmt.Print(r.Body)
		return nil
	case "put":
		if len(rest) != 4 {
			return errors.New(docUsage)
		}
		body, err := readSource(rest[3])
		if err != nil {
			return err
		}
		a := proto.DocArgs{Op: "put", Path: rest[2], Body: body, Note: "ccqc doc put"}
		if cur, err := call(proto.DocArgs{Op: "get", Path: rest[2], Full: true}); err == nil { // replacing: base is what we read
			a.Base = &cur.Version
		} else if !proto.IsCode(err, "no_such_page") {
			return err
		}
		r, err := call(a)
		if err != nil {
			return err
		}
		fmt.Println(r.Text)
		return nil
	case "export":
		if len(rest) != 3 {
			return errors.New(docUsage)
		}
		return exportDocs(call, rest[2])
	}
	return errors.New(docUsage)
}

func readSource(src string) (string, error) {
	var raw []byte
	var err error
	if src == "-" {
		raw, err = io.ReadAll(io.LimitReader(os.Stdin, 65<<10))
	} else {
		raw, err = os.ReadFile(src)
	}
	return string(raw), err
}

// exportDocs writes every live page as <dir>/<path>.md.
func exportDocs(call func(proto.DocArgs) (proto.DocReply, error), dir string) error {
	tree, err := call(proto.DocArgs{Op: "tree", All: true})
	if err != nil {
		return err
	}
	for _, p := range tree.Paths {
		page, err := call(proto.DocArgs{Op: "get", Path: p, Full: true})
		if err != nil {
			return err
		}
		file := filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(p, "/"))+".md")
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(file, []byte(page.Body), 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("exported %d page(s) to %s\n", len(tree.Paths), dir)
	return nil
}

// joinForDoc enters a chat for the document commands: by an invitation, by the owner's key, or (no
// owner on this machine) as before by the token.
func joinForDoc(chat, nick, invitation string) (*client.Conn, func(), error) {
	if invitation != "" {
		c, _, _, err := joinInvited(invitation)
		if err != nil {
			return nil, nil, err
		}
		return c, func() { _ = c.Call("leave", nil, nil); c.Close() }, nil
	}
	if _, err := owner.Load(local.OwnerKeyPath()); err == nil {
		if nick == "" {
			nick = proto.OwnerNick
		}
		// Proto 3: the owner's writes from a terminal are unsigned, as they were.
		c, _, _, _, err := joinOwner(chat, nick, proto.LegacyProto)
		if err != nil {
			return nil, nil, err
		}
		return c, func() { _ = c.Call("leave", nil, nil); c.Close() }, nil
	}
	if nick == "" {
		nick = "cli"
	}
	return joinAsMember(chat, nick)
}

// joinAsMember joins a chat as an ordinary member under nick (its key kept like any nick's) and
// returns the connection and a function that leaves.
func joinAsMember(chat, nick string) (*client.Conn, func(), error) {
	token, ok := local.FindChatToken(chat)
	if !ok {
		var err error
		if token, err = owner.ReadSecret(fmt.Sprintf("Token of chat /%s (not stored): ", chat)); err != nil {
			return nil, nil, err
		}
	}
	token = strings.TrimSpace(token)
	_, chatID, err := proto.ParseToken(token)
	if err != nil {
		return nil, nil, err
	}
	c, err := client.Connect()
	if err != nil {
		return nil, nil, err
	}
	var w proto.Welcome
	if err := c.Call("join", proto.JoinArgs{Token: token, Name: nick, Key: local.NickKey(chatID, nick), Proto: proto.LegacyProto}, &w); err != nil {
		c.Close()
		return nil, nil, err
	}
	if w.Key != "" {
		if err := local.SetNickKey(chatID, nick, w.Key); err != nil {
			fmt.Fprintf(os.Stderr, "warning: the key of nick @%s could not be saved (%v)\n", nick, err)
		}
	}
	return c, func() { _ = c.Call("leave", nil, nil); c.Close() }, nil
}
