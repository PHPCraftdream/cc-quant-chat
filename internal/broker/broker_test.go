package broker

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// memStore keeps records like a database would, so a second broker can load them, and can be
// told to fail.
type memStore struct {
	mu        sync.Mutex
	recs      map[string][]store.Record
	loadErr   error
	appendErr error
}

func newMemStore() *memStore { return &memStore{recs: map[string][]store.Record{}} }

func (s *memStore) Load(id, _ string) ([]store.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return append([]store.Record(nil), s.recs[id]...), nil
}

func (s *memStore) Append(id string, r store.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appendErr != nil {
		return s.appendErr
	}
	s.recs[id] = append(s.recs[id], r)
	return nil
}

func (s *memStore) fail(load, appendErr error) {
	s.mu.Lock()
	s.loadErr, s.appendErr = load, appendErr
	s.mu.Unlock()
}

func newTestBroker(be store.Backend) *Broker {
	return &Broker{
		store: be, storeDesc: "test",
		chats: map[string]*chat{}, conns: map[*conn]bool{}, hits: map[string][]time.Time{},
		idle: time.Hour, grace: time.Hour, started: time.Now(),
	}
}

// client is a minimal peer over net.Pipe.
type client struct {
	t       *testing.T
	nc      net.Conn
	id      int64
	events  chan proto.Event
	replies chan proto.Frame
	key     string
}

func (b *Broker) connect(t *testing.T) *client {
	t.Helper()
	srv, cl := net.Pipe()
	b.serving.Add(1)
	go b.serve(srv)
	c := &client{t: t, nc: cl, events: make(chan proto.Event, 256), replies: make(chan proto.Frame, 16)}
	go func() {
		sc := bufio.NewScanner(cl)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
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
	c.id++
	raw, _ := json.Marshal(args)
	f, _ := json.Marshal(proto.Frame{Op: op, ID: c.id, Args: raw})
	if _, err := c.nc.Write(append(f, '\n')); err != nil {
		c.t.Fatalf("write %s: %v", op, err)
	}
	select {
	case r := <-c.replies:
		if r.Op == "err" {
			return r.Code
		}
		if out != nil && len(r.Data) > 0 {
			if err := json.Unmarshal(r.Data, out); err != nil {
				c.t.Fatalf("%s reply: %v", op, err)
			}
		}
		return ""
	case <-time.After(5 * time.Second):
		c.t.Fatalf("%s: no reply", op)
	}
	return ""
}

func (c *client) join(token, name string, level int) (proto.Welcome, string) {
	c.t.Helper()
	var w proto.Welcome
	code := c.call("join", proto.JoinArgs{Token: token, Name: name, Key: c.key, Proto: level}, &w)
	if code == "" && w.Key != "" {
		c.key = w.Key
	}
	return w, code
}

func (c *client) send(room, to, text string) (int64, string) {
	c.t.Helper()
	var r proto.SendResult
	code := c.call("send", proto.SendArgs{Room: room, To: to, Text: text}, &r)
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
		b.mu.Lock()
		m := ch.members[nick]
		off := m == nil || m.conn == nil
		b.mu.Unlock()
		if off {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("@%s still online", nick)
}

func token(t *testing.T) (string, string) {
	t.Helper()
	tok, err := proto.NewToken("team")
	if err != nil {
		t.Fatal(err)
	}
	_, id, err := proto.ParseToken(tok)
	if err != nil {
		t.Fatal(err)
	}
	return tok, id
}

func (b *Broker) chatOf(id string) *chat {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.chats[id]
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
	if _, code := a.join(tok, "alice", proto.ProtoVersion); code != "" || a.key == "" {
		t.Fatalf("first join: code %q, key %q", code, a.key)
	}
	key := a.key

	b2 := newTestBroker(be) // restart: everything comes from the store
	thief := b2.connect(t)
	if _, code := thief.join(tok, "alice", proto.ProtoVersion); code != "name_taken" {
		t.Fatalf("join without the key: code %q, want name_taken", code)
	}
	owner := b2.connect(t)
	owner.key = key
	if _, code := owner.join(tok, "alice", proto.ProtoVersion); code != "" {
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
	if w, _ := c3.join(tok, "bob", proto.ProtoVersion); len(w.Backlog) != 0 {
		t.Fatalf("after ack the backlog must be empty, got %d", len(w.Backlog))
	}
	if c3.call("ack", proto.AckArgs{ID: 99}, nil) != "bad_args" {
		t.Fatal("ack beyond the newest message must be refused")
	}
}

func TestOlderClientsKeepTheDisconnectCursor(t *testing.T) {
	be := newMemStore()
	b := newTestBroker(be)
	tok, id := token(t)
	a, c := b.connect(t), b.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)
	c.join(tok, "bob", 0) // no proto level: can't ack
	a.send(proto.RoomCommon, "", "one")
	c.msgs()
	c.close()
	b.waitOffline(t, b.chatOf(id), "bob")
	c2 := b.connect(t)
	if w, _ := c2.join(tok, "bob", 0); len(w.Backlog) != 0 {
		t.Fatalf("legacy client got %d old messages", len(w.Backlog))
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
	b.mu.Lock()
	ch := b.chats[mustID(t, tok)]
	for i := 0; i < maxRooms; i++ {
		ch.rooms["filler"+string(rune('a'+i%26))+string(rune('a'+i/26))] = &room{members: map[string]bool{}}
	}
	b.mu.Unlock()
	if code := a.call("room_create", proto.RoomArgs{Room: "one-more"}, nil); code != "too_many_rooms" {
		t.Fatalf("room beyond the limit: %q", code)
	}
}

func mustID(t *testing.T, tok string) string {
	t.Helper()
	_, id, err := proto.ParseToken(tok)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestNickReset(t *testing.T) {
	b := newTestBroker(newMemStore())
	tok, id := token(t)
	a, c := b.connect(t), b.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)
	c.join(tok, "bob", proto.ProtoVersion)
	a.send(proto.RoomCommon, "", "history stays")
	c.msgs()
	if c.call("ack", proto.AckArgs{ID: 1}, nil) != "" {
		t.Fatal("ack failed")
	}

	op := b.connect(t)
	if code := op.call("nick_reset", proto.NickResetArgs{Token: tok, Name: "bob"}, nil); code != "name_online" {
		t.Fatalf("reset of an online nick: %q", code)
	}
	if code := op.call("nick_reset", proto.NickResetArgs{Token: tok, Name: "nobody"}, nil); code != "no_such_member" {
		t.Fatalf("reset of an unknown nick: %q", code)
	}
	c.close()
	b.waitOffline(t, b.chatOf(id), "bob")
	b.mu.Lock()
	delete(b.chats[id].members, "bob") // grace period over
	b.mu.Unlock()
	if code := op.call("nick_reset", proto.NickResetArgs{Token: tok, Name: "bob"}, nil); code != "" {
		t.Fatalf("reset: %q", code)
	}

	fresh := b.connect(t) // a new session without the old key
	w, code := fresh.join(tok, "bob", proto.ProtoVersion)
	if code != "" || w.Key == "" {
		t.Fatalf("join after reset: code %q, key %q; want a new key", code, w.Key)
	}
	if len(w.Backlog) != 0 {
		t.Fatalf("the nick's read cursor must be kept, got backlog %d", len(w.Backlog))
	}
	late := b.connect(t)
	if _, code := late.join(tok, "bob", proto.ProtoVersion); code == "" {
		t.Fatal("the new key must protect the nick again")
	}
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
