package peer

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/events"
	"github.com/PHPCraftdream/cc-quant-chat/internal/node"
)

// rogue is a node of the tests that speaks the protocol with a key of its own and sends whatever it
// likes: what a node that lies would do.
type rogue struct {
	t      *testing.T
	origin string
	pub    ed25519.PublicKey
	priv   ed25519.PrivateKey
	conn   *tls.Conn
	sc     *bufio.Scanner
	enc    *json.Encoder
}

func newRogue(t *testing.T) *rogue {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &rogue{t: t, origin: node.Origin(pub), pub: pub, priv: priv}
}

// connect dials the target (which must know the rogue as a peer) and says hello for one chat.
func (r *rogue) connect(target *tnode, chat string) error {
	cfg, err := tlsConfig(r.priv, false, func(string, ed25519.PublicKey) error { return nil })
	if err != nil {
		return err
	}
	conn, err := tls.Dial("tcp", target.addr, cfg)
	if err != nil {
		return err
	}
	r.conn, r.enc = conn, json.NewEncoder(conn)
	r.sc = bufio.NewScanner(conn)
	r.sc.Buffer(make([]byte, 64<<10), maxFrame)
	if err := r.enc.Encode(Frame{T: "hello", Origin: r.origin, Chats: []string{chat}}); err != nil {
		return err
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if !r.sc.Scan() { // the target's hello; a refused client learns it here
		if err := r.sc.Err(); err != nil {
			return err
		}
		return io.EOF
	}
	_ = conn.SetReadDeadline(time.Time{})
	r.t.Cleanup(func() { conn.Close() })
	return nil
}

func (r *rogue) event(chat string, seq int64, prev string, lamport int64, kind, payload string) events.Event {
	e := events.Event{ChatID: chat, Origin: r.origin, Seq: seq, Prev: prev, Lamport: lamport, Kind: kind, Payload: payload,
		Assigned: "{}", TS: time.Now().UTC().Format(time.RFC3339)}
	if kind == "msg" { // a message says who wrote it, as the node that took it recorded
		e.Assigned = fmt.Sprintf(`{"from":"rogue","id":%d,"thread":"r:common"}`, seq)
	}
	e.Sign(r.priv)
	return e
}

func (r *rogue) send(chat string, pubs map[string]string, evs ...events.Event) {
	r.t.Helper()
	if err := r.enc.Encode(Frame{T: "events", Chat: chat, Events: evs, Pubs: pubs}); err != nil {
		r.t.Fatal(err)
	}
}

// victim is a node that listens, shares the chat with a rogue and knows it as a peer.
func victim(t *testing.T, r *rogue) *tnode {
	t.Helper()
	v := newTnode(t, "victim")
	t.Cleanup(func() {
		if t.Failed() {
			v.mu.Lock()
			defer v.mu.Unlock()
			for _, l := range v.logs {
				t.Log("victim log:", l)
			}
		}
	})
	v.listen()
	v.chat(chatX)
	if err := v.db.AddPeer(r.origin, ""); err != nil {
		t.Fatal(err)
	}
	if err := v.db.SharePeerChat(chatX, r.origin); err != nil {
		t.Fatal(err)
	}
	if err := v.m.Refresh(); err != nil {
		t.Fatal(err)
	}
	if err := r.connect(v, chatX); err != nil {
		t.Fatal(err)
	}
	return v
}

const msgPayload = `{"room":"common","text":"x"}`

func TestAnHonestSequenceOfEventsFromAPeerIsKept(t *testing.T) {
	r := newRogue(t)
	v := victim(t, r)
	e1 := r.event(chatX, 1, "", 1, "msg", msgPayload)
	e2 := r.event(chatX, 2, e1.ID(), 2, "msg", msgPayload)
	r.send(chatX, nil, e1, e2)
	eventually(t, wait, "both events", func() bool { return v.holds(chatX, e1) && v.holds(chatX, e2) })
	if rep, err := v.db.Verify(chatX); err != nil || len(rep.Issues) != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
}

func TestEventsThatDoNotPassTheChecksAreNotKeptAndTheExchangeGoesOn(t *testing.T) {
	other := newRogue(t) // another key: the "foreign" signer
	cases := []struct {
		name string
		make func(r *rogue, good events.Event) events.Event // the bad event, the second of the chain
		log  string
	}{
		{"a payload changed after the node signed", func(r *rogue, good events.Event) events.Event {
			e := r.event(chatX, 2, good.ID(), 2, "msg", msgPayload)
			e.Payload = `{"room":"common","text":"forged"}`
			return e
		}, "node's signature does not verify"},
		{"a signature of another key", func(r *rogue, good events.Event) events.Event {
			e := other.event(chatX, 2, good.ID(), 2, "msg", msgPayload)
			e.Origin = r.origin
			return e
		}, "node's signature does not verify"},
		{"a broken chain (prev)", func(r *rogue, good events.Event) events.Event {
			return r.event(chatX, 2, "0000000000000000000000000000000000000000000000000000000000000000", 2, "msg", msgPayload)
		}, "was not kept"},
		{"a clock far ahead", func(r *rogue, good events.Event) events.Event {
			return r.event(chatX, 2, good.ID(), 1+lamportJump+1, "msg", msgPayload)
		}, "ahead"},
		{"a clock that goes back", func(r *rogue, good events.Event) events.Event {
			return r.event(chatX, 2, good.ID(), 0, "msg", msgPayload)
		}, "behind"},
		{"an author's signature that does not verify", func(r *rogue, good events.Event) events.Event {
			e := r.event(chatX, 2, good.ID(), 2, "msg", msgPayload)
			e.AuthorPub = base64.StdEncoding.EncodeToString(other.pub)
			e.Sig = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
			e.Sign(r.priv) // the node vouches for the lie; the author does not
			return e
		}, "author's signature"},
		{"a malformed header", func(r *rogue, good events.Event) events.Event {
			return r.event(chatX, 2, good.ID(), 2, "NOT A KIND!", msgPayload)
		}, "malformed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRogue(t)
			v := victim(t, r)
			good := r.event(chatX, 1, "", 1, "msg", msgPayload)
			bad := c.make(r, good)
			next := r.event(chatX, 3, bad.ID(), 3, "msg", msgPayload) // follows the bad one: cannot be kept either
			r.send(chatX, nil, good, bad, next)
			eventually(t, wait, "the good event", func() bool { return v.holds(chatX, good) })
			eventually(t, wait, "the reason in the log", func() bool { return v.logged(c.log) })
			time.Sleep(200 * time.Millisecond)
			if got := v.journal(chatX); len(got) != 1 {
				t.Fatalf("kept %v; only the first event was good", got)
			}
			// The exchange goes on: the rogue's honest events still come in (the good one's successor).
			again := r.event(chatX, 2, good.ID(), 2, "msg", msgPayload)
			r.send(chatX, nil, again)
			eventually(t, wait, "an honest event after the refused one", func() bool { return v.holds(chatX, again) })
		})
	}
}

