package broker

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	cl "github.com/PHPCraftdream/cc-quant-chat/internal/client"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

func newTestBroker(be store.Backend) *Broker {
	return &Broker{
		store: be, storeDesc: "test",
		chats: map[string]*chat{}, conns: map[*conn]bool{}, hits: map[string][]time.Time{},
		idle: time.Hour, grace: time.Hour, started: time.Now(), origin: "0123456789abcdef0123456789abcdef",
	}
}

// client is a minimal peer over net.Pipe.
type client struct {
	b       *Broker    // the broker it is connected to
	sg      *cl.Signer // the signer of its nick: its writes are signed by call (nil until it joined)
	t       *testing.T
	nc      net.Conn
	id      int64
	events  chan proto.Event
	replies chan proto.Frame
	key     string
	lax     bool // a refused or lost connection is an answer ("closed", "timeout"), not a failure of the test
	gateMu  sync.Mutex
	gate    chan struct{} // while set and open, the reader takes no more frames
	ended   chan struct{} // closed when the connection is over
}

func (c *client) hold() chan struct{} {
	g := make(chan struct{})
	c.gateMu.Lock()
	c.gate = g
	c.gateMu.Unlock()
	return g
}

func (b *Broker) connect(t *testing.T) *client {
	t.Helper()
	srv, cl := net.Pipe()
	b.serving.Add(1)
	go b.serve(srv)
	c := &client{b: b, t: t, nc: cl, events: make(chan proto.Event, 256), replies: make(chan proto.Frame, 16), ended: make(chan struct{})}
	go func() {
		defer close(c.ended)
		sc := bufio.NewScanner(cl)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			c.gateMu.Lock()
			g := c.gate
			c.gateMu.Unlock()
			if g != nil {
				<-g
			}
			var f proto.Frame
			if json.Unmarshal(sc.Bytes(), &f) != nil {
				continue
			}
			if f.Op == "event" {
				var ev proto.Event
				_ = json.Unmarshal(f.Data, &ev)
				c.events <- ev
			} else {
				c.replies <- f
			}
		}
	}()
	t.Cleanup(func() { cl.Close() })
	return c
}

// call sends a request and returns the data and the error code ("" on success).
func (c *client) call(op string, args, out any) string {
	c.t.Helper()
	var pe *proto.Error
	if errors.As(c.callSigned(op, args, out), &pe) {
		return pe.Code
	}
	return ""
}

// callSigned is callE, with the writes of a joined nick signed as its client does (a message, a board
// change, a document version); what is not a write goes as it is.
func (c *client) callSigned(op string, args, out any) error {
	c.t.Helper()
	if c.sg != nil {
		var st proto.Stamper
		kind := ""
		switch a := args.(type) {
		case proto.SendArgs:
			st, kind = &a, "msg"
		case *proto.SendArgs:
			st, kind = a, "msg"
		case proto.BoardArgs:
			if proto.BoardWrite(a.Op) {
				st, kind = &a, a.Op
			}
		case *proto.BoardArgs:
			if proto.BoardWrite(a.Op) {
				st, kind = a, a.Op
			}
		case proto.DocArgs:
			if proto.DocWrite(a.Op) {
				st, kind = &a, "doc."+a.Op
			}
		case *proto.DocArgs:
			if proto.DocWrite(a.Op) {
				st, kind = a, "doc."+a.Op
			}
		}
		if st != nil {
			return c.sg.Write(c.callE, op, kind, st, out)
		}
	}
	return c.callE(op, args, out)
}

// callE is call returning the broker's error whole, with its data.
func (c *client) callE(op string, args, out any) error {
	c.t.Helper()
	c.id++
	raw, _ := json.Marshal(args)
	f, _ := json.Marshal(proto.Frame{Op: op, ID: c.id, Args: raw})
	if _, err := c.nc.Write(append(f, '\n')); err != nil {
		if c.lax {
			return &proto.Error{Code: "closed"}
		}
		c.t.Fatalf("write %s: %v", op, err)
	}
	select {
	case r := <-c.replies:
		return c.answer(op, r, out)
	case <-c.ended:
		select { // the reply was filed before the connection ended
		case r := <-c.replies:
			return c.answer(op, r, out)
		default:
		}
		if c.lax {
			return &proto.Error{Code: "closed"}
		}
		c.t.Fatalf("%s: connection ended", op)
	case <-time.After(5 * time.Second):
		if c.lax {
			return &proto.Error{Code: "timeout"}
		}
		c.t.Fatalf("%s: no reply", op)
	}
	return nil
}

func (c *client) answer(op string, r proto.Frame, out any) error {
	if r.Op == "err" {
		return &proto.Error{Code: r.Code, Msg: r.Msg, Data: r.Data}
	}
	if out != nil && len(r.Data) > 0 {
		reflect.ValueOf(out).Elem().SetZero() // a reused value keeps the fields a reply leaves out (omitempty)
		if err := json.Unmarshal(r.Data, out); err != nil {
			c.t.Fatalf("%s reply: %v", op, err)
		}
	}
	return nil
}

