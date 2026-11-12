// Package broker: long-lived local process holding chats. One IPC connection = one member
// in one chat. A chat is named by its id. Nicks, room membership and
// messages go through store.Backend (SQLite by default); presence, roles and connections don't.
package broker

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/ipc"
	"github.com/PHPCraftdream/cc-quant-chat/internal/owner"
	"github.com/PHPCraftdream/cc-quant-chat/internal/peer"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store/sqlite"
	"github.com/PHPCraftdream/cc-quant-chat/internal/version"
)

const (
	histCap     = store.ThreadWindow // messages kept in memory per room/dialog
	drainWait   = 2 * time.Second    // how long a stopping broker lets writers flush their queues
	maxClientID = 64                 // bytes of a client_msg_id
	rateLimit   = 30                 // messages per minute per nick (kept across leave/join)
	ctlLimit    = 60                 // room and role changes per minute per nick
	maxRooms    = 100                // rooms per chat
	outQueue    = 1024
	joinRecent  = 20
	historyMax  = 500
	historyDflt = 20
	unreadCap   = 100 // backlog delivered on join; the rest via history/timeline
)

type Broker struct {
	// mu protects what is shared by every chat: the map of chats, the connections, the owner state
	// below, the counters of the stop and idle logic. A chat's operations run under its own lock
	// (chat.mu); mu is taken before a chat's lock, never after it. hitsMu, setMu and ownerMu guard
	// single values that chat operations read; nothing is taken while one of them is held.
	mu        sync.Mutex
	hitsMu    sync.Mutex
	setMu     sync.RWMutex
	ownerMu   sync.RWMutex
	ownerOpMu sync.Mutex // one change of the owner at a time
	keyMu     sync.Mutex // the proofs of A, one at a time (the counters of wrong ones); nothing is taken after it but a database

	keyAlerts map[string]time.Time // chat/nick → when the chat was last told of wrong proofs (under keyMu)

	nodePriv  ed25519.PrivateKey // the node key; with it the broker starts the exchange of chats with other nodes
	peerMgr   *peer.Manager      // nil without a store that can exchange
	peerAt    string             // where it accepts other nodes, "" if it does not
	store     store.Backend
	storeDesc string
	warning   string // set when history can't be saved; shown to every joiner
	chats     map[string]*chat
	conns     map[*conn]bool
	hits      map[string][]time.Time       // rate limiter: chat/nick/class → recent actions
	settings  map[string]map[string]string // changed with `ccqc config set`: scope ("" or chat id) → key → value
	ownerPub  ed25519.PublicKey            // the registered owner key, nil if none
	ownerAt   string                       // when it was registered
	origin    string                       // this broker's identity: first 16 bytes of SHA-256(node key), hex
	active    int
	idleAt    time.Time
	idle      time.Duration
	grace     time.Duration
	started   time.Time
	ln        net.Listener
	serving   sync.WaitGroup // serve goroutines; waited for before the store closes
	stopping  atomic.Bool
	reason    atomic.Value // why the broker stops: "idle" or "requested"
}

type chat struct {
	mu       sync.Mutex   // everything below
	refs     atomic.Int32 // operations that hold the chat (found it, or wait for its lock): it is not unloaded under them
	dead     bool         // unloaded: out of the map of chats, its store released
	id, name string
	nextID   int64
	nicks    map[string]*nick   // registered nicks (durable)
	members  map[string]*member // present: online or within the grace period
	rooms    map[string]*room
	dms      map[string][]proto.Message
	roles    map[string]*role
	where    map[int64]string // msg id → thread key: "r:<room>" | "d:<a>|<b>"
	sent     map[string]int64 // "<nick>|<client_msg_id>" → msg id: recent idempotent sends
	sentOrd  []string         // insertion order of sent, to cap it
	memClock int64            // logical clock when the store keeps none (memory store)
	store    store.Backend
}

type nick struct {
	pub  string // the nick's Ed25519 key, base64; "" while the nick still joins by its secret
	seen int64  // last message id delivered live before going offline
	// pinned: a role the owner pinned to this nick; the member holds it, can't change it and
	// nobody else can claim the name.
	pinned string
}

type member struct {
	name    string
	role    string
	conn    *conn
	offline time.Time
	owner   bool              // joined with a valid owner signature; set by the broker only
	pub     ed25519.PublicKey // the nick's key, proven at join; nil for a client that joined by its secret
	acks    bool              // the client confirms delivery with "ack" (proto 2+)
	proto   int               // protocol level the client announced in join
	status  memberStatus
}

type role struct {
	exclusive bool
	holders   map[string]bool
}

type room struct {
	name, topic, createdBy string
	members                map[string]bool // nicks, durable
	hist                   []proto.Message
}

