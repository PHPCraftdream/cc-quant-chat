package mcp

import (
	"github.com/PHPCraftdream/cc-quant-chat/internal/client"
	"github.com/PHPCraftdream/cc-quant-chat/internal/invite"
	"github.com/PHPCraftdream/cc-quant-chat/internal/local"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// idsFor says what the session knows of its id: the current one and, after /clear or a compaction, the one
// the broker has the nick bound to and why it changed (kept with the chat, so it survives a restart).
func (s *Session) idsFor(localName string) client.SessionIDs {
	sid := s.sessionID()
	ids := client.SessionIDs{SID: sid}
	if sid == "" {
		return ids
	}
	entries, _ := local.SessionChats(local.ProjectDir(), sid)
	for _, e := range entries {
		if e.Local() == localName && e.Bound != "" && e.Bound != sid {
			ids.PrevSID, ids.Source = e.Bound, e.BoundVia
		}
	}
	return ids
}

// connectKeys gets the connection into the chat (client.JoinWithKeys); localName is the chat's name
// for this session.
func (s *Session) connectKeys(conn *client.Conn, t joinTarget, name, localName string, inv *invite.Invitation, w *proto.Welcome) (client.KeyJoin, error) {
	return client.JoinWithKeys(conn.Call, client.KeyJoinInput{ChatID: t.id, Token: t.token, Nick: name, Invite: inv, SID: s.sessionID(), IDs: s.idsFor(localName)}, w)
}
