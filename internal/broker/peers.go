package broker

import (
	"crypto/ed25519"
	"encoding/json"
	"log"
	"net"
	"regexp"
	"slices"

	"github.com/PHPCraftdream/cc-quant-chat/internal/peer"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

var originName = regexp.MustCompile(`^[0-9a-f]{32}$`)

// startPeers starts the exchange of chats with other nodes: the manager that keeps the connections,
// and the listener if `peer.listen` is set (it is not by default: the broker listens to no one
// outside its machine unless the owner says so). Without a store that can exchange (memory) there
// is nothing to start.
func (b *Broker) startPeers(priv ed25519.PrivateKey) error {
	fed, ok := b.store.(store.Federation)
	if !ok || priv == nil {
		return nil
	}
	m := peer.New(fed, b.origin, priv, log.Printf)
	fed.OnEvents(m.Notify)
	b.peerMgr = m
	if addr, _ := b.setting("", "peer.listen"); addr != "" {
		bound, err := m.Listen(addr)
		if err != nil {
			m.Close()
			b.peerMgr = nil
			return err
		}
		b.peerAt = bound.String()
		log.Printf("broker: accepting nodes on %s", bound)
	}
	return m.Refresh()
}

func (b *Broker) stopPeers() {
	if b.peerMgr != nil {
		b.peerMgr.Close()
	}
}

// peerAdmin runs the administrator operations on the nodes.
func (b *Broker) peerAdmin(op string, args json.RawMessage) (any, *proto.Error) {
	fed, ok := b.store.(store.Federation)
	if !ok || b.peerMgr == nil {
		return nil, proto.Errf("peers_unavailable", "exchange with other nodes needs the SQLite store (the broker runs with CCQC_STORE=memory)")
	}
	bad := func(err error) *proto.Error { return proto.Errf("bad_args", "%v", err) }
	done := func(text string) (any, *proto.Error) {
		if err := b.peerMgr.Refresh(); err != nil {
			return nil, proto.Errf("store_failed", "%v", err)
		}
		return proto.BoardReply{Text: text}, nil
	}
	switch op {
	case "peer_add":
		var a proto.PeerAdd
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, bad(err)
		}
		if !originName.MatchString(a.Origin) {
			return nil, proto.Errf("bad_args", "origin is 32 hex characters (`ccqc broker status` on the other node shows it)")
		}
		if a.Origin == b.origin {
			return nil, proto.Errf("bad_args", "that is this node")
		}
		if _, _, err := net.SplitHostPort(a.Addr); err != nil {
			return nil, proto.Errf("bad_args", "addr is host:port, got %q", a.Addr)
		}
		if err := fed.AddPeer(a.Origin, a.Addr); err != nil {
			return nil, proto.Errf("store_failed", "%v", err)
		}
		return done("node " + a.Origin + " added (" + a.Addr + "); share chats with it: ccqc peer share <chat> " + a.Origin)
	case "peer_remove":
		var a proto.PeerRemove
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, bad(err)
		}
		if err := fed.RemovePeer(a.Origin); err != nil {
			return nil, proto.Errf("store_failed", "%v", err)
		}
		return done("node " + a.Origin + " removed")
	case "peer_share", "peer_unshare":
		var a proto.PeerShare
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, bad(err)
		}
		ids, ok := b.store.(interface{ ChatIDs() ([]string, error) })
		if !ok {
			return nil, proto.Errf("peers_unavailable", "the store does not list chats")
		}
		list, err := ids.ChatIDs()
		if err != nil {
			return nil, proto.Errf("store_failed", "%v", err)
		}
		if !slices.Contains(list, a.Chat) {
			return nil, proto.Errf("no_such_chat", "this node has no chat with that id: only a chat both nodes have can be shared")
		}
		if op == "peer_share" {
			err = fed.SharePeerChat(a.Chat, a.Origin)
		} else {
			err = fed.UnsharePeerChat(a.Chat, a.Origin)
		}
		if err != nil {
			return nil, proto.Errf("bad_args", "%v", err)
		}
		return done(op[len("peer_"):] + "d chat with " + a.Origin)
	}
	return nil, proto.Errf("unknown_op", "no administrator operation %q", op)
}

// peerList answers `ccqc peer list`.
func (b *Broker) peerList() (any, *proto.Error) {
	out := proto.PeerList{Node: b.origin, Listen: b.peerAt, Peers: []proto.PeerInfo{}}
	fed, ok := b.store.(store.Federation)
	if !ok || b.peerMgr == nil {
		return out, nil
	}
	peers, err := fed.Peers()
	if err != nil {
		return nil, proto.Errf("store_failed", "%v", err)
	}
	connected := b.peerMgr.Sessions()
	for _, p := range peers {
		reason, _, _ := fed.Flagged(p.Origin)
		out.Peers = append(out.Peers, proto.PeerInfo{Origin: p.Origin, Addr: p.Addr, Trust: p.Trust, Chats: p.Chats,
			Connected: slices.Contains(connected, p.Origin), Flagged: reason})
	}
	return out, nil
}
