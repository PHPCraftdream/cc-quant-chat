package broker

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// stoppable gives the broker a listener, which shutdown closes.
func stoppable(t *testing.T, b *Broker) *Broker {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	b.ln = ln
	return b
}

func (s *memStore) messages(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.recs[id] {
		if r.T == "msg" {
			n++
		}
	}
	return n
}

func TestSendWithTheSameClientIDStoresAndDeliversOnce(t *testing.T) {
	be := newMemStore()
	b := newTestBroker(be)
	tok, id := token(t)
	a, c := b.connect(t), b.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)
	c.join(tok, "bob", proto.ProtoVersion)

	first, code := a.sendID(proto.RoomCommon, "", "hello", "cid-1")
	if code != "" || first != 1 {
		t.Fatalf("first send: id %d, code %q", first, code)
	}
	again, code := a.sendID(proto.RoomCommon, "", "hello", "cid-1")
	if code != "" || again != first {
		t.Fatalf("repeat: id %d, code %q; want the same id %d", again, code, first)
	}
	if got := c.msgs(); len(got) != 1 {
		t.Fatalf("bob received %v; want one delivery", got)
	}
	var h proto.HistoryResult
	a.call("history", proto.HistoryArgs{Room: proto.RoomCommon}, &h)
	if len(h.Messages) != 1 || be.messages(id) != 1 {
		t.Fatalf("history %d, stored %d; want one message", len(h.Messages), be.messages(id))
	}

	// The broker restarts: the second broker knows the id only from the store.
	b2 := newTestBroker(be)
	a2 := b2.connect(t)
	a2.key = a.key
	a2.join(tok, "alice", proto.ProtoVersion)
	if n, code := a2.sendID(proto.RoomCommon, "", "hello", "cid-1"); code != "" || n != first {
		t.Fatalf("repeat after restart: id %d, code %q; want %d", n, code, first)
	}
	if be.messages(id) != 1 {
		t.Fatalf("stored %d messages after the repeat, want 1", be.messages(id))
	}
}

func TestRepeatedSendDoesNotSpendTheLimit(t *testing.T) {
	be := newMemStore()
	tok, _ := token(t)
	b := newTestBroker(be)
	a := b.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)
	a.sendID(proto.RoomCommon, "", "x", "cid")
	b2 := newTestBroker(be) // repeats that only the store can recognize
	a2 := b2.connect(t)
	a2.key = a.key
	a2.join(tok, "alice", proto.ProtoVersion)
	for i := 0; i < 3*rateLimit; i++ {
		if _, code := a2.sendID(proto.RoomCommon, "", "x", "cid"); code != "" {
			t.Fatalf("repeat %d: %q; repeats must not count against the limit", i, code)
		}
	}
}

func TestUnconfirmedMessageSurvivesABrokerRestart(t *testing.T) {
	be := newMemStore()
	tok, _ := token(t)
	b1 := stoppable(t, newTestBroker(be))
	a, c := b1.connect(t), b1.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)
	c.join(tok, "bob", proto.ProtoVersion)
	a.send(proto.RoomCommon, "", "before the update")
	if got := c.msgs(); len(got) != 1 {
		t.Fatalf("live delivery: %v", got)
	}
	b1.shutdown("test") // bob never confirmed

	b2 := newTestBroker(be)
	c2 := b2.connect(t)
	c2.key = c.key
	w, code := c2.join(tok, "bob", proto.ProtoVersion)
	if code != "" || len(w.Backlog) != 1 {
		t.Fatalf("after the restart: code %q, backlog %d; want the unconfirmed message", code, len(w.Backlog))
	}
}

func TestStopFlushesWhatIsQueued(t *testing.T) {
	be := newMemStore()
	tok, _ := token(t)
	b := stoppable(t, newTestBroker(be))
	a, c := b.connect(t), b.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)
	c.join(tok, "bob", proto.ProtoVersion)
	gate := c.hold() // bob's side stops reading: frames pile up in the broker's queue
	const n = 20
	for i := 0; i < n; i++ {
		if _, code := a.send(proto.RoomCommon, "", "queued"); code != "" {
			t.Fatalf("send %d: %q", i, code)
		}
	}
	stopped := make(chan struct{})
	go func() { b.shutdown("test"); close(stopped) }()
	time.Sleep(100 * time.Millisecond) // the stop is under way and waiting for the writers
	close(gate)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not finish")
	}
	select {
	case <-c.ended:
	case <-time.After(3 * time.Second):
		t.Fatal("bob's connection was not closed")
	}
	if got := c.msgs(); len(got) != n {
		t.Fatalf("bob got %d of %d queued messages: the stop cut the queue", len(got), n)
	}
}

func TestChangesAreRefusedWhileStopping(t *testing.T) {
	be := newMemStore()
	tok, id := token(t)
	b := newTestBroker(be)
	a := b.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)
	b.stopping.Store(true)

	if _, code := a.send(proto.RoomCommon, "", "late"); code != "restarting" {
		t.Fatalf("send: %q, want restarting", code)
	}
	if code := a.call("room_create", proto.RoomArgs{Room: "ops"}, nil); code != "restarting" {
		t.Fatalf("room_create: %q, want restarting", code)
	}
	if be.messages(id) != 0 {
		t.Fatal("a message reached the store while stopping")
	}
	var info proto.BrokerInfo
	if code := a.call("ping", nil, &info); code != "" {
		t.Fatalf("ping: %q; reads must keep working", code)
	}
}

func TestOldClientsAreToldToRestart(t *testing.T) {
	b := newTestBroker(newMemStore())
	tok, _ := token(t)
	old, cur := b.connect(t), b.connect(t)
	w, code := old.join(tok, "oldie", 0)
	if code != "" || !strings.Contains(w.Warning, "older ccqc") {
		t.Fatalf("old client: code %q, warning %q; want the restart advice", code, w.Warning)
	}
	w, code = cur.join(tok, "newbie", proto.ProtoVersion)
	if code != "" || w.Warning != "" || w.Proto != proto.ProtoVersion {
		t.Fatalf("current client: code %q, warning %q, proto %d; want none and the broker's level", code, w.Warning, w.Proto)
	}
	b.mu.Lock()
	info := b.info()
	b.mu.Unlock()
	if info.ByProto["0"] != 1 || info.ByProto["2"] != 1 {
		t.Fatalf("by proto = %v", info.ByProto)
	}
}

func TestStopDoesNotWaitForeverForSeveralStuckClients(t *testing.T) {
	tok, _ := token(t)
	b := stoppable(t, newTestBroker(newMemStore()))
	a := b.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)
	var gates []chan struct{}
	for _, name := range []string{"bob", "carol", "dave"} {
		c := b.connect(t)
		c.join(tok, name, proto.ProtoVersion)
		gates = append(gates, c.hold()) // a frozen process: it never reads again
	}
	for i := 0; i < 20; i++ {
		a.send(proto.RoomCommon, "", "more than the pipe holds")
	}
	stopped := make(chan struct{})
	go func() { b.shutdown("test"); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(drainWait + 3*time.Second):
		t.Fatal("shutdown is stuck on clients that don't read")
	}
	for _, g := range gates {
		close(g)
	}
}
