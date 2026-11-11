package broker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/invite"
	"github.com/PHPCraftdream/cc-quant-chat/internal/node"
	"github.com/PHPCraftdream/cc-quant-chat/internal/peer"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// Inviting a node (docs/design-trust.md section 2): the administrator operations of the owner
// (peer_invite, peer_invite_revoke, peer_join, peer_code, peer_confirm) and what the broker does when
// the exchange layer reports a use of an invitation, a confirmation or a drop. The invitation string
// and the code are in the replies to the owner's own terminal and nowhere else: not in a chat, not
// in the log, not in the admin log (redactAdminArgs).

// redactAdminArgs keeps what must not be written down out of the admin log.
func redactAdminArgs(op string, canon []byte) []byte {
	switch op {
	case "peer_join":
		var a proto.PeerJoin
		if json.Unmarshal(canon, &a) == nil {
			a.Invitation = "(not logged)"
			out, _ := json.Marshal(a)
			return out
		}
	case "peer_confirm":
		var a proto.PeerCode
		if json.Unmarshal(canon, &a) == nil {
			a.Code = "(not logged)"
			out, _ := json.Marshal(a)
			return out
		}
	}
	return canon
}

const joinTimeout = 20 * time.Second

func (b *Broker) pairAdmin(op string, args json.RawMessage, at *store.AdminAttest) (any, *proto.Error) {
	pr, ok := b.store.(store.Pairing)
	if !ok || b.peerMgr == nil {
		return nil, proto.Errf("peers_unavailable", "inviting a node needs the SQLite store and a node key")
	}
	bad := func(err error) *proto.Error { return proto.Errf("bad_args", "%v", err) }
	switch op {
	case "peer_invite":
		var a proto.PeerInvite
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, bad(err)
		}
		return b.peerInvite(pr, a, at)
	case "peer_invite_revoke":
		var a proto.PeerInviteID
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, bad(err)
		}
		recs, err := pr.ListNodeInvites()
		if err != nil {
			return nil, proto.Errf("store_failed", "%v", err)
		}
		var hit []invite.Record
		for _, r := range recs {
			if a.ID != "" && strings.HasPrefix(r.ID, a.ID) {
				hit = append(hit, r)
			}
		}
		if len(hit) != 1 {
			return nil, proto.Errf("no_such_invite", "no single invitation of a node starts with %q", a.ID)
		}
		if done, err := pr.RevokeNodeInvite(hit[0].ID); err != nil {
			return nil, proto.Errf("store_failed", "%v", err)
		} else if !done {
			return nil, proto.Errf("no_such_invite", "that invitation is not open (used, expired, burnt or revoked already)")
		}
		return proto.BoardReply{Text: "invitation " + invite.Short(hit[0].ID) + " revoked"}, nil
	case "peer_join":
		var a proto.PeerJoin
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, bad(err)
		}
		in, err := invite.Parse(a.Invitation)
		if err != nil {
			return nil, bad(err)
		}
		if a.Trust == "" {
			a.Trust = store.TrustMember
		}
		ctx, cancel := context.WithTimeout(context.Background(), joinTimeout)
		defer cancel()
		res, err := b.peerMgr.Join(ctx, in, a.Trust)
		if err != nil {
			return nil, proto.Errf("join_failed", "%v", err)
		}
		log.Printf("broker: joined node %s at %s: pending until its code is confirmed (%s)", res.Origin, res.Addr, res.Until.Format(time.RFC3339))
		var names []string
		for _, c := range res.Chats {
			names = append(names, "/"+c.Name)
		}
		offered := ""
		if len(names) > 0 {
			offered = "; it offers the chats " + strings.Join(names, ", ") + " (made here once you both confirm)"
		}
		return proto.BoardReply{Text: fmt.Sprintf("joined node %s at %s%s.\nThe code to compare with the owner of that node: %s\nCompare it by voice or another channel; when it is the same, enter it: ccqc peer confirm %s <code>\nUnconfirmed, the node is dropped at %s.",
			res.Origin, res.Addr, offered, res.Code, res.Origin, res.Until.Local().Format("15:04"))}, nil
	case "peer_code":
		var a proto.PeerCode
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, bad(err)
		}
		code, ok, err := pr.PendingCode(a.Origin)
		if err != nil {
			return nil, proto.Errf("store_failed", "%v", err)
		}
		if !ok {
			return nil, proto.Errf("no_such_peer", "no node %s waits for a code", a.Origin)
		}
		return proto.BoardReply{Text: fmt.Sprintf("the code of node %s: %s\nCompare it with the owner of that node by voice or another channel, then: ccqc peer confirm %s <code>", a.Origin, code, a.Origin)}, nil
	case "peer_confirm":
		var a proto.PeerCode
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, bad(err)
		}
		a.Code = strings.TrimSpace(a.Code)
		res, err := b.peerMgr.ConfirmCode(a.Origin, a.Code)
		switch {
		case err != nil:
			return nil, proto.Errf("no_such_peer", "%v", err)
		case res.Gone:
			return nil, proto.Errf("bad_code", "wrong code, the third time: the node %s is dropped (invite it again if you want it)", a.Origin)
		case !res.Right:
			return nil, proto.Errf("bad_code", "wrong code (%d of %d); the other owner's code is the same as yours: ask them to read it again", res.Tries, 3)
		case res.Active:
			return proto.BoardReply{Text: "confirmed by both: node " + a.Origin + " is active"}, nil
		}
		return proto.BoardReply{Text: "confirmed here; waiting for the owner of node " + a.Origin + " to confirm too"}, nil
	}
	return nil, proto.Errf("unknown_op", "no administrator operation %q", op)
}