// join enters the chat as a nick does: it is a protected nick (made when it is not there yet, with a key of its own
// that every client of that name has: a test needs no memory of it), the proof of its auth key, the join; its
// writes are signed from then on. chat is the chat's id; level is a leftover of the tests of the old levels.
func (c *client) join(chat, name string, level int) (proto.Welcome, string) {
	c.t.Helper()
	var w proto.Welcome
	k := nickKeyOf(chat, name)
	if err := c.b.seedProtectedIn(c.t, chat, "team", name, k); err != nil {
		return w, "store_unavailable"
	}
	if code := unlockAs(c.t, c, chat, name, k); code != "" {
		return w, code
	}
	sg, err := cl.JoinSigned(c.callE, proto.JoinArgs{Chat: chat, Name: name}, k, chat, &w)
	if err != nil {
		var pe *proto.Error
		if errors.As(err, &pe) {
			return w, pe.Code
		}
		c.t.Fatalf("join: %v", err)
	}
	c.sg = sg
	return w, ""
}

// nickKeyOf is the key of a test nick: the same for every client that joins under the name.
func nickKeyOf(chat, name string) ed25519.PrivateKey {
	h := sha256.Sum256([]byte("test-nick|" + chat + "|" + name))
	return ed25519.NewKeyFromSeed(h[:])
}

func (c *client) send(room, to, text string) (int64, string) {
	c.t.Helper()
	return c.sendID(room, to, text, "")
}

func (c *client) sendID(room, to, text, cid string) (int64, string) {
	c.t.Helper()
	var r proto.SendResult
	code := c.call("send", proto.SendArgs{Room: room, To: to, Text: text, ClientMsgID: cid}, &r)
	return r.ID, code
}

func (c *client) close() { c.nc.Close() }

// msgs drains the message events that arrived within a short while.
func (c *client) msgs() []int64 {
	var ids []int64
	for {
		select {
		case ev := <-c.events:
			if ev.Kind == "msg" {
				ids = append(ids, ev.Msg.ID)
			}
		case <-time.After(150 * time.Millisecond):
			return ids
		}
	}
}

// waitOffline waits until the broker has noticed that the member is gone.
func (b *Broker) waitOffline(t *testing.T, ch *chat, nick string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		ch.mu.Lock()
		m := ch.members[nick]
		off := m == nil || m.conn == nil
		ch.mu.Unlock()
		if off {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("@%s still online", nick)
}

// token is a chat for a test: its id (twice, as the tests were written when a chat was named by a token).
func token(t *testing.T) (string, string) {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	id := hex.EncodeToString(raw)
	return id, id
}

func (b *Broker) chatOf(id string) *chat {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.chats[id]
}

// inChat runs fn with the broker's lock and the chat's held (ch is nil if the chat is not loaded).
func (b *Broker) inChat(id string, fn func(ch *chat)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := b.chats[id]
	if ch == nil {
		fn(nil)
		return
	}
	ch.mu.Lock()
	defer ch.mu.Unlock()
	fn(ch)
}

func TestJoinRefusedWhenHistoryCannotBeRead(t *testing.T) {
	be := newMemStore()
	b := newTestBroker(be)
	tok, id := token(t)

	be.fail(errors.New("disk on fire"), nil)
	a := b.connect(t)
	if _, code := a.join(tok, "alice", proto.ProtoVersion); code != "store_unavailable" {
		t.Fatalf("code = %q, want store_unavailable", code)
	}
	if b.chatOf(id) != nil {
		t.Fatal("a chat that failed to load must not stay cached")
	}
	if len(be.recs[id]) != 0 {
		t.Fatalf("records written despite the failed load: %+v", be.recs[id])
	}

	be.fail(nil, nil)
	if _, code := a.join(tok, "alice", proto.ProtoVersion); code != "" {
		t.Fatalf("after recovery: code %q", code)
	}
}

func TestNickKeySurvivesBrokerRestartAndIsChecked(t *testing.T) {
	be := newMemStore()
	tok, _ := token(t)
	b1 := newTestBroker(be)
	a := b1.connect(t)
	if _, code := a.join(tok, "alice", proto.ProtoVersion); code != "" {
		t.Fatalf("first join: code %q", code)
	}
	a.close()

	b2 := newTestBroker(be) // restart: everything comes from the store
	if _, err := b2.keyedJoin(t, tok, "alice", newKey(t)); codeOf(err) != "unlock_denied" {
		t.Fatalf("join with another key: %v, want unlock_denied", err)
	}
	if _, code := b2.connect(t).join(tok, "alice", proto.ProtoVersion); code != "" {
		t.Fatalf("join with the key: code %q", code)
	}
}

func TestFailedWriteChangesNothing(t *testing.T) {
	be := newMemStore()
	b := newTestBroker(be)
	tok, id := token(t)
	a, c := b.connect(t), b.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)
	c.join(tok, "bob", proto.ProtoVersion)

	be.fail(nil, errors.New("disk full"))
	if _, code := a.send(proto.RoomCommon, "", "lost?"); code != "store_failed" {
		t.Fatalf("send code = %q, want store_failed", code)
	}
	if got := c.msgs(); len(got) != 0 {
		t.Fatalf("bob received %v for a message that was not stored", got)
	}
	if a.call("room_create", proto.RoomArgs{Room: "ops"}, nil) != "store_failed" {
		t.Fatal("room_create must fail when it can't be stored")
	}
	if b.chatOf(id).rooms["ops"] != nil {
		t.Fatal("the room exists in memory but not in the store")
	}

	be.fail(nil, nil)
	if n, code := a.send(proto.RoomCommon, "", "stored"); code != "" || n != 1 {
		t.Fatalf("after recovery: id %d, code %q; want id 1 (no id was burned)", n, code)
	}
}

