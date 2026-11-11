package broker

import (
	"strings"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

const otherNode = "0123456789abcdef0123456789abcdef"

func TestNodesAreAddedSharedAndRemovedByTheOwnerOnly(t *testing.T) {
	b, _, _, priv := journalBroker(t)
	if err := b.startPeers(priv); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.stopPeers)
	tok, id := token(t)
	k := newKey(t)
	register(t, b, k)
	a, adm := b.connect(t), b.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)

	if code := adminReq(t, adm, newKey(t), "peer_add", proto.PeerAdd{Origin: otherNode, Addr: "127.0.0.1:9"}); code == "" {
		t.Fatal("a node was added without the owner's signature")
	}
	for name, args := range map[string]proto.PeerAdd{
		"a short origin": {Origin: "abc", Addr: "127.0.0.1:9"},
		"this node":      {Origin: b.origin, Addr: "127.0.0.1:9"},
		"no port":        {Origin: otherNode, Addr: "localhost"},
		"unknown scheme": {Origin: otherNode, Addr: "quic://127.0.0.1:9"},
	} {
		if code := adminReq(t, adm, k, "peer_add", args); code != "bad_args" {
			t.Fatalf("%s: %q", name, code)
		}
	}
	if code := adminReq(t, adm, k, "peer_add", proto.PeerAdd{Origin: otherNode, Addr: "127.0.0.1:9"}); code != "" {
		t.Fatalf("add: %q", code)
	}
	// A chat nobody here has cannot be shared.
	if code := adminReq(t, adm, k, "peer_share", proto.PeerShare{Chat: strings.Repeat("a", 64), Origin: otherNode}); code != "no_such_chat" {
		t.Fatalf("a chat this node does not have: %q", code)
	}
	if code := adminReq(t, adm, k, "peer_share", proto.PeerShare{Chat: id, Origin: strings.Repeat("b", 32)}); code != "bad_args" {
		t.Fatalf("a node that was not added: %q", code)
	}
	if code := adminReq(t, adm, k, "peer_share", proto.PeerShare{Chat: id, Origin: otherNode}); code != "" {
		t.Fatalf("share: %q", code)
	}

	var l proto.PeerList
	if code := a.call("peer_list", nil, &l); code != "" {
		t.Fatalf("peer_list: %q", code)
	}
	if l.Node != b.origin || l.Listen != "" || len(l.Peers) != 1 {
		t.Fatalf("list: %+v", l)
	}
	if p := l.Peers[0]; p.Origin != otherNode || p.Addr != "tcp://127.0.0.1:9" || p.Trust != "trusted" || len(p.Chats) != 1 || p.Chats[0] != id {
		t.Fatalf("peer: %+v", p)
	}

	if code := adminReq(t, adm, k, "peer_unshare", proto.PeerShare{Chat: id, Origin: otherNode}); code != "" {
		t.Fatalf("unshare: %q", code)
	}
	a.call("peer_list", nil, &l)
	if len(l.Peers) != 1 || len(l.Peers[0].Chats) != 0 {
		t.Fatalf("after unshare: %+v", l)
	}
	if code := adminReq(t, adm, k, "peer_remove", proto.PeerRemove{Origin: otherNode}); code != "" {
		t.Fatalf("remove: %q", code)
	}
	a.call("peer_list", nil, &l)
	if len(l.Peers) != 0 {
		t.Fatalf("after remove: %+v", l)
	}
}

func TestNodesNeedTheSQLiteStore(t *testing.T) {
	b := newTestBroker(newMemStore())
	k := newKey(t)
	register(t, b, k)
	adm := b.connect(t)
	if code := adminReq(t, adm, k, "peer_add", proto.PeerAdd{Origin: otherNode, Addr: "127.0.0.1:9"}); code != "peers_unavailable" {
		t.Fatalf("memory store: %q", code)
	}
	var l proto.PeerList
	if code := adm.call("peer_list", nil, &l); code != "" || len(l.Peers) != 0 {
		t.Fatalf("list on the memory store: %q %+v", code, l)
	}
}

func TestPeerListenIsASettingThatMakesTheBrokerAcceptNodes(t *testing.T) {
	b, _, _, priv := journalBroker(t)
	if _, e := b.setConfig("", "peer.listen", "127.0.0.1:0", nil); e != nil {
		t.Fatalf("a valid address: %v", e)
	}
	if _, e := b.setConfig("", "peer.listen", "no-port", nil); e == nil {
		t.Fatal("an address without a port was accepted")
	}
	if err := b.startPeers(priv); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.stopPeers)
	if b.peerAt == "" || !strings.HasPrefix(b.peerAt, "127.0.0.1:") {
		t.Fatalf("listening on %q", b.peerAt)
	}
}
