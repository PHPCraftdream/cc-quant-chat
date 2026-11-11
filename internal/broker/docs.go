package broker

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/docs"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

var docVerbs = map[string]string{"put": "updated", "append": "appended to", "move": "moved", "delete": "deleted", "revert": "reverted"}

// canDocs is the one place that decides whether an actor may do a document operation. Every
// member of the chat may do everything for now.
func (b *Broker) canDocs(who actor, op, path string) *proto.Error { return nil }

// docOp serves one request of the chat's document tree. Writes share the board's limit.
func (b *Broker) docOp(ch *chat, m *member, a proto.DocArgs, raw json.RawMessage) (any, *proto.Error) {
	ds, ok := b.store.(store.Docs)
	if !ok {
		return nil, proto.Errf("docs_unavailable", "the document tree needs the SQLite store (the broker runs with CCQC_STORE=memory)")
	}
	who := b.actorOf(ch, m)
	if e := b.canDocs(who, a.Op, a.Path); e != nil {
		return nil, e
	}
	if proto.DocWrite(a.Op) {
		if b.stopping.Load() {
			return nil, proto.Errf("restarting", "the broker is restarting; repeat in a moment")
		}
		sg, e := b.verifyWrite(ch, m, "doc."+a.Op, raw)
		if e != nil {
			return nil, e
		}
		if !b.allow(ch, m.name, "board", boardLimit) {
			return nil, proto.Errf("rate_limited", "max %d board and document changes per minute", boardLimit)
		}
		var res docs.Result
		err := ds.DoDocs(ch.id, func(tx store.DocsTx) error {
			var err error
			res, err = applyDoc(docs.Ctx{Tx: tx, Actor: m.name, Owner: m.owner, Origin: b.origin, Sig: sg, Now: time.Now()}, a)
			return err
		})
		if err != nil {
			return nil, boardError(err)
		}
		if res.Changed {
			// A quiet line in the presence stream; nobody is woken by a page.
			b.presence(ch, nil, m.name, fmt.Sprintf("%s %s %s v%d", m.name, docVerbs[a.Op], res.Path, res.Version))
		}
		return proto.DocReply{Text: res.Text, Version: res.Version}, nil
	}
	var reply proto.DocReply
	err := ds.ViewDocs(ch.id, func(tx store.DocsTx) error {
		var err error
		reply, err = readDoc(tx, a)
		return err
	})
	if err != nil {
		return nil, boardError(err)
	}
	return reply, nil
}

func applyDoc(c docs.Ctx, a proto.DocArgs) (docs.Result, error) {
	switch a.Op {
	case "put":
		return docs.Put(c, a.Path, a.Title, a.Body, a.Base, a.Note)
	case "append":
		return docs.Append(c, a.Path, a.Text, a.Base)
	case "move":
		return docs.Move(c, a.Path, a.To)
	case "delete":
		return docs.Delete(c, a.Path)
	case "revert":
		return docs.Revert(c, a.Path, a.Version)
	}
	return docs.Result{}, proto.Errf("unknown_op", "doc op %q", a.Op)
}

func readDoc(tx store.DocsTx, a proto.DocArgs) (proto.DocReply, error) {
	var r proto.DocReply
	var err error
	switch a.Op {
	case "tree":
		r.Text, r.Paths, err = docs.Tree(tx, a.Prefix, a.Depth)
		if !a.All {
			r.Paths = nil
		}
	case "outline":
		r.Text, err = docs.Outline(tx, a.Path)
	case "get":
		r.Text, r.Body, r.Version, err = docs.Get(tx, a.Path, a.Section, a.Version, a.Full)
	case "search":
		r.Text, err = docs.Search(tx, a.Query, a.Prefix)
	case "history":
		r.Text, err = docs.History(tx, a.Path, a.Limit, a.Before)
	default:
		err = proto.Errf("unknown_op", "doc op %q", a.Op)
	}
	return r, err
}
