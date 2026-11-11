package broker

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/owner"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// Administrator operations: pinning roles, changing settings, freeing a nick. Each is signed with
// the owner key over the operation, its canonical arguments and a challenge of the connection, so
// nothing an agent can send is enough. Every attempt, refused ones included, is logged.

const adminRefusal = "this is an administrator operation: only the owner can do it, from their own terminal (ccqc owner ..., ccqc role ..., ccqc config ...); do not look for a way around, ask the user"

func (b *Broker) logAdmin(op string, args []byte, result string) {
	if as, ok := b.store.(store.AdminStore); ok {
		if err := as.LogAdmin(store.AdminEntry{TS: time.Now().UTC().Format(time.RFC3339), Op: op, Args: string(args), Result: result, Origin: b.origin}); err != nil {
			log.Printf("broker: admin log: %v", err)
		}
	}
}

// admin verifies the owner's signature and runs the operation.
func (b *Broker) admin(c *conn, a proto.AdminArgs) (any, *proto.Error) {
	canon, cerr := proto.CanonicalJSON(a.Args)
	if cerr != nil {
		b.logAdmin(a.Op, a.Args, "refused: arguments are not JSON")
		return nil, proto.Errf("bad_args", "args is not a JSON value")
	}
	refuse := func(code, msg, why string) (any, *proto.Error) {
		b.logAdmin(a.Op, canon, "refused: "+why)
		return nil, proto.Errf(code, "%s", msg)
	}
	ownerPub := b.ownerKey()
	if ownerPub == nil {
		return refuse("no_owner", "no owner is registered yet: the owner runs `ccqc owner init` first", "no owner")
	}
	nonce, at := c.nonce, c.nonceAt
	c.nonce = "" // spent whatever happens
	sig := decodeSig(a.Sig)
	switch {
	case sig == nil:
		return refuse("admin_only", adminRefusal, "no valid signature")
	case nonce == "" || time.Since(at) > challengeTTL:
		return refuse("admin_only", adminRefusal, "no valid challenge")
	case !ed25519.Verify(ownerPub, proto.AdminMessage(a.Op, canon, nonce), sig):
		return refuse("admin_only", adminRefusal, "the signature does not verify")
	}
	attest := &store.AdminAttest{Op: a.Op, Args: string(canon), Nonce: nonce, Sig: a.Sig, Pub: owner.Encode(ownerPub)}
	res, e := b.runAdmin(a.Op, a.Args, attest)
	if e != nil {
		b.logAdmin(a.Op, canon, "failed: "+e.Code)
		return nil, e
	}
	b.logAdmin(a.Op, canon, "ok")
	return res, nil
}

// runAdmin runs a verified administrator operation. A chat it needs is taken (loaded if necessary)
// without the broker's lock held, like any other use of a chat.
func (b *Broker) runAdmin(op string, args json.RawMessage, at *store.AdminAttest) (any, *proto.Error) {
	switch op {
	case "role_give", "role_take":
		var r proto.AdminRole
		if err := json.Unmarshal(args, &r); err != nil {
			return nil, proto.Errf("bad_args", "%v", err)
		}
		ch, e := b.adminChat(r.Chat)
		if e != nil {
			return nil, e
		}
		defer b.leaveChat(ch)
		if op == "role_give" {
			return b.pinRole(ch, r.Nick, r.Role, at)
		}
		return b.unpinRole(ch, r.Nick, at)
	case "config_set":
		var c proto.AdminConfig
		if err := json.Unmarshal(args, &c); err != nil {
			return nil, proto.Errf("bad_args", "%v", err)
		}
		if c.Chat != "" { // the chat is held while its setting is stored: it is not released under the write
			ch, e := b.adminChat(c.Chat)
			if e != nil {
				return nil, e
			}
			defer b.leaveChat(ch)
		}
		v, e := b.setConfig(c.Chat, c.Key, c.Value, at)
		if e != nil {
			return nil, e
		}
		return proto.BoardReply{Text: fmt.Sprintf("%s = %q", c.Key, v)}, nil
	case "nick_reset":
		var r proto.AdminRole
		if err := json.Unmarshal(args, &r); err != nil {
			return nil, proto.Errf("bad_args", "%v", err)
		}
		return b.resetNick(r.Chat, r.Nick, at)
	case "peer_add", "peer_remove", "peer_share", "peer_unshare":
		return b.peerAdmin(op, args)
	}
	return nil, proto.Errf("unknown_op", "no administrator operation %q", op)
}

// adminChat takes the chat with that id (64 hex characters), loading it if needed; it comes back
// locked and held. Give it back with leaveChat.
func (b *Broker) adminChat(id string) (*chat, *proto.Error) {
	if len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" {
		return nil, proto.Errf("bad_args", "chat is the 64-hex id of a chat")
	}
	ch, err := b.useChat(id, "")
	if err != nil {
		return nil, errNoStore(err)
	}
	return ch, nil
}

