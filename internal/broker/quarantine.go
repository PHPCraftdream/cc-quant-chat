package broker

import (
	"encoding/json"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/node"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// Quarantine (docs/design-trust.md section 5): the owner's operations, what is said when it begins or
// ends, and the mark on what a node in quarantine has already given (taken before, so it stays).

func (b *Broker) quarantineAdmin(op string, args json.RawMessage) (any, *proto.Error) {
	if b.peerMgr == nil {
		return nil, proto.Errf("peers_unavailable", "exchange with other nodes needs the SQLite store")
	}
	var a proto.PeerQuarantine
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, proto.Errf("bad_args", "%v", err)
	}
	if !originName.MatchString(a.Origin) {
		return nil, proto.Errf("bad_args", "origin is 32 hex characters")
	}
	if a.Origin == b.origin {
		return nil, proto.Errf("bad_args", "that is this node")
	}
	if op == "peer_release" {
		was, err := b.peerMgr.Release(a.Origin)
		if err != nil {
			return nil, proto.Errf("store_failed", "%v", err)
		}
		if !was {
			return nil, proto.Errf("not_quarantined", "node %s is not in quarantine", a.Origin)
		}
		return proto.BoardReply{Text: "node " + a.Origin + " released: its new events are taken again"}, nil
	}
	if _, ok := b.peerMgr.InQuarantine(a.Origin); ok {
		return nil, proto.Errf("already_quarantined", "node %s is in quarantine already", a.Origin)
	}
	reason := strings.TrimSpace(a.Reason)
	if reason == "" {
		reason = "by the owner"
	}
	info := store.QuarantineInfo{Origin: a.Origin, Reason: reason, Since: time.Now().UTC().Format(time.RFC3339), By: "owner"}
	if err := b.peerMgr.Quarantine(info); err != nil {
		return nil, proto.Errf("store_failed", "%v", err)
	}
	return proto.BoardReply{Text: "node " + a.Origin + " is in quarantine: its new events are not taken, what it gave stays (lift it: ccqc peer release " + a.Origin + ")"}, nil
}

// onQuarantine says loudly, in the chats of the node and of the one that brought it, that a source
// went into quarantine or came out, and why.
func (b *Broker) onQuarantine(info store.QuarantineInfo, released bool) {
	text := "node " + node.Short(info.Origin) + ": released from quarantine by the owner"
	op := "peer.release"
	if !released {
		text = "node " + node.Short(info.Origin) + ": in quarantine (" + info.Reason + "): its new events are not taken, what it gave stays, the owner lifts it"
		op = "peer.quarantine"
	}
	log.Printf("broker: %s (%s)", text, info.Origin)
	args, _ := json.Marshal(map[string]string{"origin": info.Origin, "reason": info.Reason, "by": info.By, "via": info.Via})
	b.logAdmin(op, args, text)
	if fed, ok := b.store.(store.Federation); ok {
		b.announcePeerChatsOf(fed, text, info.Origin, info.Via)
	}
}

// quarantinedShorts: the short names of the nodes in quarantine, as nicks of theirs carry them.
func (b *Broker) quarantinedShorts() map[string]bool {
	if b.peerMgr == nil {
		return nil
	}
	list := b.peerMgr.QuarantinedList()
	if len(list) == 0 {
		return nil
	}
	out := map[string]bool{}
	for _, q := range list {
		out[node.Short(q.Origin)] = true
	}
	return out
}

func fromQuarantined(nick string, shorts map[string]bool) bool {
	i := strings.LastIndexByte(nick, '@')
	return i >= 0 && shorts[nick[i+1:]]
}

func markMessages(msgs []proto.Message, shorts map[string]bool) {
	for i := range msgs {
		msgs[i].Quarantined = fromQuarantined(msgs[i].From, shorts)
	}
}

// markQuarantined sets the mark on what an answer shows of nodes in quarantine. The messages are
// copied: the history of the chat is not touched.
func (b *Broker) markQuarantined(data any) any {
	shorts := b.quarantinedShorts()
	if shorts == nil {
		return data
	}
	copied := func(m []proto.Message) []proto.Message {
		out := append([]proto.Message(nil), m...)
		markMessages(out, shorts)
		return out
	}
	switch r := data.(type) {
	case proto.HistoryResult:
		r.Messages = copied(r.Messages)
		return r
	case proto.SearchResult:
		r.Messages = copied(r.Messages)
		return r
	case proto.ContextResult:
		blocks := append([]proto.ContextBlock(nil), r.Blocks...)
		for i := range blocks {
			blocks[i].Messages = copied(blocks[i].Messages)
		}
		r.Blocks = blocks
		return r
	case proto.State:
		return markState(r, shorts)
	case proto.Welcome:
		r.State = markState(r.State, shorts)
		backlog := append([]proto.Event(nil), r.Backlog...)
		for i := range backlog {
			if backlog[i].Msg != nil {
				m := *backlog[i].Msg
				m.Quarantined = fromQuarantined(m.From, shorts)
				backlog[i].Msg = &m
			}
		}
		r.Backlog = backlog
		return r
	}
	return data
}

func markState(s proto.State, shorts map[string]bool) proto.State {
	members := append([]proto.MemberInfo(nil), s.Members...)
	for i := range members {
		members[i].Quarantined = fromQuarantined(members[i].Name, shorts)
	}
	s.Members = members
	return s
}

// quarantineLines is what `ccqc peer list` shows of the sources in quarantine.
func (b *Broker) quarantineLines() []string {
	if b.peerMgr == nil {
		return nil
	}
	var out []string
	for _, q := range b.peerMgr.QuarantinedList() {
		line := q.Origin + ": " + q.Reason + " (since " + q.Since + ", by " + q.By
		if q.Via != "" {
			line += ", brought by " + q.Via
		}
		out = append(out, line+")")
	}
	slices.Sort(out)
	return out
}