func Run() error {
	b := &Broker{
		chats:   map[string]*chat{},
		conns:   map[*conn]bool{},
		hits:    map[string][]time.Time{},
		idleAt:  time.Now(),
		idle:    envDur("CCQC_BROKER_IDLE", 30*time.Minute),
		grace:   envDur("CCQC_GRACE", 60*time.Second),
		started: time.Now(),
	}
	if err := b.openStore(); err != nil {
		return err
	}
	if err := b.initNode(); err != nil {
		b.closeStore()
		return fmt.Errorf("node key: %w", err)
	}
	if err := b.loadSettings(); err != nil {
		b.closeStore()
		return fmt.Errorf("settings: %w", err)
	}
	if err := b.loadOwner(); err != nil {
		b.closeStore()
		return fmt.Errorf("owner key: %w", err)
	}
	b.settleNickSecrets()
	if err := b.startPeers(b.nodePriv); err != nil {
		b.closeStore()
		return fmt.Errorf("exchange with other nodes: %w", err)
	}
	ln, err := ipc.Listen()
	if err != nil {
		b.closeStore()
		return fmt.Errorf("listen %s: %w", ipc.Path(), err)
	}
	b.ln = ln
	log.Printf("broker: %s listening on %s (pid %d)", version.Version, ipc.Path(), os.Getpid())
	go b.janitor()
	for {
		nc, err := ln.Accept()
		if err != nil {
			if b.stopping.Load() {
				break
			}
			log.Printf("broker: accept: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		b.serving.Add(1)
		go b.serve(nc)
	}
	// Let connections record their state (seen marks) before the store closes.
	done := make(chan struct{})
	go func() { b.serving.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		log.Printf("broker: some connections did not finish in time")
	}
	b.closeStore()
	log.Printf("broker: stopped (%v)", b.reason.Load())
	return nil
}

func (b *Broker) closeStore() {
	b.stopPeers()
	if c, ok := b.store.(interface{ Close() error }); ok {
		_ = c.Close()
	}
}

// shutdown stops accepting and closes every connection; clients reconnect to the next
// broker on their own. Safe to call more than once.
func (b *Broker) shutdown(reason string) {
	if !b.stopping.CompareAndSwap(false, true) {
		return
	}
	b.reason.Store(reason)
	b.ln.Close()
	b.mu.Lock()
	conns := make([]*conn, 0, len(b.conns))
	for c := range b.conns {
		conns = append(conns, c)
	}
	b.mu.Unlock()
	// From here handle() refuses changes ("restarting"). Every writer first sends what is
	// queued, then closes its connection; whoever is not done in time is cut off.
	for _, c := range conns {
		c.finish()
	}
	expired := make(chan struct{}) // closed, so every waiter sees it (a timer channel fires once)
	t := time.AfterFunc(drainWait, func() { close(expired) })
	defer t.Stop()
	for _, c := range conns {
		select {
		case <-c.done:
		case <-expired:
		}
	}
	for _, c := range conns {
		c.close()
	}
}

// info: what `ccqc broker status` shows. A chat in the middle of a slow write is not waited for.
func (b *Broker) info() proto.BrokerInfo {
	b.mu.Lock()
	chats := len(b.chats)
	b.mu.Unlock()
	clients := 0
	byProto := map[string]int{}
	for _, ch := range b.chatList() {
		if !ch.mu.TryLock() {
			continue
		}
		for _, m := range ch.members {
			if m.conn != nil {
				clients++
				byProto[strconv.Itoa(m.proto)]++
			}
		}
		ch.mu.Unlock()
	}
	ownerFP := ""
	ownerPub, ownerAt := b.ownerState()
	if ownerPub != nil {
		ownerFP = owner.Fingerprint(ownerPub)
	}
	return proto.BrokerInfo{
		Owner: ownerFP, OwnerSince: ownerAt,
		Version: version.Version, Proto: proto.ProtoVersion, PID: os.Getpid(), Started: b.started.UTC().Format(time.RFC3339),
		Chats: chats, Clients: clients, Store: b.storeDesc, Warning: b.warning, ByProto: byProto, Node: b.origin,
	}
}

// openStore: SQLite at sqlite.DefaultPath(), or memory with CCQC_STORE=memory.
// A database that can't be opened or migrated stops the broker: running on without it would
// silently lose history and nick keys. Memory is only for those who ask for it.
func (b *Broker) openStore() error {
	if os.Getenv("CCQC_STORE") == "memory" {
		b.store, b.storeDesc = store.Memory{}, "memory (CCQC_STORE=memory)"
		b.warning = "chat history is not saved: the broker runs with CCQC_STORE=memory"
		log.Printf("broker: store: %s", b.storeDesc)
		return nil
	}
	path := sqlite.DefaultPath()
	db, err := sqlite.Open(path)
	if err != nil {
		return fmt.Errorf("database %s: %w (set CCQC_STORE=memory to run without saving anything)", path, err)
	}
	b.store, b.storeDesc = db, "sqlite, data directory "+db.Dir()+" (node.db, chats/<id>.db)"
	log.Printf("broker: store: %s", b.storeDesc)
	return nil
}

func envDur(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

func (b *Broker) janitor() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for range t.C {
		if b.stopping.Load() {
			return
		}
		if b.sweep() {
			b.shutdown("idle")
			return
		}
	}
}

// sweep is one round of the janitor: members who stayed away past the grace period are removed,
// chats nobody is in are let go, old counters dropped. It reports whether the broker has been idle
// long enough to stop.
func (b *Broker) sweep() (stop bool) {
	now := time.Now()
	for _, ch := range b.chatList() {
		if !ch.mu.TryLock() { // busy: looked at again in the next round
			continue
		}
		for _, m := range ch.members {
			if m.conn == nil && !m.offline.IsZero() && now.Sub(m.offline) > b.graceFor() {
				b.removeMember(ch, m, m.name+" went offline")
			}
		}
		ch.mu.Unlock()
		b.unloadIfEmpty(ch)
	}
	b.pruneHits(now)
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.active == 0 && now.Sub(b.idleAt) > b.idleFor()
}

func newChat(id, name string) *chat {
	return &chat{
		id:      id,
		name:    name,
		nicks:   map[string]*nick{},
		members: map[string]*member{},
		rooms:   map[string]*room{proto.RoomCommon: {name: proto.RoomCommon, members: map[string]bool{}}},
		dms:     map[string][]proto.Message{},
		roles:   map[string]*role{},
		where:   map[int64]string{},
		sent:    map[string]int64{},
	}
}

func (ch *chat) replay(r store.Record) {
	switch r.T {
	case "nick":
		ch.nicks[r.Nick] = &nick{pub: r.Pub, seen: r.ID}
		ch.rooms[proto.RoomCommon].members[r.Nick] = true // registering a nick puts it in @common
	case "seen":
		if n := ch.nicks[r.Nick]; n != nil {
			n.seen = r.ID
		}
	case "maxid":
		ch.nextID = max(ch.nextID, r.ID)
	case "pin":
		if n := ch.nicks[r.Nick]; n != nil {
			n.pinned = r.Role
		}
	case "nick_key":
		if n := ch.nicks[r.Nick]; n != nil {
			n.pub = r.Pub
		}
	case "room":
		if ch.rooms[r.Room] == nil {
			ch.rooms[r.Room] = &room{name: r.Room, topic: r.Topic, createdBy: r.Nick, members: map[string]bool{}}
		}
	case "in", "out":
		if rm := ch.rooms[r.Room]; rm != nil {
			if r.T == "in" {
				rm.members[r.Nick] = true
			} else {
				delete(rm.members, r.Nick)
			}
		}
	case "msg":
		if r.Msg == nil {
			return
		}
		ch.keep(r.Key, *r.Msg)
		if r.Msg.ID > ch.nextID {
			ch.nextID = r.Msg.ID
		}
	}
}

// save hands a record to the store backend. Callers change memory only after it succeeds.
func (ch *chat) save(r store.Record) *proto.Error {
	if ch.store == nil {
		return nil
	}
	if err := ch.store.Append(ch.id, r); err != nil {
		log.Printf("broker: chat %s#%s: store: %v", ch.name, ch.id[:8], err)
		return proto.Errf("store_failed", "history could not be saved (%v); nothing was changed, try again", err)
	}
	return nil
}

// saveMsg stores a message; a repeat of an earlier send (same sender and client id) comes back
// in *dup instead of as an error.
func (ch *chat) saveMsg(r store.Record, dup **store.DuplicateError) *proto.Error {
	if ch.store == nil {
		return nil
	}
	err := ch.store.Append(ch.id, r)
	var d *store.DuplicateError
	if errors.As(err, &d) {
		*dup = d
		return nil
	}
	if err != nil {
		log.Printf("broker: chat %s#%s: store: %v", ch.name, ch.id[:8], err)
		return proto.Errf("store_failed", "history could not be saved (%v); nothing was changed, try again", err)
	}
	return nil
}

// ack moves the nick's read cursor forward: everything up to id reached the session.
func (ch *chat) ack(name string, id int64) *proto.Error {
	n := ch.nicks[name]
	if n == nil || id <= n.seen {
		return nil
	}
	if id > ch.nextID {
		return proto.Errf("bad_args", "ack %d is beyond the last message %d", id, ch.nextID)
	}
	if e := ch.save(store.Record{T: "seen", Nick: name, ID: id}); e != nil {
		return e
	}
	n.seen = id
	return nil
}

// markSeen is the cursor for clients that don't ack (older versions): at disconnect,
// everything sent so far counts as delivered.
func (ch *chat) markSeen(name string) {
	if n := ch.nicks[name]; n != nil && n.seen != ch.nextID {
		_ = ch.ack(name, ch.nextID)
	}
}
