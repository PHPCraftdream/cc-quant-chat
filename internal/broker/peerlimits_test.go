package broker

import (
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

func TestTheLimitsOfTheExchangeAreSettingsOfTheOwnerAndApplyAtOnce(t *testing.T) {
	b, _, _, priv := journalBroker(t)
	if err := b.startPeers(priv); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.stopPeers)
	if l := b.peerMgr.Limits(); l.EventsPerMin != 300 || l.BytesPerDay != 50<<20 || l.LamportPerHour != 1000 || l.Sources != 16 {
		t.Fatalf("the defaults: %+v", l)
	}
	for _, bad := range []string{"0", "-1", "many", "5.5", "9999999999"} {
		if _, e := b.setConfig("", "peer.limit.events_per_minute", bad, nil); e == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
	for key, val := range map[string]string{"peer.limit.events_per_minute": "7", "peer.limit.mib_per_day": "2", "peer.limit.lamport_per_hour": "30", "peer.limit.sources": "3"} {
		if _, e := b.setConfig("", key, val, nil); e != nil {
			t.Fatalf("%s: %v", key, e)
		}
	}
	if l := b.peerMgr.Limits(); l.EventsPerMin != 7 || l.BytesPerDay != 2<<20 || l.LamportPerHour != 30 || l.Sources != 3 {
		t.Fatalf("applied: %+v", l)
	}
	var found int
	for _, it := range b.configList("").Items {
		if strings.HasPrefix(it.Key, "peer.limit.") {
			found++
			if it.Source != "setting" {
				t.Errorf("%+v", it)
			}
		}
	}
	if found != 4 {
		t.Fatalf("%d limits in the list of settings", found)
	}
}

func TestQuarantineByTheOwnerIsLoudMarksWhatTheNodeGaveAndIsLiftedByTheOwner(t *testing.T) {
	p := newTwoBrokers(t)
	k := newKey(t)
	register(t, p.a, k)
	alice, adm := p.a.connect(t), p.a.connect(t)
	bob := p.b.mustJoin(t, p.tok, "bob", newKey(t))
	if _, code := alice.join(p.tok, "alice", proto.LegacyProto); code != "" {
		t.Fatalf("alice joins: %q", code)
	}
	bobAtA := remote("bob", p.b)
	if _, err := bob.do("send", "msg", &proto.SendArgs{Room: proto.RoomCommon, Text: "bob before"}, nil); err != nil {
		t.Fatal(err)
	}
	next(t, alice, "bob's message", func(m proto.Message, _ bool) bool { return m.Text == "bob before" })

	if _, code := adminCall(t, adm, newKey(t), "peer_quarantine", proto.PeerQuarantine{Origin: p.b.origin}); code != "admin_only" {
		t.Fatalf("without the owner's signature: %q", code)
	}
	if _, code := adminCall(t, adm, k, "peer_quarantine", proto.PeerQuarantine{Origin: "abc"}); code != "bad_args" {
		t.Fatalf("a bad origin: %q", code)
	}
	if _, code := adminCall(t, adm, k, "peer_release", proto.PeerQuarantine{Origin: p.b.origin}); code != "not_quarantined" {
		t.Fatalf("releasing what is not in: %q", code)
	}
	if _, code := adminCall(t, adm, k, "peer_quarantine", proto.PeerQuarantine{Origin: p.b.origin, Reason: "it spams"}); code != "" {
		t.Fatalf("quarantine: %q", code)
	}
	if _, code := adminCall(t, adm, k, "peer_quarantine", proto.PeerQuarantine{Origin: p.b.origin}); code != "already_quarantined" {
		t.Fatalf("twice: %q", code)
	}
	// Loud: the chat shared with the node hears it, with the reason.
	deadline := time.After(3 * time.Second)
	for heard := false; !heard; {
		select {
		case ev := <-alice.events:
			heard = ev.Kind == "notice" && strings.Contains(ev.Text, "in quarantine (it spams)")
		case <-deadline:
			t.Fatal("the chat did not hear of the quarantine")
		}
	}

	// What it gave stays and shows with the mark: in the history and among the members.
	var h proto.HistoryResult
	if code := alice.call("history", proto.HistoryArgs{Room: proto.RoomCommon}, &h); code != "" {
		t.Fatalf("history: %q", code)
	}
	marked := false
	for _, m := range h.Messages {
		if m.From == bobAtA && m.Text == "bob before" {
			marked = m.Quarantined
		}
	}
	if !marked {
		t.Fatalf("the message of a node in quarantine is not marked: %+v", h.Messages)
	}
	if m := memberInfo(t, alice, bobAtA); !m.Quarantined {
		t.Fatalf("the member: %+v", m)
	}
	var l proto.PeerList
	if code := alice.call("peer_list", nil, &l); code != "" || len(l.Quarantined) != 1 || !strings.Contains(l.Quarantined[0], "it spams") || !strings.Contains(l.Peers[0].Quarantine, "it spams") {
		t.Fatalf("peer list: %q %+v", code, l)
	}

	// Lifted by the owner: the marks go.
	if _, code := adminCall(t, adm, k, "peer_release", proto.PeerQuarantine{Origin: p.b.origin}); code != "" {
		t.Fatalf("release: %q", code)
	}
	alice.call("history", proto.HistoryArgs{Room: proto.RoomCommon}, &h)
	for _, m := range h.Messages {
		if m.Quarantined {
			t.Fatalf("a mark stayed: %+v", m)
		}
	}
	if m := memberInfo(t, alice, bobAtA); m.Quarantined {
		t.Fatalf("the member: %+v", m)
	}
}
