package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/ipc"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proc"
)

// hookInput: the fields of Claude Code's hook JSON that ccqc uses.
type hookInput struct {
	SessionID string `json:"session_id"`
}

// readHookInput reads the hook JSON from stdin; empty on timeout or bad input.
func readHookInput() hookInput {
	ch := make(chan hookInput, 1)
	go func() {
		var in hookInput
		_ = json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&in)
		_, _ = io.Copy(io.Discard, os.Stdin)
		ch <- in
	}()
	select {
	case in := <-ch:
		return in
	case <-time.After(2 * time.Second):
		return hookInput{}
	}
}

// dialSession connects to this session's MCP server, retrying until deadline
// (MCP servers start in parallel with SessionStart hooks).
func dialSession(within time.Duration) (net.Conn, error) {
	key, err := proc.SessionKey()
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(within)
	for {
		c, err := ipc.DialSession(key, 2*time.Second)
		if err == nil || time.Now().After(deadline) {
			return c, err
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// RunHello is a SessionStart hook: hands the session id to the MCP server, which joins
// the session's remembered chats, and tells Claude about them.
func RunHello() {
	in := readHookInput()
	if in.SessionID == "" {
		return
	}
	c, err := dialSession(8 * time.Second)
	if err != nil {
		return
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprintf(c, "hello %s\n", in.SessionID); err != nil {
		return
	}
	var chats []string
	sc := bufio.NewScanner(c)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			chats = append(chats, line)
		}
	}
	if len(chats) == 0 {
		return
	}
	fmt.Printf("ccqc chat: this session is in %s (joined automatically). Incoming chat messages wake you by "+
		"themselves (as \"ccqc: N new chat event(s)\"). Send with chat_send to /<chat>/@common[/@nick], "+
		"/<chat>/@<room>[/@nick] or /<chat>/@direct/@nick; catch up with chat_timeline, chat_history, chat_search.\n",
		strings.Join(chats, ", "))
}

// RunWait is the asyncRewake hook: blocks until this session's next chat events,
// prints them to stderr and returns 2 (wake Claude). Returns 0 when there is
// nothing to deliver (another waiter is active, no MCP server, max wait reached).
func RunWait() int {
	in := readHookInput()
	maxWait := 23 * time.Hour // below the hook timeout, so the hook ends cleanly
	if d, err := time.ParseDuration(os.Getenv("CCQC_WAIT_MAX")); err == nil && d > 0 {
		maxWait = d
	}
	c, err := dialSession(time.Minute)
	if err != nil {
		return 0
	}
	defer c.Close()
	if _, err := fmt.Fprintf(c, "wait %s\n", in.SessionID); err != nil {
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
