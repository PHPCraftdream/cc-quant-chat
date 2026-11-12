package peer

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/events"
	"github.com/PHPCraftdream/cc-quant-chat/internal/node"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store/sqlite"
)

const (
	chatX = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	chatY = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
)

// tnode is a node in a test: a store with its own key and a manager.
type tnode struct {
	t      *testing.T
	name   string
	db     *sqlite.DB
	origin string
	priv   ed25519.PrivateKey
	pub    ed25519.PublicKey
	m      *Manager
	addr   string

	mu   sync.Mutex
	logs []string
	ids  map[string]int64

	writing sync.Mutex // what the chat's lock is to a broker: one writer of messages at a time
}

func newTnode(t *testing.T, name string) *tnode {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "ccqc.db"))
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	n := &tnode{t: t, name: name, db: db, origin: node.Origin(pub), priv: priv, pub: pub, ids: map[string]int64{}}
	if err := db.RegisterOrigin(n.origin, pub); err != nil {
		t.Fatal(err)
	}
	db.SetNode(n.origin, priv)
	n.m = New(db, n.origin, priv, n.logf)
	n.m.Accept = func(chatID string, e events.Event) error { // like the broker's acceptRemote: under the writers' lock
		n.writing.Lock()
		defer n.writing.Unlock()
		_, err := db.AcceptEvent(chatID, e)
		return err
	}
	db.OnEvents(n.m.Notify)
	t.Cleanup(func() { n.m.Close(); db.Close() })
	return n
}

func (n *tnode) logf(format string, args ...any) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.logs = append(n.logs, fmt.Sprintf(format, args...))
}

func (n *tnode) logged(part string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, l := range n.logs {
		if strings.Contains(l, part) {
			return true
		}
	}
	return false
}

func (n *tnode) logCount(part string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	c := 0
	for _, l := range n.logs {
		if strings.Contains(l, part) {
			c++
		}
	}
	return c
}

func (n *tnode) listen() string {
	n.t.Helper()
	addr, err := n.m.Listen("127.0.0.1:0")
	if err != nil {
		n.t.Fatal(err)
	}
	n.addr = addr.String()
	return n.addr
}

func (n *tnode) chat(id string) {
	n.t.Helper()
	if _, err := n.db.Load(id, "team"); err != nil {
		n.t.Fatal(err)
	}
}

// say writes a message of this node to the chat (an event of its journal).
//
// The broker numbers the messages of its chat under the chat's lock, and takes the events of other
// nodes under the same lock (broker.acceptRemote), so the number it gives a message of its own is
// never taken from under it. The nodes of these tests have no broker: a remote event applied by the
// manager takes MAX(id)+1 in its own transaction, and could land between the read of the newest id
// below and the append of this message (UNIQUE messages.id). The two share one lock here, as they do
// there (n.writing, also taken around the manager's Accept: see newTnode).
func (n *tnode) say(chat, text string) {
	n.t.Helper()
	n.writing.Lock()
	defer n.writing.Unlock()
	// The broker numbers messages after the newest one the store holds, those of other nodes included.
	recs, err := n.db.Load(chat, "")
	if err != nil {
		n.t.Fatal(err)
	}
	for _, r := range recs {
		if r.T == "maxid" {
			n.ids[chat] = max(n.ids[chat], r.ID)
		}
	}
	n.ids[chat]++
	m := &proto.Message{ID: n.ids[chat], TS: time.Now().UTC().Format(time.RFC3339), Room: proto.RoomCommon, From: n.name, Text: text}
	if err := n.db.Append(chat, store.Record{T: "msg", Key: "r:common", Msg: m}); err != nil {
		n.t.Fatal(err)
	}
}

// befriend makes two nodes peers of each other and shares the chats between them; a dials b.
func befriend(a, b *tnode, chats ...string) {
	a.t.Helper()
	must := func(err error) {
		a.t.Helper()
		if err != nil {
			a.t.Fatal(err)
		}
	}
	must(a.db.AddPeer(b.origin, b.addr))
	must(b.db.AddPeer(a.origin, ""))
	for _, c := range chats {
		must(a.db.SharePeerChat(c, b.origin))
		must(b.db.SharePeerChat(c, a.origin))
	}
	must(a.m.Refresh())
	must(b.m.Refresh())
}

func eventually(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("not within %v: %s", d, what)
}

// journal is every event of a chat the node holds, by (origin, seq), as the ID of the event.
func (n *tnode) journal(chat string) map[string]string {
	n.t.Helper()
	vec, err := n.db.Vector(chat)
	if err != nil {
		n.t.Fatal(err)
	}
	out := map[string]string{}
	for origin, top := range vec {
		evs, err := n.db.EventsAfter(chat, origin, 0, int(top)+1)
		if err != nil {
			n.t.Fatal(err)
		}
		for _, e := range evs {
			out[fmt.Sprintf("%s#%d", origin, e.Seq)] = e.ID()
		}
	}
	return out
}

func sameJournal(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func (n *tnode) holds(chat string, e events.Event) bool {
	evs, err := n.db.EventsAfter(chat, e.Origin, e.Seq-1, 1)
	return err == nil && len(evs) == 1 && evs[0].Seq == e.Seq && evs[0].ID() == e.ID()
}
