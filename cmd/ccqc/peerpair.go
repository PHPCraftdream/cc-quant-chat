package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/PHPCraftdream/cc-quant-chat/internal/client"
	"github.com/PHPCraftdream/cc-quant-chat/internal/invite"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

const peerPairUsage = "usage: ccqc peer invite [--ttl 15m] [--trust read|member|trusted] [--addr host:port] [--chat <chat>]... | invite revoke <id> | join <invitation> [--trust ...] | code <origin> | confirm <origin> <code>"

// peerPair: inviting a node, joining by an invitation and comparing the code (docs/design-trust.md
// section 2). All are administrator operations; the invitation and the code appear only here.
func peerPair(c *client.Conn, args []string) (handled bool, err error) {
	switch {
	case args[0] == "invite" && len(args) >= 2 && args[1] == "revoke":
		if len(args) != 3 {
			return true, errors.New(peerPairUsage)
		}
		return true, peerOp(c, "peer_invite_revoke", proto.PeerInviteID{ID: args[2]})
	case args[0] == "invite":
		return true, peerInvite(c, args[1:])
	case args[0] == "join":
		return true, peerJoin(c, args[1:])
	case args[0] == "code" && len(args) == 2:
		return true, peerOp(c, "peer_code", proto.PeerCode{Origin: args[1]})
	case args[0] == "confirm" && len(args) == 3:
		return true, peerOp(c, "peer_confirm", proto.PeerCode{Origin: args[1], Code: args[2]})
	}
	return false, nil
}

func flagValue(args []string, i int, name string) (string, error) {
	if i+1 >= len(args) {
		return "", fmt.Errorf("%s wants a value", name)
	}
	return args[i+1], nil
}

func peerInvite(c *client.Conn, args []string) error {
	var a proto.PeerInvite
	for i := 0; i < len(args); i += 2 {
		v, err := flagValue(args, i, args[i])
		if err != nil {
			return err
		}
		switch args[i] {
		case "--ttl":
			if a.TTLSeconds, err = parseTTL(v); err != nil {
				return err
			}
		case "--trust":
			a.Trust = v
		case "--addr":
			a.Addr = v
		case "--chat":
			id, err := findChat(c, v)
			if err != nil {
				return err
			}
			a.Chats = append(a.Chats, id)
		default:
			return errors.New(peerPairUsage)
		}
	}
	text, err := adminDo(c, "peer_invite", a)
	if err != nil {
		return err
	}
	fmt.Println(text)
	if in, err := invite.Parse(text); err == nil {
		fmt.Fprintf(os.Stderr, "\nGive this to the owner of the other node; it works once. Its id is %s (revoke: ccqc peer invite revoke %s).\nThey run: ccqc peer join <the line above>\n", invite.Short(in.ID()), invite.Short(in.ID()))
	}
	return nil
}

func peerJoin(c *client.Conn, args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "--") {
		return errors.New(peerPairUsage)
	}
	a := proto.PeerJoin{Invitation: args[0]}
	for i := 1; i < len(args); i += 2 {
		v, err := flagValue(args, i, args[i])
		if err != nil || args[i] != "--trust" {
			return errors.New(peerPairUsage)
		}
		a.Trust = v
	}
	return peerOp(c, "peer_join", a)
}
