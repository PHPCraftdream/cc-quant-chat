package mcp

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/PHPCraftdream/cc-quant-chat/internal/addr"
	"github.com/PHPCraftdream/cc-quant-chat/internal/client"
	"github.com/PHPCraftdream/cc-quant-chat/internal/invite"
	"github.com/PHPCraftdream/cc-quant-chat/internal/local"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// joinTarget names the chat a join is for: an older chat by its token, a chat made for invitations by
// its id (and name, which the chat tells and the invitation carries).
type joinTarget struct{ token, id, name string }

// join enters a chat by its token under a nick and remembers it for this Claude Code session, so
// resuming the session joins again. auto: called on session start; backlog goes out as events.
func (s *Session) join(token, name, alias string, auto bool) (string, error) {
	token = strings.TrimSpace(token)
	chatName, chatID, err := proto.ParseToken(token)
	if err != nil {
		return "", err
	}
	return s.joinTo(joinTarget{token, chatID, chatName}, name, alias, auto, nil)
}

// joinEntry joins again a chat remembered for the session: by token, or by id and the nick's key.
func (s *Session) joinEntry(e local.Entry) (string, error) {
	if e.Token != "" {
		return s.join(e.Token, e.Name, e.Alias, true)
	}
	return s.joinTo(joinTarget{id: e.ChatID, name: e.Chat}, e.Name, e.Alias, true, nil)
}

// joinInvite enters a chat with a one-time invitation. The invitation is spent by this join and kept
// nowhere: not in the session's chats, not in the reply, not in an error. The nick is the one the
// invitation is for.
func (s *Session) joinInvite(line, alias string) (string, error) {
	in, err := invite.Parse(line)
	if err != nil {
		return "", err // says what is wrong, never what was typed
	}
	if in.Kind != invite.KindChat {
		return "", errors.New("this invitation is not for a chat")
	}
	return s.joinTo(joinTarget{id: in.ChatID, name: in.Chat}, in.Nick, alias, false, &in)
}