func TestAKeyOfferedForAnOriginMustHashToIt(t *testing.T) {
	r, third := newRogue(t), newRogue(t)
	v := victim(t, r)
	e := third.event(chatX, 1, "", 1, "msg", msgPayload)
	// The rogue offers its own key as the third node's.
	r.send(chatX, map[string]string{third.origin: base64.StdEncoding.EncodeToString(r.pub)}, e)
	eventually(t, wait, "the refusal", func() bool { return v.logged("does not hash to its origin") })
	if len(v.journal(chatX)) != 0 {
		t.Fatal("an event was kept under a key that is not its origin's")
	}
	// The right key is taken, and the third node's events are kept (they come through a peer).
	r.send(chatX, map[string]string{third.origin: base64.StdEncoding.EncodeToString(third.pub)}, e)
	eventually(t, wait, "the event with the right key", func() bool { return v.holds(chatX, e) })
}

func TestAForkFlagsTheNodeEndsItsSessionAndKeepsItOut(t *testing.T) {
	r := newRogue(t)
	v := victim(t, r)
	first := r.event(chatX, 1, "", 1, "msg", `{"room":"common","text":"what I said"}`)
	r.send(chatX, nil, first)
	eventually(t, wait, "the first event", func() bool { return v.holds(chatX, first) })

	// A different event with the same origin and seq: the node tells two stories.
	other := r.event(chatX, 1, "", 1, "msg", `{"room":"common","text":"what I say now"}`)
	r.send(chatX, nil, other)
	eventually(t, wait, "the node is flagged", func() bool {
		_, flagged, _ := v.db.Flagged(r.origin)
		return flagged
	})
	if reason, _, _ := v.db.Flagged(r.origin); reason != "fork" {
		t.Fatalf("reason %q", reason)
	}
	eventually(t, wait, "the session is gone", func() bool { return len(v.m.Sessions()) == 0 })
	if !v.holds(chatX, first) || v.holds(chatX, other) {
		t.Fatal("the journal must keep the first story only")
	}
	// It cannot come back, and nothing it still sends is taken.
	if err := r.connect(v, chatX); err == nil {
		t.Fatal("a flagged node connected again")
	}
	third := r.event(chatX, 2, first.ID(), 2, "msg", msgPayload)
	if err := v.m.accept(chatX, third, nil); err == nil {
		t.Fatal("an event of a flagged node was accepted")
	}
}

