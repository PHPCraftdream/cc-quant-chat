package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/PHPCraftdream/cc-quant-chat/internal/addr"
	"github.com/PHPCraftdream/cc-quant-chat/internal/client"
	"github.com/PHPCraftdream/cc-quant-chat/internal/local"
	"github.com/PHPCraftdream/cc-quant-chat/internal/owner"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

const ownerUsage = "usage: ccqc owner init | status | say [--nick NAME] <address> [text|-] | rotate"

// runOwner: the commands of the human who owns ccqc on this machine. They need a console: the
// password is read from it and from nowhere else.
func runOwner(args []string) error {
	if len(args) == 0 {
		return errors.New(ownerUsage)
	}
	switch args[0] {
	case "init":
		return ownerInit()
	case "status":
		return ownerStatus()
	case "say":
		return ownerSay(args[1:])
	case "rotate":
		return ownerRotate()
	}
	return errors.New(ownerUsage)
}

func fp(pub ed25519.PublicKey) string { return owner.Fingerprint(pub) }

func brokerOwner(c *client.Conn) (proto.OwnerInfo, error) {
	var info proto.OwnerInfo
	err := c.Call("owner_info", nil, &info)
	if proto.IsCode(err, "unknown_op") || proto.IsCode(err, "not_joined") {
		return info, errors.New("the running broker is older and has no owner support: run `ccqc broker restart`")
	}
	return info, err
}

// newPassword asks twice and refuses a short or mismatched one.
// matchBroker refuses to go on when the owner key the broker holds is not the one in the local
// key file: the password would be typed for a key the broker does not trust, or the broker was
// given another key.
func matchBroker(c *client.Conn, f owner.File) error {
	info, err := brokerOwner(c)
	if err != nil {
		return err
	}
	if !info.Registered {
		return errors.New("no owner is registered at the broker; run `ccqc owner init`")
	}
	pub, err := f.PublicKey()
	if err != nil {
		return err
	}
	if fp(pub) != info.Fingerprint {
		return fmt.Errorf("refusing: the owner key at the broker (%s, registered %s) is not the key in your key file (%s). "+
			"Someone else registered a key, or the database was changed. Nothing was sent; see README, \"Owner\"", info.Fingerprint, info.Since, fp(pub))
	}
	return nil
}

func newPassword(what string) (string, error) {
	p1, err := owner.ReadSecret(what + " password: ")
	if err != nil {
		return "", err
	}
	if len(p1) < 8 {
		return "", errors.New("the password must be at least 8 characters")
	}
	p2, err := owner.ReadSecret("Repeat it: ")
	if err != nil {
		return "", err
	}
	if p1 != p2 {
		return "", errors.New("the passwords differ")
	}
	return p1, nil
}

func ownerInit() error {
	path := local.OwnerKeyPath()
	if f, err := owner.Load(path); err == nil {
		pub, _ := f.PublicKey()
		return fmt.Errorf("there is a key file already (%s, %s); to replace the key use `ccqc owner rotate`", path, fp(pub))
	}
	c, err := client.Connect()
	if err != nil {
		return err
	}
	defer c.Close()
	info, err := brokerOwner(c)
	if err != nil {
		return err
	}
	if info.Registered {
		return fmt.Errorf("the broker already has an owner key (%s, registered %s). If it is not yours, someone registered before you: "+
			"stop the broker (`ccqc broker stop`), delete the row of the `owner` table in node.db (the directory is `ccqc db path`) and run init again; README, \"Owner\"", info.Fingerprint, info.Since)
	}
	pw, err := newPassword("New owner")
	if err != nil {
		return err
	}
	priv, file, err := owner.Generate(pw, owner.Iterations)
	if err != nil {
		return err
	}
	pub := owner.Encode(priv.Public().(ed25519.PublicKey))
	sig := owner.Encode(ed25519.Sign(priv, proto.OwnerRegisterMessage(pub)))
	if err := c.Call("owner_register", proto.OwnerRegisterArgs{Pub: pub, Sig: sig}, nil); err != nil {
		return err
	}
	if err := owner.Save(path, file); err != nil {
		return fmt.Errorf("the key is registered at the broker but could not be saved (%v): remove the owner row as described in the README and run init again", err)
	}
	fmt.Printf("owner key created: %s\nkey file: %s (encrypted; keep the password, it can't be recovered)\n", fp(priv.Public().(ed25519.PublicKey)), path)
	return nil
}

