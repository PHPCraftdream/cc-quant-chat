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
	c := &conn{nc: nc, out: make(chan []byte, outQueue), done: make(chan struct{})}
	b.mu.Lock()
	b.active++
	b.mu.Unlock()
	go c.writer()

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
		b.mu.Unlock()
		if f.Op == "leave" && perr == nil {
			break
		}
	}

	b.mu.Lock()
	b.active--
	if b.active == 0 {
		b.idleAt = time.Now()
	}
	if c.m != nil && c.m.conn == c {
		c.m.conn = nil
		c.m.offline = time.Now()
		c.ch.markSeen(c.m.name)
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
