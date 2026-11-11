package broker

import (
	"bufio"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/owner"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// slowStore holds the writes of messages of chats that have a gate until the gate is opened.
type slowStore struct {
	*memStore
	mu      sync.Mutex
	gates   map[string]chan struct{}
	entered chan string
}

func newSlowStore() *slowStore {
	return &slowStore{memStore: newMemStore(), gates: map[string]chan struct{}{}, entered: make(chan string, 16)}
}

func (s *slowStore) hold(chatID string) chan struct{} {
	g := make(chan struct{})
	s.mu.Lock()
	s.gates[chatID] = g
	s.mu.Unlock()
	return g
}

func (s *slowStore) Append(id string, r store.Record) error {
	if r.T == "msg" {
		s.mu.Lock()
		g := s.gates[id]
		s.mu.Unlock()
		if g != nil {
			s.entered <- id
			<-g
		}
	}
	return s.memStore.Append(id, r)
}

func within(t *testing.T, d time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s: no answer within %v", what, d)
	}
}

func TestAChatWithASlowStoreDoesNotHoldUpAnother(t *testing.T) {
	ss := newSlowStore()
	b := newTestBroker(ss)
	tokA, idA := token(t)
	tokB, _ := token(t)
	alice, carol, watcher := b.connect(t), b.connect(t), b.connect(t)
	alice.join(tokA, "alice", proto.ProtoVersion)
	carol.join(tokB, "carol", proto.ProtoVersion)
	gate := ss.hold(idA)

	slow := make(chan string, 1)
	go func() { _, code := alice.send(proto.RoomCommon, "", "slow"); slow <- code }()
	select {
	case <-ss.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("alice's message did not reach the store")
	}
	// Alice's request is inside the store of chat A with that chat's lock held. Chat B, the
	// status of the broker and a join to a third chat must not wait for it.
	within(t, 3*time.Second, "a message in chat B", func() {
		if _, code := carol.send(proto.RoomCommon, "", "fast"); code != "" {
			t.Errorf("carol: %q", code)
		}
	})
	within(t, 3*time.Second, "ping", func() {
		var info proto.BrokerInfo
		if code := watcher.call("ping", nil, &info); code != "" || info.Chats != 2 {
			t.Errorf("ping: %q, %d chats", code, info.Chats)
		}
	})
	tokC, _ := token(t)
	dave := b.connect(t)
	within(t, 3*time.Second, "a join to a third chat", func() {
		if _, code := dave.join(tokC, "dave", proto.ProtoVersion); code != "" {
			t.Errorf("dave: %q", code)
		}
	})
	select {
	case code := <-slow:
		t.Fatalf("the slow send finished (%q) before its store let it go", code)
	default:
	}
	close(gate)
	select {
	case code := <-slow:
		if code != "" {
			t.Fatalf("the slow send: %q", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the slow send did not finish after the store let go")
	}
	if got := ss.messages(idA); got != 1 {
		t.Fatalf("%d messages in chat A", got)
	}
}

// rawFrames reads a connection frame by frame, in the order they arrive.
type rawFrames struct {
	t  *testing.T
	nc net.Conn
	sc *bufio.Scanner
	id int64
}

func (b *Broker) raw(t *testing.T) *rawFrames {
	t.Helper()
	srv, cl := net.Pipe()
	b.serving.Add(1)
	go b.serve(srv)
	t.Cleanup(func() { cl.Close() })
	sc := bufio.NewScanner(cl)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	return &rawFrames{t: t, nc: cl, sc: sc}
}

func (r *rawFrames) send(op string, args any) int64 {
	r.t.Helper()
	r.id++
	raw, _ := json.Marshal(args)
	f, _ := json.Marshal(proto.Frame{Op: op, ID: r.id, Args: raw})
	if _, err := r.nc.Write(append(f, '\n')); err != nil {
		r.t.Fatal(err)
	}
	return r.id
}

func (r *rawFrames) next() proto.Frame {
	r.t.Helper()
	if !r.sc.Scan() {
		r.t.Fatalf("connection ended: %v", r.sc.Err())
	}
	var f proto.Frame
	if err := json.Unmarshal(r.sc.Bytes(), &f); err != nil {
		r.t.Fatal(err)
	}
	return f
}

// The reply to a join comes ahead of every event of the chat, however busy the chat is: the join
// holds the chat's lock until the reply is out.
func TestTheReplyToAJoinComesBeforeTheEventsOfTheChat(t *testing.T) {
	b := newTestBroker(newMemStore())
	b.settings = map[string]map[string]string{"": {"limits.messages_per_minute": "100000"}}
	tok, _ := token(t)
	alice := b.connect(t)
	alice.join(tok, "alice", proto.ProtoVersion)

	var stop atomic.Bool
	flooded := make(chan struct{})
	go func() {
		defer close(flooded)
		for i := 0; !stop.Load(); i++ {
			if _, code := alice.send(proto.RoomCommon, "", fmt.Sprintf("noise %d", i)); code != "" {
				t.Errorf("alice: %q", code)
				return
			}
			time.Sleep(200 * time.Microsecond) // busy, but not faster than a reader can keep up with
		}
	}()
	defer func() { stop.Store(true); <-flooded }()
	key := ""
	for round := 0; round < 40; round++ {
		bob := b.raw(t) // a leave ends the connection
		id := bob.send("join", proto.JoinArgs{Token: tok, Name: "bob", Key: key, Proto: proto.ProtoVersion})
		first := bob.next()
		if first.Op == "event" || first.ID != id {
			t.Fatalf("round %d: the first frame after join is %+v, want its reply", round, first)
		}
		if first.Op == "err" {
			t.Fatalf("round %d: join: %s %s", round, first.Code, first.Msg)
		}
		var w proto.Welcome
		if err := json.Unmarshal(first.Data, &w); err == nil && w.Key != "" {
			key = w.Key
		}
		lid := bob.send("leave", nil)
		for f := bob.next(); f.Op == "event" || f.ID != lid; f = bob.next() {
		}
	}
}

// A mix of everything that comes and goes, run under the race detector: members joining, sending,
// making rooms and leaving; the janitor sweeping; status asked for; chats unloaded and loaded
// again; nicks reset. Nothing may deadlock, race, or answer with an internal error.
func TestChatsLoadUnloadAndResetWhileBusy(t *testing.T) {
	b := stoppable(t, newTestBroker(newMemStore()))
	b.grace = time.Millisecond
	toks := []string{}
	for i := 0; i < 3; i++ {
		tok, _ := token(t)
		toks = append(toks, tok)
	}
	var stop atomic.Bool
	var bad atomic.Int32
	check := func(what, code string) {
		switch code {
		case "", "name_online", "no_such_member", "not_joined", "name_taken", "restarting", "rate_limited", "no_such_room", "not_in_room", "room_exists":
		default:
			t.Errorf("%s: unexpected %q", what, code)
			bad.Add(1)
		}
	}
	var wg sync.WaitGroup
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, nickName, key := toks[w%3], fmt.Sprintf("w%d", w), ""
			for i := 0; i < 25 && !stop.Load(); i++ {
				c := b.connect(t)
				c.key = key
				_, code := c.join(tok, nickName, proto.ProtoVersion)
				if c.key != "" {
					key = c.key
				}
				check("join", code)
				if code == "" {
					_, code = c.send(proto.RoomCommon, "", "hi")
					check("send", code)
					check("room_create", c.call("room_create", proto.RoomArgs{Room: fmt.Sprintf("r%d", i%4)}, nil))
					var st proto.State
					check("state", c.call("state", nil, &st))
					if i%2 == 0 {
						check("leave", c.call("leave", nil, nil))
					}
				}
				c.close()
			}
		}()
	}
	for _, task := range []func(){
		func() { b.sweep() },
		func() { b.mu.Lock(); _ = b.info(); b.mu.Unlock() },
		func() {
			c := b.connect(t)
			for i, tok := range toks {
				check("nick_reset", c.call("nick_reset", proto.NickResetArgs{Token: tok, Name: fmt.Sprintf("w%d", i)}, nil))
			}
		},
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 40 && !stop.Load(); i++ {
				task()
				time.Sleep(2 * time.Millisecond)
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		stop.Store(true)
		t.Fatal("the broker is stuck: a deadlock between the broker's lock and the chats'")
	}
	b.sweep()
	b.mu.Lock()
	for id, ch := range b.chats {
		ch.mu.Lock()
		if ch.dead {
			t.Errorf("chat %.8s is dead but still in the map", id)
		}
		ch.mu.Unlock()
	}
	b.mu.Unlock()
}

// Administrator operations and the owner's key change under load, then the broker stops while
// members are still working: every request is answered or refused as restarting; the stop ends.
func TestAdministratorOperationsAndShutdownWhileBusy(t *testing.T) {
	b := stoppable(t, newTestBroker(newMemStore()))
	b.grace = time.Millisecond
	k := newKey(t)
	register(t, b, k)
	var toks, ids []string
	for i := 0; i < 2; i++ {
		tok, id := token(t)
		toks, ids = append(toks, tok), append(ids, id)
	}
	var stop atomic.Bool
	var wg sync.WaitGroup
	ok := func(what, code string) {
		switch code {
		case "", "name_online", "no_such_member", "not_joined", "name_taken", "restarting", "closed", "timeout", "role_taken", "no_pinned_role", "rate_limited", "no_such_room", "not_in_room", "room_exists":
		default:
			t.Errorf("%s: unexpected %q", what, code)
		}
	}
	admin := func(c *client, op string, args any) string {
		var ch proto.Challenge
		if code := c.call("challenge", nil, &ch); code != "" {
			return code
		}
		raw, _ := json.Marshal(args)
		canon, _ := proto.CanonicalJSON(raw)
		sig := owner.Encode(ed25519.Sign(k, proto.AdminMessage(op, canon, ch.Nonce)))
		return c.call("admin", proto.AdminArgs{Op: op, Args: raw, Sig: sig}, nil)
	}
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key := ""
			for i := 0; !stop.Load(); i++ {
				c := b.connect(t)
				c.lax = true
				c.key = key
				_, code := c.join(toks[w%2], fmt.Sprintf("w%d", w), proto.ProtoVersion)
				if c.key != "" {
					key = c.key
				}
				if code == "" {
					_, code = c.send(proto.RoomCommon, "", "work")
					ok("send", code)
					ok("room", c.call("room_create", proto.RoomArgs{Room: fmt.Sprintf("r%d", i%3)}, nil))
				} else {
					ok("join", code)
				}
				c.close()
				if i > 200 {
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		c := b.connect(t)
		c.lax = true
		for i := 0; !stop.Load() && i < 60; i++ {
			chat := ids[i%2]
			nickName := fmt.Sprintf("w%d", i%4)
			ok("role_give", admin(c, "role_give", proto.AdminRole{Chat: chat, Nick: nickName, Role: "lead"}))
			ok("config_set", admin(c, "config_set", proto.AdminConfig{Chat: chat, Key: "board.manager_role", Value: "lead"}))
			ok("config_set", admin(c, "config_set", proto.AdminConfig{Key: "limits.messages_per_minute", Value: "1000"}))
			ok("role_take", admin(c, "role_take", proto.AdminRole{Chat: chat, Nick: nickName}))
			ok("nick_reset", admin(c, "nick_reset", proto.AdminRole{Chat: chat, Nick: nickName}))
		}
	}()
	time.Sleep(300 * time.Millisecond)
	within(t, drainWait+5*time.Second, "shutdown", func() { b.shutdown("test") })
	stop.Store(true)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("a request is still waiting after the broker stopped")
	}
}