func ownerStatus() error {
	var brokerFP string
	c, err := client.Dial()
	if err != nil {
		fmt.Println("broker:    not running")
	} else {
		defer c.Close()
		info, err := brokerOwner(c)
		switch {
		case err != nil:
			return err
		case info.Registered:
			brokerFP = info.Fingerprint
			fmt.Println("broker:   ", brokerFP, "(registered "+info.Since+")")
		default:
			fmt.Println("broker:    no owner registered (run `ccqc owner init`)")
		}
	}
	localFP := ""
	if f, err := owner.Load(local.OwnerKeyPath()); err == nil {
		if pub, err := f.PublicKey(); err == nil {
			localFP = fp(pub)
		}
	}
	if localFP == "" {
		fmt.Println("key file:   none at", local.OwnerKeyPath())
	} else {
		fmt.Println("key file:  ", localFP)
	}
	switch {
	case brokerFP != "" && localFP != "" && brokerFP == localFP:
		fmt.Println("the keys match")
	case brokerFP != "" && localFP != "":
		return errors.New("the keys DIFFER: the broker's owner key is not the one in your key file. Someone registered another key, or the database was changed")
	}
	return nil
}

func ownerRotate() error {
	path := local.OwnerKeyPath()
	f, err := owner.Load(path)
	if err != nil {
		return fmt.Errorf("no key file at %s: %w", path, err)
	}
	c, err := client.Connect()
	if err != nil {
		return err
	}
	defer c.Close()
	if err := matchBroker(c, f); err != nil {
		return err
	}
	pw, err := owner.ReadSecret("Current owner password: ")
	if err != nil {
		return err
	}
	oldPriv, err := f.Open(pw)
	if err != nil {
		return err
	}
	newPw, err := newPassword("New owner")
	if err != nil {
		return err
	}
	newPriv, newFile, err := owner.Generate(newPw, owner.Iterations)
	if err != nil {
		return err
	}
	oldPub := owner.Encode(oldPriv.Public().(ed25519.PublicKey))
	newPub := owner.Encode(newPriv.Public().(ed25519.PublicKey))
	msg := proto.OwnerRotateMessage(oldPub, newPub)
	if err := c.Call("owner_rotate", proto.OwnerRotateArgs{
		Pub:    newPub,
		OldSig: owner.Encode(ed25519.Sign(oldPriv, msg)),
		NewSig: owner.Encode(ed25519.Sign(newPriv, msg)),
	}, nil); err != nil {
		return err
	}
	if err := owner.Save(path, newFile); err != nil {
		return fmt.Errorf("the broker has the new key but it could not be saved (%v)", err)
	}
	fmt.Printf("owner key replaced: %s\n", fp(newPriv.Public().(ed25519.PublicKey)))
	return nil
}

// ownerSay joins a chat as the owner, sends one message and leaves.
func ownerSay(args []string) error {
	nick := proto.OwnerNick
	var rest []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--nick" && i+1 < len(args) {
			nick = strings.TrimPrefix(args[i+1], "@")
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	if len(rest) < 1 || len(rest) > 2 {
		return errors.New("usage: ccqc owner say [--nick NAME] <address> [text|-]   (no text, or -, reads the message from stdin)")
	}
	a, err := addr.Parse(rest[0])
	if err != nil {
		return err
	}
	text := ""
	if len(rest) == 2 && rest[1] != "-" {
		text = rest[1]
	} else {
		raw, err := io.ReadAll(io.LimitReader(os.Stdin, proto.MaxText+1))
		if err != nil {
			return err
		}
		text = strings.TrimRight(string(raw), "\r\n")
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("the message is empty")
	}

	c, chatID, priv, w, err := joinOwner(a.Chat, nick, proto.ProtoVersion)
	if err != nil {
		return err
	}
	defer c.Close()
	defer func() { _ = c.Call("leave", nil, nil) }()
	if a.Room != proto.RoomCommon && a.Room != proto.RoomDirect {
		if err := c.Call("room_join", proto.RoomArgs{Room: a.Room}, nil); err != nil {
			return err
		}
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	var r proto.SendResult
	sa := proto.SendArgs{Room: a.Room, To: a.Member, Text: text, ClientMsgID: hex.EncodeToString(id)}
	if w.Proto >= proto.ProtoVersion && w.Origin != "" {
		// The owner key is the author's: the OWNER marker can be checked against the message itself.
		sg := &client.Signer{Priv: priv, ChatID: chatID, Origin: w.Origin}
		err = sg.Write(c.Call, "send", "msg", &sa, &r)
	} else {
		err = c.Call("send", sa, &r)
	}
	if err != nil {
		return err
	}
	fmt.Printf("sent #%d to %s as OWNER @%s\n", r.ID, a, nick)
	return nil
}
