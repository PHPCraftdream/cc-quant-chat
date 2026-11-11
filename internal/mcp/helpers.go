package mcp

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/ipc"
	"github.com/PHPCraftdream/cc-quant-chat/internal/local"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proc"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// RunTail is a monitor: prints this session's chat events, one per line.
func RunTail() error {
	key, err := proc.SessionKey()
	if err != nil {
		return err
	}
	for {
		c, err := ipc.DialSession(key, 2*time.Second)
		if err != nil {
			time.Sleep(2 * time.Second) // MCP server not up yet or restarting
			continue
		}
		if _, err := c.Write([]byte("tail\n")); err == nil {
			_, _ = io.Copy(os.Stdout, c)
		}
		c.Close()
		time.Sleep(time.Second)
	}
}

// RunHello is a SessionStart hook: tells Claude which chats this folder joins automatically.
func RunHello() {
	go func() { _, _ = io.Copy(io.Discard, os.Stdin) }()
	entries, err := local.FolderChats(local.ProjectDir())
	if err != nil || len(entries) == 0 {
		return
	}
	var chats []string
	for _, e := range entries {
		chats = append(chats, "/"+e.Local()+" as @"+e.Name)
	}
	fmt.Printf("ccqc chat: this session joins %s automatically. Incoming chat messages wake you by themselves "+
		"(as \"ccqc: N new chat event(s)\"). Send with chat_send to /<chat>/@common[/@nick], /<chat>/@<room>[/@nick] "+
		"or /<chat>/@direct/@nick; catch up with chat_timeline, chat_history, chat_search.\n", strings.Join(chats, ", "))
}

// RunRemember makes the current folder join a chat automatically (used by the setup skill).
func RunRemember(token, nick, alias string) error {
	if _, _, err := proto.ParseToken(token); err != nil {
		return err
	}
	nick = strings.TrimPrefix(nick, "@")
	if !proto.ValidName(nick) {
		return fmt.Errorf("bad nick %q: want [a-z0-9-]{1,32}", nick)
	}
	return local.RememberChat(local.ProjectDir(), local.Entry{Token: strings.TrimSpace(token), Name: nick, Alias: alias})
}

// RunWait is the asyncRewake hook: blocks until this session's next chat events,
// prints them to stderr and returns 2 (wake Claude). Returns 0 when there is
// nothing to deliver (another waiter is active, no MCP server, max wait reached).
func RunWait() int {
	go func() { _, _ = io.Copy(io.Discard, os.Stdin) }() // hook input is not needed
	key, err := proc.SessionKey()
	if err != nil {
		return 0
	}
	maxWait := 23 * time.Hour // below the hook timeout, so the hook ends cleanly
	if d, err := time.ParseDuration(os.Getenv("CCQC_WAIT_MAX")); err == nil && d > 0 {
		maxWait = d
	}
	deadline := time.Now().Add(time.Minute) // MCP servers start after SessionStart hooks
	for {
		c, err := ipc.DialSession(key, 2*time.Second)
		if err == nil {
			defer c.Close()
			if _, err := c.Write([]byte("wait\n")); err != nil {
				return 0
			}
			_ = c.SetReadDeadline(time.Now().Add(maxWait))
			data, _ := io.ReadAll(c)
			text := string(data)
			if strings.TrimSpace(text) == "" || text == "busy\n" {
				return 0
			}
			_, _ = os.Stderr.WriteString(text)
			return 2
		}
		if time.Now().After(deadline) {
			return 0
		}
		time.Sleep(time.Second)
	}
}
