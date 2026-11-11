package mcp

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/PHPCraftdream/cc-quant-chat/internal/addr"
	"github.com/PHPCraftdream/cc-quant-chat/internal/client"
	"github.com/PHPCraftdream/cc-quant-chat/internal/local"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// join enters a chat under a nick. remember: join it automatically in this folder from now on.
// auto: called on session start; backlog goes out as events instead of the tool result.
func (s *Session) join(token, name, alias string, remember, auto bool) (string, error) {
	token = strings.TrimSpace(token)
	chatName, chatID, err := proto.ParseToken(token)
	if err != nil {
		return "", err
	}
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
		if cc.token == token {
			return "", fmt.Errorf("already joined /%s as @%s; use chat_state", lname, cc.name)
		}
		return "", &proto.Error{Code: "chat_name_conflict", Msg: fmt.Sprintf("already in another chat named %q; retry chat_join with alias", lname)}
	}
	s.mu.Unlock()

	key := local.NickKey(chatID, name)
	conn, err := client.Connect()
	if err != nil {
		return "", err
	}
	var w proto.Welcome
	if err := conn.Call("join", proto.JoinArgs{Token: token, Name: name, Key: key}, &w); err != nil {
		conn.Close()
		return "", err
	}
	if w.Key != "" {
		key = w.Key
		if err := local.SetNickKey(chatID, name, key); err != nil {
			log.Printf("save nick key: %v", err) // nick still works for this session
		}
	}
	cc := &chatConn{s: s, local: lname, token: token, name: name, key: key, conn: conn,
		role: w.State.MyRole, rooms: set(w.State.MyRooms)}

	s.mu.Lock()
	if s.chats[lname] != nil {
		s.mu.Unlock()
		conn.Close()
		return "", errors.New("concurrent chat_join for the same chat")
	}
	s.chats[lname] = cc
	s.mu.Unlock()
	go cc.run(conn)

	if remember {
		if err := local.RememberChat(local.ProjectDir(), local.Entry{Token: token, Name: name, Alias: alias}); err != nil {
			return "", fmt.Errorf("joined, but could not remember the chat for this folder: %v", err)
		}
	}
	if auto {
		cc.presence(fmt.Sprintf("auto-joined /%s as @%s", lname, name))
		for _, ev := range w.Backlog {
			cc.onEvent(ev)
		}
		return "", nil
	}

	var b strings.Builder
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
	if remember {
		b.WriteString("\nThis folder now joins this chat automatically on every session start.")
	}
	b.WriteString("\nNew chat messages will arrive by themselves.")
	return b.String(), nil
}

// autoJoin enters the chats remembered for this folder.
func (s *Session) autoJoin() {
	entries, err := local.FolderChats(local.ProjectDir())
	if err != nil {
		log.Printf("auto-join: %v", err)
		return
	}
	for _, e := range entries {
		if _, err := s.join(e.Token, e.Name, e.Alias, false, true); err != nil {
			s.emit("", nil, fmt.Sprintf("[/%s] auto-join as @%s failed: %v", e.Local(), e.Name, err), true)
		}
	}
}

// hookDelivery: CCQC_DELIVERY=hook means the rewake hook is configured, so events wait
// for it from the very start instead of going out as (possibly ignored) channel notifications.
func hookDelivery() bool { return os.Getenv("CCQC_DELIVERY") == "hook" }
