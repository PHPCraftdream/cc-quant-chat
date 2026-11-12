package broker

import (
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/node"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store/sqlite"
)

// twoBrokers: two brokers on their own databases, with node keys, that have the same chat and
// exchange it over loopback. a dials b; b has `peer.listen`, a has not.
type twoBrokers struct {
	a, b     *Broker
	adb, bdb *sqlite.DB
	tok, id  string
}

func newTwoBrokers(t *testing.T) *twoBrokers {
	t.Helper()
	a, adb, _, apriv := journalBroker(t)
	b, bdb, _, bpriv := journalBroker(t)
	tok, id := token(t)
	for _, db := range []*sqlite.DB{adb, bdb} {
		if _, err := db.Load(id, "team"); err != nil {
			t.Fatal(err)
		}
	}
	if _, e := b.setConfig("", "peer.listen", "127.0.0.1:0", nil); e != nil {
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
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(adb.AddPeer(b.origin, "tcp://"+b.peerAt))
	must(adb.SharePeerChat(id, b.origin))
	must(bdb.AddPeer(a.origin, ""))
	must(bdb.SharePeerChat(id, a.origin))
	must(a.peerMgr.Refresh())
	must(b.peerMgr.Refresh())
	return &twoBrokers{a, b, adb, bdb, tok, id}
}

// remote is how the broker of origin sees a member of the other node.
func remote(nick string, of *Broker) string { return nick + "@" + node.Short(of.origin) }

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 600; i++ {
		if ok() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("not within 15 seconds: %s", what)
}

// next waits for the next message event of c that fits.
func next(t *testing.T, c *client, what string, fits func(proto.Message, bool) bool) proto.Message {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case ev := <-c.events:
			if ev.Kind == "msg" && ev.Msg != nil && fits(*ev.Msg, ev.ToMe) {
				return *ev.Msg
			}
		case <-deadline:
			t.Fatalf("no message event: %s", what)
		}
	}
}

func hasMember(t *testing.T, c *client, name string) func() bool {
	return func() bool {
		var st proto.State
		if code := c.call("state", nil, &st); code != "" {
			t.Fatalf("state: %q", code)
		}
		for _, m := range st.Members {
			if m.Name == name {
				return true
			}
		}
		return false
	}
}

func TestMembersAndMessagesOfTwoNodesMeetAndTheProjectionsAgree(t *testing.T) {
	p := newTwoBrokers(t)
	alice := p.a.connect(t)
	bob := p.b.mustJoin(t, p.tok, "bob", newKey(t)) // signs: his key is registered at his node
	if _, code := alice.join(p.tok, "alice", proto.ProtoVersion); code != "" {
		t.Fatalf("alice joins: %q", code)
	}
	bobAtA, aliceAtB := remote("bob", p.b), remote("alice", p.a)
	eventually(t, "each node knows the member of the other", func() bool {
		return hasMember(t, alice, bobAtA)() && hasMember(t, bob.client, aliceAtB)()
	})
	if m := memberInfo(t, bob.client, aliceAtB); m.Unsigned || m.Online {
		t.Fatalf("a member of another node: %+v", m)
	}
	if m := memberInfo(t, alice, bobAtA); m.Unsigned || m.Online {
		t.Fatalf("a member of another node that signs: %+v", m)
	}

	// A message from alice reaches bob under the name bob knows her by.
	aliceID, code := alice.send(proto.RoomCommon, "", "hello from a")
	mustCode(t, "alice sends", code)
	got := next(t, bob.client, "alice's message", func(m proto.Message, _ bool) bool { return m.Text == "hello from a" })
	if got.From != aliceAtB || got.Room != proto.RoomCommon {
		t.Fatalf("the message as bob sees it: %+v", got)
	}
	// Bob replies to it: the reply names the message by its number at bob's node, and arrives at
	// alice naming hers.
	if _, err := bob.do("send", "msg", &proto.SendArgs{Room: proto.RoomCommon, Text: "hello back", ReplyTo: got.ID}, nil); err != nil {
		t.Fatal(err)
	}
	back := next(t, alice, "bob's reply", func(m proto.Message, _ bool) bool { return m.Text == "hello back" })
	if back.From != bobAtA || back.ReplyTo != aliceID {
		t.Fatalf("the reply as alice sees it: %+v (her message is #%d)", back, aliceID)
	}

	// A message addressed to a member of the other node wakes that member: the name carries the node.
	if _, code := alice.send(proto.RoomCommon, bobAtA, "for bob only"); code != "" {
		t.Fatalf("addressed to a member of another node: %q", code)
	}
	next(t, bob.client, "the addressed message", func(m proto.Message, toMe bool) bool { return m.Text == "for bob only" && toMe && m.To == "bob" })
	if _, err := bob.do("send", "msg", &proto.SendArgs{Room: proto.RoomCommon, To: aliceAtB, Text: "for alice only"}, nil); err != nil {
		t.Fatal(err)
	}
	next(t, alice, "the message addressed to alice", func(m proto.Message, toMe bool) bool { return m.Text == "for alice only" && toMe && m.To == "alice" })

	// A direct message across nodes says why it cannot be sent.
	if _, code := alice.send(proto.RoomDirect, bobAtA, "psst"); code != "remote_member" {
		t.Fatalf("a direct message to another node: %q", code)
	}

	// Both nodes hold the same messages in the same order, and a rebuild changes nothing.
	same := func() bool {
		return textsOf(t, p.adb, p.id) == textsOf(t, p.bdb, p.id) && textsOf(t, p.adb, p.id) != ""
	}
	eventually(t, "the same messages on both nodes", same)
	consistent(t, p.adb, "the exchange (node a)")
	consistent(t, p.bdb, "the exchange (node b)")
}