func TestDeliveryIsConfirmedByAckNotByDisconnect(t *testing.T) {
	be := newMemStore()
	b := newTestBroker(be)
	tok, id := token(t)
	a, c := b.connect(t), b.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)
	c.join(tok, "bob", proto.ProtoVersion)
	a.send(proto.RoomCommon, "", "one")
	a.send(proto.RoomCommon, "", "two")
	if got := c.msgs(); len(got) != 2 {
		t.Fatalf("live events = %v", got)
	}
	c.close() // the connection ends before bob's session confirmed anything
	b.waitOffline(t, b.chatOf(id), "bob")

	c2 := b.connect(t)
	c2.key = c.key
	w, code := c2.join(tok, "bob", proto.ProtoVersion)
	if code != "" || len(w.Backlog) != 2 {
		t.Fatalf("rejoin: code %q, backlog %d; want both unconfirmed messages again", code, len(w.Backlog))
	}
	if c2.call("ack", proto.AckArgs{ID: 2}, nil) != "" {
		t.Fatal("ack failed")
	}
	c2.close()
	b.waitOffline(t, b.chatOf(id), "bob")

	c3 := b.connect(t)
	c3.key = c.key
	if w, code := c3.join(tok, "bob", proto.ProtoVersion); code != "" || len(w.Backlog) != 0 {
		t.Fatalf("after ack the backlog must be empty, got %d", len(w.Backlog))
	}
	if c3.call("ack", proto.AckArgs{ID: 99}, nil) != "bad_args" {
		t.Fatal("ack beyond the newest message must be refused")
	}
}

func TestRateLimitSurvivesLeaveAndRejoin(t *testing.T) {
	b := newTestBroker(newMemStore())
	tok, _ := token(t)
	a := b.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)
	for i := 0; i < rateLimit; i++ {
		if _, code := a.send(proto.RoomCommon, "", "spam"); code != "" {
			t.Fatalf("message %d: %q", i, code)
		}
	}
	if a.call("leave", nil, nil) != "" {
		t.Fatal("leave failed")
	}
	a2 := b.connect(t)
	a2.key = a.key
	a2.join(tok, "alice", proto.ProtoVersion)
	if _, code := a2.send(proto.RoomCommon, "", "again"); code != "rate_limited" {
		t.Fatalf("code = %q; leaving and rejoining must not reset the limit", code)
	}
}

func TestRoomLimits(t *testing.T) {
	b := newTestBroker(newMemStore())
	tok, _ := token(t)
	a := b.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)
	long := make([]byte, proto.MaxTopic+1)
	for i := range long {
		long[i] = 'x'
	}
	if code := a.call("room_create", proto.RoomArgs{Room: "wide", Topic: string(long)}, nil); code != "too_large" {
		t.Fatalf("long topic: %q", code)
	}
	b.inChat(mustID(t, tok), func(ch *chat) {
		for i := 0; i < maxRooms; i++ {
			ch.rooms["filler"+string(rune('a'+i%26))+string(rune('a'+i/26))] = &room{members: map[string]bool{}}
		}
	})
	if code := a.call("room_create", proto.RoomArgs{Room: "one-more"}, nil); code != "too_many_rooms" {
		t.Fatalf("room beyond the limit: %q", code)
	}
}

func mustID(t *testing.T, tok string) string {
	t.Helper()
	id := tok
	return id
}

func TestStoppedBrokerTakesNoConnections(t *testing.T) {
	b := newTestBroker(newMemStore())
	b.stopping.Store(true)
	srv, cl := net.Pipe()
	b.serving.Add(1)
	done := make(chan struct{})
	go func() { b.serve(srv); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serve kept a connection after shutdown began")
	}
	cl.Close()
}
