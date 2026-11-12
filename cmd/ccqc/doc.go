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
// --invite, an invitation of its own for another nick.
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
	c, signer, leave, err := joinForDoc(chat, nick, invitation)
	if err != nil {
		return err
	}
	defer leave()
	call := func(a proto.DocArgs) (proto.DocReply, error) {
		var r proto.DocReply
		var err error
		if signer != nil && a.Op == "put" { // a nick with a key signs what it writes
			err = signer.Write(c.Call, "doc", "doc."+a.Op, &a, &r)
		} else {
			err = c.Call("doc", a, &r)
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

// joinForDoc enters a chat for the document commands: by an invitation (a protected nick of its own), as a protected
// nick of this machine (--nick), or as the owner (the owner's key; the password is asked for on the console). What
// is written is signed: by the nick's key, or by the owner's.
func joinForDoc(chat, nick, invitation string) (*client.Conn, *client.Signer, func(), error) {
	leave := func(c *client.Conn) func() { return func() { _ = c.Call("leave", nil, nil); c.Close() } }
	if invitation != "" {
		c, sg, err := joinInvited(invitation)
		if err != nil {
			return nil, nil, nil, err
		}
		return c, sg, leave(c), nil
	}
	if nick != "" && nick != proto.OwnerNick && nickKnownHere(nick) { // a protected nick of this machine: no password per command
		c, sg, err := joinTerminal(chat, nick)
		if err != nil {
			return nil, nil, nil, err
		}
		return c, sg, leave(c), nil
	}
	if _, err := owner.Load(local.OwnerKeyPath()); err != nil {
		return nil, nil, nil, errors.New("this machine has no owner key (ccqc owner init) and no protected nick of that name: pass --invite or --nick")
	}
	if nick == "" {
		nick = proto.OwnerNick
	}
	c, chatID, priv, w, err := joinOwner(chat, nick)
	if err != nil {
		return nil, nil, nil, err
	}
	return c, &client.Signer{Priv: priv, ChatID: chatID, Origin: w.Origin}, leave(c), nil
}
