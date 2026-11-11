package broker

import (
	"bufio"
	"encoding/json"
	"log"
	"net"
	"sync"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// unjoinedWait: how long a connection may stay without joining a chat.
const unjoinedWait = 30 * time.Second

type conn struct {
	nc   net.Conn
	out  chan []byte
	done chan struct{}
	once sync.Once
	ch   *chat
	m    *member
}

func (c *conn) close() {
	c.once.Do(func() {
		close(c.done)
		c.nc.Close()
	})
}

// finish asks the writer to flush the queue and close the connection. A full queue means a
// client that isn't reading: that one is cut off at once.
func (c *conn) finish() {
	select {
	case c.out <- nil:
	default:
		c.close()
	}
}

// push enqueues without blocking; a client that can't keep up is dropped.
func (c *conn) push(f proto.Frame) {
	raw, err := json.Marshal(f)
	if err != nil {
		return
	}
	select {
	case <-c.done:
	case c.out <- raw:
	default:
		log.Printf("broker: slow client, dropping connection")
		c.close()
	}
}

func (c *conn) reply(id int64, data any, perr *proto.Error) {
	f := proto.Frame{Op: "ok", ID: id}
	if perr != nil {
		f.Op, f.Code, f.Msg = "err", perr.Code, perr.Msg
	} else if data != nil {
		raw, err := json.Marshal(data)
		if err != nil {
			f.Op, f.Code, f.Msg = "err", "internal", err.Error()
		} else {
			f.Data = raw
		}
	}
	c.push(f)
}

// writer: nil on out = flush and close.
func (c *conn) writer() {
	w := bufio.NewWriter(c.nc)
	for {
		select {
		case b := <-c.out:
			if b == nil {
				_ = w.Flush()
				if f, ok := c.nc.(interface{ Flush() error }); ok {
					_ = f.Flush() // named pipes: wait until the peer has read everything
				}
				c.close()
				return
			}
			w.Write(b)
			w.WriteByte('\n')
			if len(c.out) == 0 {
				if err := w.Flush(); err != nil {
					c.close()
					return
				}
			}
		case <-c.done:
			return
		}
	}
}

func (b *Broker) serve(nc net.Conn) {
	defer b.serving.Done()
	c := &conn{nc: nc, out: make(chan []byte, outQueue), done: make(chan struct{})}
	b.mu.Lock()
	if b.stopping.Load() { // shutdown already collected its connections: don't outlive it
		b.mu.Unlock()
		nc.Close()
		return
	}
	b.active++
	b.conns[c] = true
	b.mu.Unlock()
	go c.writer()
	// A connection that never joins (and never pings) must not hold a slot forever.
	_ = nc.SetReadDeadline(time.Now().Add(unjoinedWait))

	sc := bufio.NewScanner(nc)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var f proto.Frame
		if err := json.Unmarshal(sc.Bytes(), &f); err != nil {
			c.reply(0, nil, proto.Errf("bad_frame", "%v", err))
			continue
		}
		b.mu.Lock()
		data, perr := b.handle(c, &f)
		c.reply(f.ID, data, perr) // under lock: reply precedes later events
		joined := c.m != nil
		b.mu.Unlock()
		if joined && f.Op == "join" {
			_ = nc.SetReadDeadline(time.Time{})
		}
		if f.Op == "leave" && perr == nil {
			break
		}
	}

	b.mu.Lock()
	b.active--
	delete(b.conns, c)
	if b.active == 0 {
		b.idleAt = time.Now()
	}
	if c.m != nil && c.m.conn == c {
		c.m.conn = nil
		c.m.offline = time.Now()
		if !c.m.acks {
			c.ch.markSeen(c.m.name)
		}
		b.presence(c.ch, nil, c.m.name, c.m.name+" disconnected")
	}
	b.mu.Unlock()

	select {
	case c.out <- nil:
	case <-c.done:
	case <-time.After(time.Second):
	}
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
	}
	c.close()
}

func decode(raw json.RawMessage, v any) *proto.Error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return proto.Errf("bad_args", "%v", err)
	}
	return nil
}
