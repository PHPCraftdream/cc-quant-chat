package main

import (
	"crypto/ed25519"
	"fmt"

	"github.com/PHPCraftdream/cc-quant-chat/internal/client"
	"github.com/PHPCraftdream/cc-quant-chat/internal/local"
	"github.com/PHPCraftdream/cc-quant-chat/internal/owner"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// How a terminal enters a chat: with the owner's key, the password typed on the console, and in no other way: a
// terminal has no Claude Code session to bind a nick to, and a nick without a session or a password would be a
// door for any program of the user. The chat is found by its name through the broker.

// chatForJoin gives the id of a chat by its name (or takes the id itself).
func chatForJoin(c *client.Conn, chat string) (string, error) { return findChat(c, chat) }

// joinOwner enters a chat as the owner under nick: the owner's key proves it. It returns the
// connection (the caller closes it), the chat id, the owner's key and the welcome.
func joinOwner(chat, nick string) (*client.Conn, string, ed25519.PrivateKey, proto.Welcome, error) {
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
	chatID, err := chatForJoin(c, chat)
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
	args := proto.JoinArgs{Name: nick, Chat: chatID, Proto: proto.ProtoVersion,
		OwnerSig: owner.Encode(ed25519.Sign(priv, proto.OwnerJoinMessage(chatID, nick, ch.Nonce)))}
	if err := c.Call("join", args, &w); err != nil {
		return fail(err)
	}
	return c, chatID, priv, w, nil
}
