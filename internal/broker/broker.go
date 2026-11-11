// Package broker: long-lived local process holding chats. One IPC connection = one member
// in one chat. Chat key = SHA-256(token); the token is not stored. Nicks, room membership and
// messages go through store.Backend (SQLite by default); presence, roles and connections don't.
package broker

import (
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/ipc"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store/sqlite"
)

const (
	histCap     = 10000 // messages kept in memory per room/dialog
	rateLimit   = 30    // messages per minute per member
	outQueue    = 1024
	joinRecent  = 20
	historyMax  = 500
	historyDflt = 20
	unreadCap   = 100 // backlog delivered on join; the rest via history/timeline
)

type Broker struct {
	mu       sync.Mutex
	store    store.Backend
	warning  string // set when history can't be saved; shown to every joiner
	chats    map[string]*chat
	active   int
	idleAt   time.Time
	idle     time.Duration
	grace    time.Duration
	stopping atomic.Bool
}

type chat struct {
	id, name string
	nextID   int64
	nicks    map[string]*nick   // registered nicks (durable)
	members  map[string]*member // present: online or within the grace period
	rooms    map[string]*room
	dms      map[string][]proto.Message
	roles    map[string]*role
	where    map[int64]string // msg id → thread key: "r:<room>" | "d:<a>|<b>"
	store    store.Backend
}

type nick struct {
	hash string // SHA-256 of the nick key
	seen int64  // last message id delivered live before going offline
}

type member struct {
	name    string
	role    string
	conn    *conn
	offline time.Time
	sent    []time.Time
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
	ln, err := ipc.Listen()
	if err != nil {
		return fmt.Errorf("listen %s: %w", ipc.Path(), err)
	}
	b := &Broker{
		chats:  map[string]*chat{},
		idleAt: time.Now(),
		idle:   envDur("CCQC_BROKER_IDLE", 30*time.Minute),
		grace:  envDur("CCQC_GRACE", 60*time.Second),
	}
	b.openStore()
	log.Printf("broker: listening on %s (pid %d)", ipc.Path(), os.Getpid())
	go b.janitor(ln)
	for {
		nc, err := ln.Accept()
		if err != nil {
			if b.stopping.Load() {
				log.Printf("broker: stopped (idle)")
				if c, ok := b.store.(interface{ Close() error }); ok {
					_ = c.Close()
				}
				return nil
			}
			log.Printf("broker: accept: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go b.serve(nc)
	}
}

// openStore: SQLite at sqlite.DefaultPath(), or memory with CCQC_STORE=memory.
// If the database can't be opened the broker still runs, in memory, and warns every joiner.
func (b *Broker) openStore() {
	if os.Getenv("CCQC_STORE") == "memory" {
		b.store = store.Memory{}
		log.Printf("broker: store: memory (CCQC_STORE=memory)")
		return
	}
	path := sqlite.DefaultPath()
	db, err := sqlite.Open(path)
	if err != nil {
		b.store = store.Memory{}
		b.warning = fmt.Sprintf("chat history is not saved: database %s: %v", path, err)
		log.Printf("broker: %s", b.warning)
		return
	}
	b.store = db
	log.Printf("broker: store: sqlite %s", path)
}

func envDur(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

func (b *Broker) janitor(ln net.Listener) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for range t.C {
		b.mu.Lock()
		now := time.Now()
		for _, ch := range b.chats {
			for _, m := range ch.members {
				if m.conn == nil && !m.offline.IsZero() && now.Sub(m.offline) > b.grace {
					b.removeMember(ch, m, m.name+" went offline")
				}
			}
		}
		stop := b.active == 0 && now.Sub(b.idleAt) > b.idle
		if stop {
			b.stopping.Store(true)
		}
		b.mu.Unlock()
		if stop {
			ln.Close()
			return
		}
	}
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
	}
}

// loadChat creates the in-memory chat and replays its stored records.
func (b *Broker) loadChat(id, name string) *chat {
	ch := newChat(id, name)
	ch.store = b.store
	recs, err := b.store.Load(id, name)
	if err != nil {
		log.Printf("broker: chat %s#%s: load: %v", name, id[:8], err)
	}
	for _, r := range recs {
		ch.replay(r)
	}
	return ch
}

func (ch *chat) replay(r store.Record) {
	switch r.T {
	case "nick":
		ch.nicks[r.Nick] = &nick{hash: r.Hash, seen: r.ID}
	case "seen":
		if n := ch.nicks[r.Nick]; n != nil {
			n.seen = r.ID
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

// save hands a record to the store backend.
func (ch *chat) save(r store.Record) {
	if ch.store == nil {
		return
	}
	if err := ch.store.Append(ch.id, r); err != nil {
		log.Printf("broker: chat %s#%s: store: %v", ch.name, ch.id[:8], err)
	}
}

// markSeen remembers that everything so far was delivered to the nick live.
func (ch *chat) markSeen(name string) {
	if n := ch.nicks[name]; n != nil && n.seen != ch.nextID {
		n.seen = ch.nextID
		ch.save(store.Record{T: "seen", Nick: name, ID: n.seen})
	}
}
