package broker

import (
	"regexp"
	"strings"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/invite"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

var sixDigits = regexp.MustCompile(`\b\d{6}\b`)

func TestTheOwnersPairTwoBrokersByAnInvitationAndTheChatAppearsAtTheInvitedOneOnlyWhenBothConfirm(t *testing.T) {
	a, adb, _, apriv := journalBroker(t)
	b, bdb, _, bpriv := journalBroker(t)
	if _, e := a.setConfig("", "peer.listen", "127.0.0.1:0", nil); e != nil {
		t.Fatal(e)
	}
	for _, p := range []struct {
		b    *Broker
		priv []byte
	}{{a, apriv}, {b, bpriv}} {
		if err := p.b.startPeers(p.priv); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.b.stopPeers)
	}
	ka, kb := newKey(t), newKey(t)
	register(t, a, ka)
	register(t, b, kb)
	adm, bdm := a.connect(t), b.connect(t)
	chat := strings.Repeat("ab", 32)
	if err := adb.NewChat(chat, "team"); err != nil {
		t.Fatal(err)
	}

	// Who may invite and how.
	if _, code := adminCall(t, adm, newKey(t), "peer_invite", proto.PeerInvite{Addr: a.peerAt}); code != "admin_only" {
		t.Fatalf("without the owner's signature: %q", code)
	}
	if _, code := adminCall(t, adm, ka, "peer_invite", proto.PeerInvite{}); code != "no_address" {
		t.Fatalf("no address to put in it: %q", code)
	}
	if _, code := adminCall(t, adm, ka, "peer_invite", proto.PeerInvite{Addr: a.peerAt, Trust: "boss"}); code != "bad_args" {
		t.Fatalf("a level that is none: %q", code)
	}
	if _, code := adminCall(t, adm, ka, "peer_invite", proto.PeerInvite{Addr: a.peerAt, Chats: []string{strings.Repeat("c", 64)}}); code != "no_such_chat" {
		t.Fatalf("a chat that is not here: %q", code)
	}
	str, code := adminCall(t, adm, ka, "peer_invite", proto.PeerInvite{Addr: a.peerAt, Trust: "member", Chats: []string{chat}})
	if code != "" {
		t.Fatalf("peer_invite: %q", code)
	}
	if _, err := invite.Parse(str); err != nil {
		t.Fatalf("not an invitation: %q", str)
	}

	if _, code := adminCall(t, bdm, kb, "peer_join", proto.PeerJoin{Invitation: "ccqc1.abc.def"}); code != "bad_args" {
		t.Fatalf("a mistyped invitation: %q", code)
	}
	text, code := adminCall(t, bdm, kb, "peer_join", proto.PeerJoin{Invitation: str, Trust: "trusted"})
	if code != "" {
		t.Fatalf("peer_join: %q %q", text, code)
	}
	joined := sixDigits.FindString(text)
	if joined == "" || !strings.Contains(text, a.origin) || !strings.Contains(text, "/team") {
		t.Fatalf("what the owner is told: %q", text)
	}
	if _, code := adminCall(t, bdm, kb, "peer_join", proto.PeerJoin{Invitation: str}); code != "join_failed" {
		t.Fatalf("the invitation used twice: %q", code)
	}

	// The same code on both sides, shown only to the owner who asks.
	shown, code := adminCall(t, adm, ka, "peer_code", proto.PeerCode{Origin: b.origin})
	if code != "" || sixDigits.FindString(shown) != joined {
		t.Fatalf("alice's code %q, bob's %q (%q)", shown, joined, code)
	}
	if _, code := adminCall(t, adm, newKey(t), "peer_code", proto.PeerCode{Origin: b.origin}); code != "admin_only" {
		t.Fatalf("the code without the owner's signature: %q", code)
	}
	var l proto.PeerList
	adm.call("peer_list", nil, &l)
	if len(l.Peers) != 1 || l.Peers[0].State != "pending" || l.Peers[0].Until == "" || strings.Contains(strings.Join([]string{l.Peers[0].Reasons, l.Peers[0].Addr}, " "), joined) {
		t.Fatalf("%+v", l.Peers)
	}

	// Pending: the chat is not at bob's yet.
	if ids, _ := bdb.ChatIDs(); len(ids) != 0 {
		t.Fatalf("a chat was made before the confirmation: %v", ids)
	}
	if _, code := adminCall(t, bdm, kb, "peer_confirm", proto.PeerCode{Origin: a.origin, Code: "000000"}); code != "bad_code" {
		t.Fatalf("a wrong code: %q", code)
	}
	if text, code := adminCall(t, adm, ka, "peer_confirm", proto.PeerCode{Origin: b.origin, Code: joined}); code != "" || !strings.Contains(text, "waiting") {
		t.Fatalf("%q %q", text, code)
	}
	if text, code := adminCall(t, bdm, kb, "peer_confirm", proto.PeerCode{Origin: a.origin, Code: joined}); code != "" || !strings.Contains(text, "active") {
		t.Fatalf("%q %q", text, code)
	}
	eventually(t, "alice active", func() bool {
		ps, _ := adb.Peers()
		return len(ps) == 1 && ps[0].State == store.StateActive
	})
	// The chat of the same id and name is made at bob's and shared with alice's node.
	if ids, _ := bdb.ChatIDs(); len(ids) != 1 || ids[0] != chat {
		t.Fatalf("bob's chats: %v", ids)
	}
	if name, _ := bdb.ChatName(chat); name != "team" {
		t.Fatalf("its name: %q", name)
	}
	if with, _ := bdb.SharedWith(chat); len(with) != 1 || with[0] != a.origin {
		t.Fatalf("shared with: %v", with)
	}
}

func TestTheInvitationAndTheCodeAreNotWrittenToTheAdminLog(t *testing.T) {
	in := `{"invitation":"ccqc1.SECRET.abcd","trust":"member"}`
	if got := string(redactAdminArgs("peer_join", []byte(in))); strings.Contains(got, "SECRET") || !strings.Contains(got, "member") {
		t.Fatalf("%s", got)
	}
	cf := `{"code":"424242","origin":"` + otherNode + `"}`
	if got := string(redactAdminArgs("peer_confirm", []byte(cf))); strings.Contains(got, "424242") || !strings.Contains(got, otherNode) {
		t.Fatalf("%s", got)
	}
	if got := string(redactAdminArgs("peer_trust", []byte(cf))); got != cf {
		t.Fatalf("another operation was changed: %s", got)
	}
}