func (b *Broker) peerInvite(pr store.Pairing, a proto.PeerInvite, at *store.AdminAttest) (any, *proto.Error) {
	if a.Trust == "" {
		a.Trust = store.TrustMember
	}
	if !store.ValidTrust(a.Trust) {
		return nil, proto.Errf("bad_args", "trust is read, member or trusted, not %q", a.Trust)
	}
	ttl, err := invite.TTL(time.Duration(a.TTLSeconds) * time.Second)
	if err != nil {
		return nil, proto.Errf("bad_args", "%v", err)
	}
	addr := a.Addr
	if addr == "" {
		addr, _ = b.setting("", "peer.advertise")
	}
	if addr == "" {
		return nil, proto.Errf("no_address", "an invitation of a node carries the address this node is reached at: give --addr host:port, or set it once: ccqc config set peer.advertise host:port")
	}
	uri, _, err := peer.ParseAddr(addr)
	if err != nil {
		return nil, proto.Errf("bad_args", "%v", err)
	}
	if b.peerAt == "" {
		return nil, proto.Errf("not_listening", "this broker accepts no nodes yet: ccqc config set peer.listen host:port, then restart the broker")
	}
	scope := store.NodeScope{Trust: a.Trust}
	if len(a.Chats) > 0 {
		is, ok := b.store.(store.Invites)
		if !ok {
			return nil, proto.Errf("peers_unavailable", "the store does not name chats")
		}
		for _, id := range a.Chats {
			name, err := is.ChatName(id)
			if err != nil || name == "" {
				return nil, proto.Errf("no_such_chat", "this node has no chat %.8s", id)
			}
			scope.Chats = append(scope.Chats, store.ChatRef{ID: id, Name: name})
		}
	}
	now := time.Now()
	pub := b.peerMgr.Public()
	in, err := invite.New(invite.KindNode, ttl, now, func(i *invite.Invitation) {
		i.Addrs = []string{uri}
		i.Key = base64.StdEncoding.EncodeToString(pub)
	})
	if err != nil {
		return nil, proto.Errf("store_failed", "%v", err)
	}
	if err := pr.IssueNodeInvite(invite.NewRecord(in, scope.String(), at.Pub, now)); err != nil {
		return nil, proto.Errf("store_failed", "%v", err)
	}
	return proto.BoardReply{Text: in.String()}, nil
}

// onPair records what the exchange layer found out about a node being paired, and says it loudly to
// the chats offered to it (those that exist here).
func (b *Broker) onPair(ev peer.PairEvent) {
	log.Printf("broker: %s", ev.Text)
	args, _ := json.Marshal(map[string]string{"origin": ev.Origin})
	b.logAdmin("pair."+ev.Kind, args, ev.Text)
	fed, ok := b.store.(store.Federation)
	if !ok {
		return
	}
	switch ev.Kind {
	case peer.PairUsed, peer.PairActive:
		b.announcePeerChats(fed, ev.Origin, "node "+node.Short(ev.Origin)+": "+strings.TrimPrefix(ev.Text, "the "))
	case peer.PairDropped:
		for _, id := range ev.Peer.Chats {
			if ch, e := b.adminChat(id); e == nil {
				b.announceChat(ch, "node "+node.Short(ev.Origin)+": "+strings.TrimPrefix(ev.Text, "the "))
				b.leaveChat(ch)
			}
		}
	}
}

// onActive makes the chats an invited node offered (a chat of the same id is only shared; one of the
// same name but another id is made too: `chat find` shows both) and shares them with it.
func (b *Broker) onActive(origin string) {
	pr, ok1 := b.store.(store.Pairing)
	fed, ok2 := b.store.(store.Federation)
	is, ok3 := b.store.(store.Invites)
	ids, ok4 := b.store.(interface{ ChatIDs() ([]string, error) })
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return
	}
	chats, err := pr.PendingChats(origin)
	if err != nil {
		log.Printf("broker: pending chats of %s: %v", origin, err)
		return
	}
	have, _ := ids.ChatIDs()
	for _, c := range chats {
		if !slices.Contains(have, c.ID) {
			if err := is.NewChat(c.ID, c.Name); err != nil {
				log.Printf("broker: making chat /%s offered by %s: %v", c.Name, origin, err)
				continue
			}
		}
		if err := fed.SharePeerChat(c.ID, origin); err != nil {
			log.Printf("broker: sharing chat /%s with %s: %v", c.Name, origin, err)
		}
	}
}

// onAlert says loudly what the exchange layer found about a node that the owner should hear: to the
// log, the admin log and the chats shared with it.
func (b *Broker) onAlert(origin, text string) {
	log.Printf("broker: %s", text)
	args, _ := json.Marshal(map[string]string{"origin": origin})
	b.logAdmin("peer.alert", args, text)
	if fed, ok := b.store.(store.Federation); ok {
		b.announcePeerChats(fed, origin, text)
	}
}