// joinTo is the join of a session: the key of the nick, the connection, the state afterwards.
func (s *Session) joinTo(t joinTarget, name, alias string, auto bool, inv *invite.Invitation) (string, error) {
	chatName, chatID := t.name, t.id
	lname := chatName
	if alias != "" {
		lname = addr.Chat(alias)
		if !proto.ValidName(lname) {
			return "", fmt.Errorf("bad alias %q: want [a-z0-9-]{1,32}", alias)
		}
	}
	name = strings.TrimPrefix(strings.TrimSpace(name), "@")
	if name == "" {
		return "", errors.New("a nick is required: chat_join(token, name=<your nick>)")
	}
	if !proto.ValidName(name) {
		return "", fmt.Errorf("bad nick %q: want [a-z0-9-]{1,32}", name)
	}

	s.mu.Lock()
	if cc := s.chats[lname]; cc != nil {
		s.mu.Unlock()
		if cc.id == chatID {
			return "", fmt.Errorf("already joined /%s as @%s; use chat_state", lname, cc.name)
		}
		return "", &proto.Error{Code: "chat_name_conflict", Msg: fmt.Sprintf("already in another chat named %q; retry chat_join with alias", lname)}
	}
	s.mu.Unlock()

	key := ""
	if t.token != "" {
		key = local.NickKey(chatID, name) // the nick's old secret: moves a legacy nick to its key
	}
	var priv ed25519.PrivateKey
	if inv != nil { // a new key: the invitation binds it to the nick (and replaces the old one, if any)
		_, fresh, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return "", err
		}
		priv = fresh
	} else {
		var err error
		if priv, err = local.NickPrivateKey(chatID, name); err != nil {
			return "", fmt.Errorf("the nick key could not be read or saved: %w", err)
		}
	}
	conn, err := client.Connect()
	if err != nil {
		return "", err
	}
	var w proto.Welcome
	var signer *client.Signer
	if inv != nil {
		signer, err = client.JoinInvited(conn.Call, name, priv, *inv, &w)
	} else {
		args := proto.JoinArgs{Name: name, Key: key}
		if t.token != "" {
			args.Token = t.token
		} else {
			args.Chat = chatID
		}
		signer, err = client.JoinSigned(conn.Call, args, priv, chatID, &w)
	}
	if err != nil {
		conn.Close()
		return "", err
	}
	keyNote := ""
	if inv != nil { // the invitation is spent: the key it registered is the way in from now on
		if err := local.SetNickPrivateKey(chatID, name, priv); err != nil {
			log.Printf("save nick key: %v", err)
			keyNote = fmt.Sprintf("WARNING: the key of nick @%s could not be saved (%v). It works until this session ends; after that the owner must issue a new invitation.\n\n", name, err)
		}
	}
	if w.Key != "" {
		key = w.Key
		if err := local.SetNickKey(chatID, name, key); err != nil {
			log.Printf("save nick key: %v", err)
			// The nick works for this session only; after that its key is lost.
			keyNote = fmt.Sprintf("WARNING: the key of nick @%s could not be saved (%v). It works until this session ends; after that `ccqc nick reset - %s` (token on stdin) is needed to use it again.\n\n", name, err, name)
		}
	}
	brokerNote := ""
	if w.Proto < proto.SigningProto {
		brokerNote = "the broker runs an older ccqc that does not sign events: `ccqc broker restart` updates it (sessions reconnect by themselves; best done while the chats are quiet)"
	}
	cc := &chatConn{s: s, local: lname, token: t.token, id: chatID, name: name, key: key, priv: priv, signer: signer, conn: conn,
		role: w.State.MyRole, rooms: set(w.State.MyRooms)}

	s.mu.Lock()
	if s.chats[lname] != nil {
		s.mu.Unlock()
		conn.Close()
		return "", errors.New("concurrent chat_join for the same chat")
	}
	s.chats[lname] = cc
	s.mu.Unlock()

	// The backlog is handled before live events are read, so ids stay in order for the dedupe.
	remembered := ""
	if !auto {
		remembered = s.remember(local.Entry{Token: t.token, ChatID: chatID, Chat: chatName, Name: name, Alias: alias})
	}
	if auto {
		cc.presence(fmt.Sprintf("auto-joined /%s as @%s", lname, name))
		if w.Warning != "" {
			cc.presence("warning: " + w.Warning)
		}
		if brokerNote != "" {
			cc.presence("warning: " + brokerNote)
		}
		for _, ev := range w.Backlog {
			cc.onEvent(ev)
		}
		go cc.run(conn)
		go cc.pushStatus(cc.fullStatus())
		return "", nil
	}
	if n := len(w.Backlog); n > 0 && w.Backlog[n-1].Msg != nil { // shown in the result below
		cc.fresh(w.Backlog[n-1].Msg.ID)
		defer ackRef{cc, w.Backlog[n-1].Msg.ID}.confirm()
	}
	go cc.run(conn)
	go cc.pushStatus(cc.fullStatus())

	var b strings.Builder
	b.WriteString(keyNote)
	if w.Warning != "" {
		b.WriteString("warning: " + w.Warning + "\n\n")
	}
	if brokerNote != "" {
		b.WriteString("warning: " + brokerNote + "\n\n")
	}
	b.WriteString(renderState(lname, &w.State))
	if w.UnreadTotal > 0 {
		fmt.Fprintf(&b, "\nunread since you were last here: %d", w.UnreadTotal)
		if w.UnreadTotal > len(w.Backlog) {
			fmt.Fprintf(&b, " (newest %d shown; older: chat_timeline / chat_history)", len(w.Backlog))
		}
		b.WriteString("\n")
		for _, ev := range w.Backlog {
			if ev.Msg != nil {
				b.WriteString("  " + fmtMsg(lname, ev.Msg) + "\n")
			}
		}
	}
	b.WriteString("\n" + remembered)
	b.WriteString("\nNew chat messages will arrive by themselves.")
	return b.String(), nil
}

// remember stores the chat for this session; returns a note for the tool result.
func (s *Session) remember(e local.Entry) string {
	sid := s.sessionID()
	if sid == "" {
		return "Not remembered for this session: the ccqc hooks are not active here (run /quant-setup, restart Claude Code)."
	}
	if err := local.RememberChat(local.ProjectDir(), sid, e); err != nil {
		return "Could not remember the chat for this session: " + err.Error()
	}
	return "Remembered for this session: resuming it joins this chat again."
}

func (s *Session) sessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sid
}
