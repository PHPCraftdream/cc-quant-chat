package peer

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/invite"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// pairing records what a node's manager says about pairing.
type pairing struct {
	mu     sync.Mutex
	events []PairEvent
	active []string
}

func (p *pairing) kinds() map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]int{}
	for _, e := range p.events {
		out[e.Kind]++
	}
	return out
}

func watchPairing(n *tnode) *pairing {
	p := &pairing{}
	n.m.OnPair = func(e PairEvent) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.events = append(p.events, e)
	}
	n.m.OnActive = func(origin string) {
		chats, _ := n.db.PendingChats(origin)
		for _, c := range chats {
			if _, err := n.db.Load(c.ID, c.Name); err != nil {
				n.t.Error(err)
			}
			if err := n.db.SharePeerChat(c.ID, origin); err != nil {
				n.t.Error(err)
			}
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		p.active = append(p.active, origin)
	}
	return p
}

// invitationOf: a issues an invitation of a node that offers the chats and gives the level.
func invitationOf(t *testing.T, a *tnode, level string, chats ...store.ChatRef) invite.Invitation {
	t.Helper()
	in, err := invite.New(invite.KindNode, 0, time.Now(), func(i *invite.Invitation) {
		i.Addrs = []string{"tcp://" + a.addr}
		i.Key = base64.StdEncoding.EncodeToString(a.pub)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.db.IssueNodeInvite(invite.NewRecord(in, store.NodeScope{Trust: level, Chats: chats}.String(), "owner", time.Now())); err != nil {
		t.Fatal(err)
	}
	return in
}

func peerOf(t *testing.T, n *tnode, origin string) (store.Peer, bool) {
	t.Helper()
	ps, err := n.db.Peers()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.Origin == origin {
			return p, true
		}
	}
	return store.Peer{}, false
}

func TestTwoNodesPairByInvitationAndExchangeOnlyAfterBothOwnersConfirmTheSameCode(t *testing.T) {
	a, b := newTnode(t, "alice"), newTnode(t, "bob")
	pa, pb := watchPairing(a), watchPairing(b)
	a.listen()
	a.chat(chatX)
	in := invitationOf(t, a, "member", store.ChatRef{ID: chatX, Name: "team"})

	res, err := b.m.Join(context.Background(), in, "trusted")
	if err != nil {
		t.Fatal(err)
	}
	if res.Origin != a.origin || len(res.Code) != 6 || len(res.Chats) != 1 || res.Chats[0].Name != "team" {
		t.Fatalf("%+v", res)
	}
	// Both sides computed the same code, each from its own end of the connection.
	if code, ok, _ := a.db.PendingCode(b.origin); !ok || code != res.Code {
		t.Fatalf("alice has %q (%v), bob %q", code, ok, res.Code)
	}
	// The levels: alice gives what the invitation says, bob what he chose.
	if p, _ := peerOf(t, a, b.origin); p.State != "pending" || p.Trust != "member" {
		t.Fatalf("alice: %+v", p)
	}
	if p, _ := peerOf(t, b, a.origin); p.State != "pending" || p.Trust != "trusted" || len(p.Chats) != 0 {
		t.Fatalf("bob: %+v", p)
	}
	if got := pa.kinds()[PairUsed]; got != 1 {
		t.Fatalf("alice was not told of the use: %v", pa.kinds())
	}
	if _, err := b.m.Join(context.Background(), in, "member"); !errors.Is(err, errInviteRefused) {
		t.Fatalf("a second use: %v", err)
	}

	// Pending: the nodes see each other, nothing of the chat moves; bob has not even got the chat.
	eventually(t, wait, "pending sessions", func() bool { return len(a.m.Sessions()) == 1 && len(b.m.Sessions()) == 1 })
	a.say(chatX, "before the code")
	time.Sleep(500 * time.Millisecond)
	if _, err := b.db.Vector(chatX); err == nil && len(b.journal(chatX)) != 0 {
		t.Fatal("events went to a node that is not confirmed")
	}

	// Alice enters a wrong code, then the right one: she is confirmed, the node is not yet active.
	if r, err := a.m.ConfirmCode(b.origin, "000000"); err != nil || r.Right || r.Tries != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	if r, err := a.m.ConfirmCode(b.origin, res.Code); err != nil || !r.Right || r.Active {
		t.Fatalf("%+v %v", r, err)
	}
	time.Sleep(300 * time.Millisecond)
	if p, _ := peerOf(t, b, a.origin); p.State != "pending" {
		t.Fatalf("bob became active with only alice's confirmation: %+v", p)
	}
	if r, err := b.m.ConfirmCode(a.origin, res.Code); err != nil || !r.Right || !r.Active {
		t.Fatalf("%+v %v", r, err)
	}
	eventually(t, wait, "alice active", func() bool { p, _ := peerOf(t, a, b.origin); return p.State == "active" })
	if len(pb.active) != 1 || len(pa.active) != 1 {
		t.Fatalf("OnActive: bob %v, alice %v", pb.active, pa.active)
	}
	// The chat offered is made by bob under the same id and name, and the old message arrives.
	eventually(t, wait, "the exchange", func() bool { return len(b.journal(chatX)) > 0 && sameJournal(a.journal(chatX), b.journal(chatX)) })
	before := len(b.journal(chatX))
	b.say(chatX, "after the code")
	eventually(t, wait, "bob's event reaches alice", func() bool {
		return sameJournal(a.journal(chatX), b.journal(chatX)) && len(a.journal(chatX)) == before+1
	})
}

func TestAnUnknownKeyWithNoInvitationGetsNothingAndIsNotListed(t *testing.T) {
	a, stranger := newTnode(t, "alice"), newTnode(t, "mallory")
	a.listen()
	a.chat(chatX)
	conn := rawDial(t, a.addr, stranger)
	defer conn.Close()
	// A frame of the exchange, not an invitation: the connection is closed, nothing is answered.
	_ = json.NewEncoder(conn).Encode(Frame{T: "hello", Origin: stranger.origin, Chats: []string{chatX}})
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if sc := bufio.NewScanner(conn); sc.Scan() {
		t.Fatalf("a stranger was answered: %s", sc.Text())
	}
	if ps, _ := a.db.Peers(); len(ps) != 0 {
		t.Fatalf("%+v", ps)
	}
	if _, ok, _ := a.db.OriginKey(stranger.origin); ok {
		t.Fatal("the key of a stranger was kept")
	}
}

func rawDial(t *testing.T, addr string, as *tnode) *tls.Conn {
	t.Helper()
	cfg, err := tlsConfig(as.priv, false, func(string, ed25519.PublicKey) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	conn, err := tls.Dial("tcp", addr, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestAProofMadeOnAnotherConnectionDoesNotPassAndCountsAgainstTheInvitation(t *testing.T) {
	a, b := newTnode(t, "alice"), newTnode(t, "bob")
	pa := watchPairing(a)
	a.listen()
	in := invitationOf(t, a, "member")
	use := func(sig func(c1 *tls.Conn) string) Frame {
		c1 := rawDial(t, a.addr, b)
		defer c1.Close()
		c2 := rawDial(t, a.addr, b)
		defer c2.Close()
		_ = json.NewEncoder(c2).Encode(Frame{T: "invite", ID: in.ID(), Sig: sig(c1)})
		_ = c2.SetReadDeadline(time.Now().Add(5 * time.Second))
		var f Frame
		sc := bufio.NewScanner(c2)
		if sc.Scan() {
			_ = json.Unmarshal(sc.Bytes(), &f)
		}
		return f
	}
	// Signed with the keying material of the first connection, shown on the second.
	other := func(c1 *tls.Conn) string {
		cs := c1.ConnectionState()
		exp, _ := cs.ExportKeyingMaterial(exporterLabel, nil, 32)
		return base64.StdEncoding.EncodeToString(in.Proof(exp, subject(a.pub, b.pub)))
	}
	for i := 0; i < invite.MaxTries; i++ {
		if f := use(other); f.T != "invite_invalid" {
			t.Fatalf("try %d: %+v", i, f)
		}
	}
	if pa.kinds()[PairBad] != invite.MaxTries {
		t.Fatalf("alice was not told: %v", pa.kinds())
	}
	// Burnt: even the holder of the invitation, with a good proof on its own connection, is refused.
	if _, err := b.m.Join(context.Background(), in, "member"); !errors.Is(err, errInviteRefused) {
		t.Fatalf("a burnt invitation: %v", err)
	}
	if ps, _ := a.db.Peers(); len(ps) != 0 {
		t.Fatalf("%+v", ps)
	}
}

func TestJoiningRefusesACertificateOfAnotherKeyBeforeAnythingIsSaid(t *testing.T) {
	a, b, impostor := newTnode(t, "alice"), newTnode(t, "bob"), newTnode(t, "mallory")
	impostor.listen()
	in := invitationOf(t, a, "member")
	in.Addrs = []string{"tcp://" + impostor.addr} // the address leads to another node
	if _, err := b.m.Join(context.Background(), in, "member"); err == nil || errors.Is(err, errInviteRefused) {
		t.Fatalf("%v", err)
	}
	if ps, _ := b.db.Peers(); len(ps) != 0 {
		t.Fatalf("%+v", ps)
	}
	if ps, _ := impostor.db.Peers(); len(ps) != 0 {
		t.Fatalf("the impostor learnt something: %+v", ps)
	}
}

func TestConnectionsOfUnknownKeysAreLimitedPerMinute(t *testing.T) {
	a := newTnode(t, "alice")
	for i := 0; i < strangerPerMinute; i++ {
		if !a.m.strangerAllowed() {
			t.Fatalf("refused at %d", i)
		}
	}
	if a.m.strangerAllowed() {
		t.Fatal("more than the limit in a minute")
	}
}

func TestAPendingNodeThatIsNotConfirmedInTimeIsDroppedAndTheOwnerIsTold(t *testing.T) {
	a := newTnode(t, "alice")
	pa := watchPairing(a)
	o := "9f1810d6cddc386de296075d00c90e8d"
	if err := a.db.AddPendingPeer(store.Pending{Origin: o, Trust: "member", Until: time.Now().Add(-time.Second), Code: "123456"}); err != nil {
		t.Fatal(err)
	}
	a.m.expire()
	if _, ok := peerOf(t, a, o); ok {
		t.Fatal("the node stayed")
	}
	if pa.kinds()[PairDropped] != 1 {
		t.Fatalf("%v", pa.kinds())
	}
}