func TestARefusedEventIsNotAskedForInALoopAndOthersGoOn(t *testing.T) {
	r, third := newRogue(t), newRogue(t)
	v := victim(t, r)

	var mu, emu sync.Mutex // mu: the count; emu: the encoder
	vectors := 0
	bad := r.event(chatX, 1, "", 1+lamportJump+1, "msg", msgPayload) // a clock far ahead: refused for good
	go func() {                                                      // an honest peer: it sends again what the vector says is lacking
		for r.sc.Scan() {
			var f Frame
			if json.Unmarshal(r.sc.Bytes(), &f) != nil || f.T != "vector" {
				continue
			}
			mu.Lock()
			vectors++
			mu.Unlock()
			if f.Vec[r.origin] < bad.Seq {
				emu.Lock()
				_ = r.enc.Encode(Frame{T: "events", Chat: chatX, Events: []events.Event{bad}})
				emu.Unlock()
			}
		}
	}()
	emu.Lock()
	r.send(chatX, nil, bad)
	emu.Unlock()
	eventually(t, wait, "the refusal in the log", func() bool { return v.logged("was not kept") })

	// Another origin in the same chat goes on, through the same peer.
	e := third.event(chatX, 1, "", 1, "msg", msgPayload)
	emu.Lock()
	r.send(chatX, map[string]string{third.origin: base64.StdEncoding.EncodeToString(third.pub)}, e)
	emu.Unlock()
	eventually(t, wait, "the event of another origin", func() bool { return v.holds(chatX, e) })

	time.Sleep(1500 * time.Millisecond)
	mu.Lock()
	got := vectors
	mu.Unlock()
	if got > 5 { // the first one after hello, one per frame answered; a loop would be hundreds
		t.Fatalf("%d vectors in a moment: the refused event is being asked for again and again", got)
	}
	if n := v.logCount("was not kept"); n != 1 {
		t.Fatalf("the refusal was logged %d times, want once", n)
	}
	if len(v.m.Sessions()) != 1 {
		t.Fatal("the session of a peer that was refused once was closed")
	}
}

func TestUnfinishedHandshakesAreCapped(t *testing.T) {
	a := newTnode(t, "alice")
	a.listen()
	var conns []net.Conn
	for i := 0; i < maxHandshakes+8; i++ {
		c, err := net.Dial("tcp", a.addr)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
		t.Cleanup(func() { c.Close() })
	}
	time.Sleep(300 * time.Millisecond)
	closed := 0
	for _, c := range conns {
		_ = c.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		if _, err := c.Read(make([]byte, 1)); err != nil && !os.IsTimeout(err) {
			closed++
		}
	}
	if closed < 8 {
		t.Fatalf("%d of %d silent connections were dropped, want at least 8", closed, len(conns))
	}
}