// textsOf is the words of the messages of a chat, sorted: the numbers and the names differ between
// nodes, the set of messages does not.
func textsOf(t *testing.T, db *sqlite.DB, id string) string {
	t.Helper()
	recs, err := db.Load(id, "")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, r := range recs {
		if r.T == "msg" && r.Msg != nil {
			seen[r.Msg.Text] = true
		}
	}
	out := ""
	for _, k := range sortedKeys(seen) {
		out += k + "|"
	}
	return out
}

// A message of another node arrives while nobody is present at this node (the chat is not in
// memory); the next message written here must not meet its number.
func TestAForeignMessageTakenWhileTheChatIsNotLoadedDoesNotTakeTheNumberOfTheNextOne(t *testing.T) {
	p := newTwoBrokers(t)
	alice := p.a.connect(t)
	if _, code := alice.join(p.tok, "alice", proto.ProtoVersion); code != "" {
		t.Fatalf("alice joins: %q", code)
	}
	for _, text := range []string{"one", "two", "three"} {
		mustCode(t, "alice sends", func() string { _, c := alice.send(proto.RoomCommon, "", text); return c }())
	}
	eventually(t, "the messages reach b, where nobody is", func() bool { return textsOf(t, p.bdb, p.id) == "one|three|two|" })
	p.b.mu.Lock()
	loaded := len(p.b.chats)
	p.b.mu.Unlock()
	if loaded != 0 {
		t.Fatalf("the chat of b is in memory with nobody present (%d)", loaded)
	}

	bob := p.b.connect(t)
	if _, code := bob.join(p.tok, "bob", proto.ProtoVersion); code != "" {
		t.Fatalf("bob joins: %q", code)
	}
	id, code := bob.send(proto.RoomCommon, "", "four")
	if code != "" || id != 4 {
		t.Fatalf("the next number after three messages of another node: %d %q", id, code)
	}
	eventually(t, "the same messages on both nodes", func() bool {
		return textsOf(t, p.adb, p.id) == textsOf(t, p.bdb, p.id) && strings.Contains(textsOf(t, p.adb, p.id), "four")
	})
	consistent(t, p.bdb, "a message after a foreign one (node b)")
}

// A node that takes away or replaces the key of one of its members says so to the chat: from then
// on it speaks for that nick without a signature.
func TestTheKeyOfAMemberOfAnotherNodeBeingResetIsToldToTheChat(t *testing.T) {
	p := newTwoBrokers(t)
	alice := p.a.connect(t)
	if _, code := alice.join(p.tok, "alice", proto.ProtoVersion); code != "" {
		t.Fatalf("alice joins: %q", code)
	}
	bob := p.b.mustJoin(t, p.tok, "bob", newKey(t))
	eventually(t, "alice knows bob", hasMember(t, alice, remote("bob", p.b)))
	presenceTexts(alice)                                                                 // what was said so far
	if err := p.bdb.Append(p.id, store.Record{T: "nick_key", Nick: "bob"}); err != nil { // the node of bob resets him
		t.Fatal(err)
	}
	var told bool
	for i := 0; i < 40 && !told; i++ {
		for _, line := range presenceTexts(alice) {
			told = told || strings.Contains(line, "the key of "+remote("bob", p.b)+" was reset by its node")
		}
	}
	if !told {
		t.Fatal("the chat was not told that the key of a member of another node was reset")
	}
	_ = bob
}
