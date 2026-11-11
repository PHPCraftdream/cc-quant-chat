package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/PHPCraftdream/cc-quant-chat/internal/client"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

const peerUsage = "usage: ccqc peer add <host:port> <origin> | list | remove <origin> | share <chat> <origin> | unshare <chat> <origin>"

// runPeer: the nodes this broker exchanges chats with. `list` is read-only; the rest are
// administrator operations signed with the owner key.
func runPeer(args []string) error {
	if len(args) == 0 {
		return errors.New(peerUsage)
	}
	c, err := client.Connect()
	if err != nil {
		return err
	}
	defer c.Close()
	switch {
	case args[0] == "list" && len(args) == 1:
		var l proto.PeerList
		if err := c.Call("peer_list", nil, &l); err != nil {
			if proto.IsCode(err, "unknown_op") {
				return errors.New("the running broker is older and has no nodes: run `ccqc broker restart`")
			}
			return err
		}
		printPeers(l)
		return nil
	case args[0] == "add" && len(args) == 3:
		return peerOp(c, "peer_add", proto.PeerAdd{Addr: args[1], Origin: args[2]})
	case args[0] == "remove" && len(args) == 2:
		return peerOp(c, "peer_remove", proto.PeerRemove{Origin: args[1]})
	case (args[0] == "share" || args[0] == "unshare") && len(args) == 3:
		id, err := chatID(strings.Trim(args[1], "/"))
		if err != nil {
			return err
		}
		return peerOp(c, "peer_"+args[0], proto.PeerShare{Chat: id, Origin: args[2]})
	}
	return errors.New(peerUsage)
}

func peerOp(c *client.Conn, op string, args any) error {
	text, err := adminDo(c, op, args)
	if err != nil {
		return err
	}
	fmt.Println(text)
	return nil
}

func printPeers(l proto.PeerList) {
	fmt.Printf("this node: %s\n", l.Node)
	if l.Listen != "" {
		fmt.Printf("accepting nodes on %s\n", l.Listen)
	} else {
		fmt.Println("not accepting nodes (set peer.listen to host:port to do so)")
	}
	if len(l.Peers) == 0 {
		fmt.Println("no nodes added")
		return
	}
	for _, p := range l.Peers {
		state := "disconnected"
		if p.Connected {
			state = "connected"
		}
		if p.Flagged != "" {
			state = "flagged: " + p.Flagged
		}
		addr := p.Addr
		if addr == "" {
			addr = "(it connects to us)"
		}
		fmt.Printf("%s  %s  %s  %s  shared chats: %d\n", p.Origin, addr, p.Trust, state, len(p.Chats))
	}
}
