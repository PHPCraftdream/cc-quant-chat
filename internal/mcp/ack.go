package mcp

import (
	"log"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// Delivery confirmation. The broker keeps a read cursor per nick; it moves only when the
// session confirms that events reached Claude (hook batch written, channel notification
// sent, join result built), never merely because a connection ended.

// ackRef: the message to confirm once its event was delivered. Zero value: nothing to confirm.
type ackRef struct {
	cc *chatConn
	id int64
}

// confirm reports the delivery to the broker in the background.
func (a ackRef) confirm() {
	if a.cc != nil && a.id > 0 {
		a.cc.delivered(a.id)
	}
}

// pendItem: one line waiting for the rewake hook.
type pendItem struct {
	line string
	ack  ackRef
}

// confirmAll confirms the newest message of every chat in the batch.
func confirmAll(items []pendItem) {
	top := map[*chatConn]int64{}
	for _, it := range items {
		if it.ack.cc != nil && it.ack.id > top[it.ack.cc] {
			top[it.ack.cc] = it.ack.id
		}
	}
	for cc, id := range top {
		cc.delivered(id)
	}
}

// delivered records that the session got everything up to id and tells the broker.
func (cc *chatConn) delivered(id int64) {
	cc.mu.Lock()
	if id > cc.done {
		cc.done = id
	}
	cc.mu.Unlock()
	go cc.flushAck()
}

// flushAck sends the newest confirmed id if the broker doesn't know it yet. Also called after
// a reconnect: a confirmation lost with the old connection is repeated.
func (cc *chatConn) flushAck() {
	cc.mu.Lock()
	id, conn := cc.done, cc.conn
	if conn == nil || id <= cc.acked {
		cc.mu.Unlock()
		return
	}
	cc.mu.Unlock()
	err := conn.Call("ack", proto.AckArgs{ID: id}, nil)
	switch {
	case err == nil, proto.IsCode(err, "unknown_op"): // an older broker has no acks: it tracks delivery itself
	default:
		log.Printf("ack /%s #%d: %v", cc.local, id, err)
		return
	}
	cc.mu.Lock()
	if id > cc.acked {
		cc.acked = id
	}
	cc.mu.Unlock()
}

// fresh reports whether a message has not been handed to the session yet (and marks it);
// the same message can come twice: live and again in the backlog after a reconnect.
func (cc *chatConn) fresh(id int64) bool {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if id <= cc.last {
		return false
	}
	cc.last = id
	return true
}
