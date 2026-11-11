package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/client"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

const peerUsage = "usage: ccqc peer add <addr> <origin> | list | remove <origin> | quarantine <origin> [reason] | release <origin> | trust <origin> read|member|trusted | share <chat> <origin> | unshare <chat> <origin> | invite | join | code | confirm (ccqc peer invite --help style: " + peerPairUsage + ")"

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
	if ok, err := peerPair(c, args); ok {
		return err
	}
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
	case args[0] == "trust" && len(args) == 3:
		return peerOp(c, "peer_trust", proto.PeerTrust{Origin: args[1], Level: args[2]})
	case args[0] == "quarantine" && len(args) >= 2:
		return peerOp(c, "peer_quarantine", proto.PeerQuarantine{Origin: args[1], Reason: strings.Join(args[2:], " ")})
	case args[0] == "release" && len(args) == 2:
		return peerOp(c, "peer_release", proto.PeerQuarantine{Origin: args[1]})
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
	if len(l.Quarantined) > 0 {
		fmt.Println("in quarantine (new events are not taken; lift: ccqc peer release <origin>):")
		for _, q := range l.Quarantined {
			fmt.Println("  " + q)
		}
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

// pendingWhat says what a pending node waits for (the code itself is not shown here: ccqc peer code).
func pendingWhat(p proto.PeerInfo) string {
	what := "waits for the code: ccqc peer code " + p.Origin
	if p.Confirmed {
		what = "confirmed here, waits for the other owner"
	}
	if p.Until != "" {
		if t, err := time.Parse(time.RFC3339, p.Until); err == nil {
			what += "; dropped at " + t.Local().Format("15:04")
		}
	}
	return what
}