// leaveChat gives a chat back and lets it go if nobody is in it.
func (b *Broker) leaveChat(ch *chat) {
	b.doneChat(ch)
	b.unloadIfEmpty(ch)
}

// announceChat tells the present members of one chat, loudly. The caller holds the chat's lock.
func (b *Broker) announceChat(ch *chat, text string) {
	for name, m := range ch.members {
		if m.conn != nil {
			b.deliver(ch, name, proto.Event{Kind: "notice", Text: text, Wake: true})
		}
	}
}

// pinnedHolder: the nick a role name is pinned to, if any other than except.
func (ch *chat) pinnedHolder(role, except string) string {
	for name, n := range ch.nicks {
		if n.pinned == role && name != except {
			return name
		}
	}
	return ""
}

// pinRole gives a nick a role it can neither change nor release.
func (b *Broker) pinRole(ch *chat, nickName, role string, at *store.AdminAttest) (any, *proto.Error) {
	nk := ch.nicks[nickName]
	if nk == nil {
		return nil, proto.Errf("no_such_member", "no nick @%s in this chat", nickName)
	}
	if !proto.ValidName(role) {
		return nil, proto.Errf("bad_name", "role must match [a-z0-9-]{1,32}: %q", role)
	}
	if r := ch.roles[role]; r != nil {
		for holder := range r.holders {
			if holder != nickName && ch.nicks[holder] != nil && ch.nicks[holder].pinned != role {
				return nil, proto.Errf("role_taken", "@%s holds role %q; it must release it first", holder, role)
			}
		}
	}
	if e := ch.save(store.Record{T: "pin", Nick: nickName, Role: role, Admin: at}); e != nil {
		return nil, e
	}
	nk.pinned = role
	if m := ch.members[nickName]; m != nil {
		b.release(ch, m)
		b.holdRole(ch, m, role)
	}
	b.announceChat(ch, fmt.Sprintf("the owner pinned the role %s to @%s", role, nickName))
	return proto.BoardReply{Text: fmt.Sprintf("role %s is pinned to @%s", role, nickName)}, nil
}

func (b *Broker) unpinRole(ch *chat, nickName string, at *store.AdminAttest) (any, *proto.Error) {
	nk := ch.nicks[nickName]
	if nk == nil {
		return nil, proto.Errf("no_such_member", "no nick @%s in this chat", nickName)
	}
	if nk.pinned == "" {
		return nil, proto.Errf("no_pinned_role", "@%s has no pinned role", nickName)
	}
	old := nk.pinned
	if e := ch.save(store.Record{T: "pin", Nick: nickName, Role: "", Admin: at}); e != nil {
		return nil, e
	}
	nk.pinned = ""
	if m := ch.members[nickName]; m != nil && m.role == old {
		b.release(ch, m)
	}
	b.announceChat(ch, fmt.Sprintf("the owner took the role %s from @%s", old, nickName))
	return proto.BoardReply{Text: fmt.Sprintf("@%s no longer has the pinned role %s", nickName, old)}, nil
}

// holdRole makes m hold role as a shared role (what a pinned role is).
func (b *Broker) holdRole(ch *chat, m *member, roleName string) {
	r := ch.roles[roleName]
	if r == nil {
		r = &role{holders: map[string]bool{}}
		ch.roles[roleName] = r
	}
	r.holders[m.name] = true
	m.role = roleName
}

// displayRole is the role as shown next to a nick: pinned ones carry a star, which role_claim
// can never produce (role names are [a-z0-9-]).
func (ch *chat) displayRole(m *member) string {
	if nk := ch.nicks[m.name]; nk != nil && nk.pinned != "" && m.role == nk.pinned {
		return m.role + "*"
	}
	return m.role
}

// resetNick frees an offline nick, as the owner's administrator operation (nickReset is the older
// token-based one, which only works while no owner exists).
func (b *Broker) resetNick(chatID, nickName string, at *store.AdminAttest) (any, *proto.Error) {
	ch, e := b.adminChat(chatID)
	if e != nil {
		return nil, e
	}
	defer b.leaveChat(ch)
	nk := ch.nicks[nickName]
	if nk == nil {
		return nil, proto.Errf("no_such_member", "no nick @%s in this chat", nickName)
	}
	if ch.members[nickName] != nil {
		return nil, proto.Errf("name_online", "@%s is online or was online a moment ago; reset works for an offline nick (wait about a minute)", nickName)
	}
	if e := ch.save(store.Record{T: "nick_key", Nick: nickName, Hash: "", Admin: at}); e != nil {
		return nil, e
	}
	nk.hash = ""
	b.presence(ch, nil, "", "the key of @"+nickName+" was reset: the next join under this nick takes it over")
	return proto.BoardReply{Text: "nick @" + nickName + " is free"}, nil
}
