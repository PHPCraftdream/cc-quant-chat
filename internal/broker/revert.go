package broker

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/node"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// Reverting the contribution of a node (docs/design-trust.md section 6): the owner's operation. In
// every chat that holds events of the node a signed event is written to the journal and the
// projections are built without what the node gave from the time asked for (and without what depended
// on it); the journal loses nothing, and the revert can be undone.

// parseSince reads "", a time (RFC 3339) or how long ago ("36h", "7d").
func parseSince(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format(time.RFC3339), nil
	}
	if strings.HasSuffix(s, "d") {
		var n int
		if _, err := fmt.Sscanf(strings.TrimSuffix(s, "d"), "%d", &n); err == nil && n > 0 {
			return time.Now().Add(-time.Duration(n) * 24 * time.Hour).UTC().Format(time.RFC3339), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return time.Now().Add(-d).UTC().Format(time.RFC3339), nil
	}
	return "", fmt.Errorf("--since is a time like 2026-03-01T12:00:00Z, or how long ago: 36h, 7d")
}

func (b *Broker) revertAdmin(args json.RawMessage, at *store.AdminAttest) (any, *proto.Error) {
	rv, ok := b.store.(store.Reverts)
	fed, ok2 := b.store.(store.Federation)
	ids, ok3 := b.store.(interface{ ChatIDs() ([]string, error) })
	if !ok || !ok2 || !ok3 || b.peerMgr == nil || at == nil {
		return nil, proto.Errf("peers_unavailable", "reverting a node needs the SQLite store")
	}
	var a proto.PeerRevert
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, proto.Errf("bad_args", "%v", err)
	}
	chats, err := ids.ChatIDs()
	if err != nil {
		return nil, proto.Errf("store_failed", "%v", err)
	}
	if a.Undo != "" {
		return b.undoRevert(rv, chats, a.Undo, at)
	}
	if !originName.MatchString(a.Origin) {
		return nil, proto.Errf("bad_args", "origin is 32 hex characters")
	}
	if a.Origin == b.origin {
		return nil, proto.Errf("bad_args", "that is this node")
	}
	since, err := parseSince(a.Since)
	if err != nil {
		return nil, proto.Errf("bad_args", "%v", err)
	}
	raw := make([]byte, 6)
	_, _ = rand.Read(raw)
	id := hex.EncodeToString(raw)
	var done []string
	var left, own int
	for _, chatID := range chats {
		vec, err := fed.Vector(chatID)
		if err != nil || vec[a.Origin] == 0 {
			continue
		}
		rep, name, e := b.revertIn(chatID, func() (store.RevertReport, error) { return rv.RevertNode(chatID, id, a.Origin, since, *at) })
		if e != nil {
			return nil, e
		}
		left, own = left+rep.Skipped, own+rep.SkippedOwn
		done = append(done, "/"+name)
		b.say(chatID, fmt.Sprintf("node %s: its contribution is reverted%s (%d events left out, %d of this node's own that depended on them); undo: ccqc peer revert --undo %s", node.Short(a.Origin), sinceText(since), rep.Skipped, rep.SkippedOwn, id))
	}
	if len(done) == 0 {
		return nil, proto.Errf("no_such_events", "no chat of this node holds events of %s", a.Origin)
	}
	log.Printf("broker: reverted node %s in %s (revert %s)", a.Origin, strings.Join(done, ", "), id)
	return proto.BoardReply{Text: fmt.Sprintf("node %s reverted%s in %s: %d events left out, %d of this node's own events that depended on them left out too (replies to its messages stay, without the link). The journal keeps everything. Undo: ccqc peer revert --undo %s",
		a.Origin, sinceText(since), strings.Join(done, ", "), left, own, id)}, nil
}

func sinceText(since string) string {
	if since == "" {
		return ""
	}
	return " since " + since
}

func (b *Broker) undoRevert(rv store.Reverts, chats []string, id string, at *store.AdminAttest) (any, *proto.Error) {
	var done []string
	for _, chatID := range chats {
		list, err := rv.Reverts(chatID)
		if err != nil {
			return nil, proto.Errf("store_failed", "%v", err)
		}
		in := false
		for _, r := range list {
			in = in || (r.ID == id && !r.Undone)
		}
		if !in {
			continue
		}
		if _, name, e := b.revertIn(chatID, func() (store.RevertReport, error) { return rv.UndoRevert(chatID, id, *at) }); e != nil {
			return nil, e
		} else {
			done = append(done, "/"+name)
		}
		b.say(chatID, "a revert of a node is undone ("+id+"): what it left out is shown again")
	}
	if len(done) == 0 {
		return nil, proto.Errf("no_such_revert", "no revert %q is in force (ccqc peer list shows them)", id)
	}
	return proto.BoardReply{Text: "revert " + id + " undone in " + strings.Join(done, ", ") + ": everything it left out is shown again"}, nil
}

// revertIn runs a change of the projections of a chat with the chat held, then makes the broker's
// copy of it match the store.
func (b *Broker) revertIn(chatID string, do func() (store.RevertReport, error)) (store.RevertReport, string, *proto.Error) {
	ch, e := b.adminChat(chatID)
	if e != nil {
		return store.RevertReport{}, "", e
	}
	defer b.leaveChat(ch)
	rep, err := do()
	if err != nil {
		return rep, ch.name, proto.Errf("store_failed", "%v", err)
	}
	if err := b.reloadChat(ch); err != nil {
		return rep, ch.name, proto.Errf("store_failed", "the chat could not be read again: %v", err)
	}
	return rep, ch.name, nil
}

// reloadChat makes what the broker keeps in memory of a chat (nicks, rooms, messages) match the store
// again after the projections were built anew. The members present stay. The numbers of messages
// already handed out are not given again.
func (b *Broker) reloadChat(ch *chat) error {
	recs, err := b.store.Load(ch.id, ch.name)
	if err != nil {
		return err
	}
	fresh := newChat(ch.id, ch.name)
	for _, r := range recs {
		fresh.replay(r)
	}
	ch.nicks, ch.rooms, ch.dms, ch.where = fresh.nicks, fresh.rooms, fresh.dms, fresh.where
	ch.nextID = max(ch.nextID, fresh.nextID)
	return nil
}

// say tells a chat loudly (it is loaded for that, if it is not).
func (b *Broker) say(chatID, text string) {
	if ch, e := b.adminChat(chatID); e == nil {
		b.announceChat(ch, text)
		b.leaveChat(ch)
	}
}

// revertLines is what `ccqc peer list` says of the reverts: per node, what they left out.
func (b *Broker) revertStats() map[string]store.RevertReport {
	out := map[string]store.RevertReport{}
	rv, ok := b.store.(store.Reverts)
	ids, ok2 := b.store.(interface{ ChatIDs() ([]string, error) })
	if !ok || !ok2 {
		return out
	}
	chats, _ := ids.ChatIDs()
	for _, id := range chats {
		st, err := rv.RevertStats(id)
		if err != nil {
			continue
		}
		for origin, r := range st {
			cur := out[origin]
			cur.Skipped += r.Skipped
			cur.SkippedOwn += r.SkippedOwn
			out[origin] = cur
		}
	}
	return out
}
