package broker

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/invite"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// Chats made for invitations and the invitations themselves (docs/design-invites.md). Making a chat
// and issuing, revoking and listing invitations are administrator operations, signed by the owner;
// finding a chat by its name is not one (a chat id is no longer a secret).

func inviteStore(b *Broker) (store.Invites, *proto.Error) {
	is, ok := b.store.(store.Invites)
	if !ok {
		return nil, proto.Errf("invites_unavailable", "chats and invitations need the SQLite store (the broker runs with CCQC_STORE=memory)")
	}
	return is, nil
}

// inviteAdmin runs the administrator operations on chats and invitations.
func (b *Broker) inviteAdmin(op string, args json.RawMessage, at *store.AdminAttest) (any, *proto.Error) {
	is, e := inviteStore(b)
	if e != nil {
		return nil, e
	}
	switch op {
	case "chat_new":
		var a proto.AdminChatNew
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, proto.Errf("bad_args", "%v", err)
		}
		if !proto.ValidName(a.Name) {
			return nil, proto.Errf("bad_name", "a chat name matches [a-z0-9-]{1,32}: %q", a.Name)
		}
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return nil, proto.Errf("store_failed", "%v", err)
		}
		id := hex.EncodeToString(raw)
		if err := is.NewChat(id, a.Name); err != nil {
			return nil, proto.Errf("store_failed", "%v", err)
		}
		return proto.BoardReply{Text: fmt.Sprintf("chat /%s made, id %s; let people in with: ccqc invite %s <nick>", a.Name, id, a.Name)}, nil
	case "invite_new":
		var a proto.AdminInviteNew
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, proto.Errf("bad_args", "%v", err)
		}
		if !proto.ValidName(a.Nick) || a.Nick == proto.OwnerNick {
			return nil, proto.Errf("bad_name", "the nick of an invitation matches [a-z0-9-]{1,32} and is not @%s: %q", proto.OwnerNick, a.Nick)
		}
		ttl, err := invite.TTL(time.Duration(a.TTLSeconds) * time.Second)
		if err != nil {
			return nil, proto.Errf("bad_args", "%v", err)
		}
		name, err := is.ChatName(a.Chat) // before the chat is taken: taking an unknown id would make a chat of it
		if err != nil || name == "" {
			return nil, proto.Errf("no_such_chat", "this node has no chat with that id")
		}
		ch, e := b.adminChat(a.Chat)
		if e != nil {
			return nil, e
		}
		defer b.leaveChat(ch)
		now := time.Now()
		in, err := invite.New(invite.KindChat, ttl, now, func(i *invite.Invitation) { i.ChatID, i.Chat, i.Nick = a.Chat, name, a.Nick })
		if err != nil {
			return nil, proto.Errf("store_failed", "%v", err)
		}
		if err := is.IssueInvite(a.Chat, invite.NewRecord(in, a.Nick, at.Pub, now), *at); err != nil {
			return nil, proto.Errf("store_failed", "%v", err)
		}
		// The one place the string exists apart from the person it goes to: this reply, to the owner's
		// own terminal. It is in no log and no record.
		return proto.BoardReply{Text: in.String()}, nil
	case "invite_revoke", "invite_list":
		var a proto.AdminInviteID
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, proto.Errf("bad_args", "%v", err)
		}
		if name, err := is.ChatName(a.Chat); err != nil || name == "" {
			return nil, proto.Errf("no_such_chat", "this node has no chat with that id")
		}
		ch, e := b.adminChat(a.Chat)
		if e != nil {
			return nil, e
		}
		defer b.leaveChat(ch)
		list, err := is.ListInvites(a.Chat)
		if err != nil {
			return nil, proto.Errf("store_failed", "%v", err)
		}
		if op == "invite_list" {
			return proto.BoardReply{Text: formatInvites(list, time.Now())}, nil
		}
		var match []invite.Record
		for _, r := range list {
			if len(a.ID) >= 8 && strings.HasPrefix(r.ID, a.ID) {
				match = append(match, r)
			}
		}
		if len(match) != 1 {
			return nil, proto.Errf("bad_args", "name one invitation by the first 8 or more characters of its id (ccqc invite list); %d match %q", len(match), a.ID)
		}
		ok, err := is.RevokeInvite(a.Chat, match[0].ID, *at)
		if err != nil {
			return nil, proto.Errf("store_failed", "%v", err)
		}
		if !ok {
			return nil, proto.Errf("not_open", "that invitation is %s, not open", match[0].State)
		}
		return proto.BoardReply{Text: "invitation " + invite.Short(match[0].ID) + " revoked"}, nil
	}
	return nil, proto.Errf("unknown_op", "no administrator operation %q", op)
}

// formatInvites lists the invitations of a chat: the first characters of the id, the nick, the
// time, the state. Nothing in it can be used to enter.
func formatInvites(list []invite.Record, now time.Time) string {
	if len(list) == 0 {
		return "no invitations"
	}
	var sb strings.Builder
	for _, r := range list {
		state := r.State
		if state == invite.StateOpen && !now.Before(r.ExpiresAt) {
			state = "expired"
		}
		fmt.Fprintf(&sb, "%s  @%s  until %s  %s\n", invite.Short(r.ID), r.Scope, r.ExpiresAt.UTC().Format(time.RFC3339), state)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// chatFind answers which chats of this node have a name.
func (b *Broker) chatFind(a proto.ChatFindArgs) (any, *proto.Error) {
	is, e := inviteStore(b)
	if e != nil {
		return nil, e
	}
	found, err := is.FindChats(a.Name)
	if err != nil {
		return nil, proto.Errf("store_failed", "%v", err)
	}
	out := proto.ChatFindReply{Chats: []proto.ChatRef{}}
	for _, c := range found {
		out.Chats = append(out.Chats, proto.ChatRef{ID: c.ID, Name: c.Name})
	}
	return out, nil
}
