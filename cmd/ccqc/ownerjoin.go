package main

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/PHPCraftdream/cc-quant-chat/internal/client"
	"github.com/PHPCraftdream/cc-quant-chat/internal/invite"
	"github.com/PHPCraftdream/cc-quant-chat/internal/local"
	"github.com/PHPCraftdream/cc-quant-chat/internal/owner"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// How a terminal enters a chat once the machine has an owner: with the owner's key (the password is
// typed on the console), or with an invitation of its own. A token alone is no way in. The chat is
// found by its name through the broker; only a chat made before invitations, that the broker does
// not know by name, falls back to a token (and with an owner that works only for the nicks the chat
// already knows).

// chatForJoin gives the id of a chat by its name, and the token if the chat is an older one that
// is entered by token.
func chatForJoin(c *client.Conn, chat string) (id, token string, err error) {
	id, err = findChat(c, chat)
	if err == nil {
		return id, "", nil
	}
	if errors.Is(err, errSeveralChats) {
		return "", "", err
	}
	// Not known by name here (or an older broker): a chat of tokens.
	t, ok := local.FindChatToken(chat)
	if !ok {
		var rerr error
		if t, rerr = owner.ReadSecret(fmt.Sprintf("Token of chat /%s (not stored): ", chat)); rerr != nil {
			return "", "", rerr
		}
	}
	t = strings.TrimSpace(t)
	_, id, perr := proto.ParseToken(t)
	if perr != nil {
		return "", "", err // the first answer is the better one
	}
	return id, t, nil
}

// joinOwner enters a chat as the owner under nick: the owner's key proves it. It returns the
// connection (the caller closes it), the chat id, the owner's key and the welcome.
func joinOwner(chat, nick string, level int) (*client.Conn, string, ed25519.PrivateKey, proto.Welcome, error) {
	var w proto.Welcome
	f, err := owner.Load(local.OwnerKeyPath())
	if err != nil {
		return nil, "", nil, w, fmt.Errorf("no key file at %s (run `ccqc owner init`): %w", local.OwnerKeyPath(), err)
	}
	c, err := client.Connect()
	if err != nil {
		return nil, "", nil, w, err
	}
	fail := func(err error) (*client.Conn, string, ed25519.PrivateKey, proto.Welcome, error) {
		c.Close()
		return nil, "", nil, w, err
	}
	if err := matchBroker(c, f); err != nil {
		return fail(err)
	}
	chatID, token, err := chatForJoin(c, chat)
	if err != nil {
		return fail(err)
	}
	pw, err := owner.ReadSecret("Owner password: ")
	if err != nil {
		return fail(err)
	}
	priv, err := f.Open(pw)
	if err != nil {
		return fail(err)
	}
	var ch proto.Challenge
	if err := c.Call("challenge", nil, &ch); err != nil {
		return fail(err)
	}
	args := proto.JoinArgs{Name: nick, Key: local.NickKey(chatID, nick), Proto: level,
		OwnerSig: owner.Encode(ed25519.Sign(priv, proto.OwnerJoinMessage(chatID, nick, ch.Nonce)))}
	if token != "" {
		args.Token = token
	} else {
		args.Chat = chatID
	}
	if err := c.Call("join", args, &w); err != nil {
		return fail(err)
	}
	if w.Key != "" {
		if err := local.SetNickKey(chatID, nick, w.Key); err != nil {
			fmt.Fprintf(os.Stderr, "warning: the key of nick @%s could not be saved (%v)\n", nick, err)
		}
	}
	return c, chatID, priv, w, nil
}

// joinInvited enters a chat with an invitation of the terminal's own: the key it registers is a
// protected one (derived from the client secret and the broker's), kept nowhere in the clear. The
// invitation is not stored anywhere. The nick has no session: it is not bound to one.
func joinInvited(line string) (*client.Conn, *client.Signer, error) {
	in, err := invite.Parse(line)
	if err != nil {
		return nil, nil, err
	}
	c, err := client.Connect()
	if err != nil {
		return nil, nil, err
	}
	var w proto.Welcome
	kj, err := client.JoinWithKeys(c.Call, client.KeyJoinInput{ChatID: in.ChatID, Nick: in.Nick, Invite: &in}, &w)
	if err != nil {
		c.Close()
		return nil, nil, err
	}
	fmt.Fprint(os.Stderr, kj.Note)
	return c, kj.Signer, nil
}

// joinTerminal enters a chat as a nick of this machine whose key is protected (or still plain and moved at
// this join), the way a session does but without one: the client secret proves it, no password is typed.
func joinTerminal(chat, nick string) (*client.Conn, *client.Signer, error) {
	c, err := client.Connect()
	if err != nil {
		return nil, nil, err
	}
	chatID, token, err := chatForJoin(c, chat)
	if err != nil {
		c.Close()
		return nil, nil, err
	}
	var w proto.Welcome
	kj, err := client.JoinWithKeys(c.Call, client.KeyJoinInput{ChatID: chatID, Token: token, Nick: nick}, &w)
	if err != nil {
		c.Close()
		return nil, nil, err
	}
	fmt.Fprint(os.Stderr, kj.Note)
	return c, kj.Signer, nil
}

// nickKnownHere: this machine holds a protected key for a nick of that name.
func nickKnownHere(nick string) bool {
	if got, err := local.ProtectedNicks(); err == nil {
		for _, k := range got {
			if strings.HasSuffix(k, "/"+nick) {
				return true
			}
		}
	}
	return false
}
