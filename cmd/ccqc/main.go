// ccqc: token-gated chat between Claude Code sessions over local IPC.
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/PHPCraftdream/cc-quant-chat/internal/broker"
	"github.com/PHPCraftdream/cc-quant-chat/internal/mcp"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proc"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

const usage = `ccqc — chat between Claude Code sessions over local IPC

usage:
  ccqc mcp                MCP stdio server (started by Claude Code)
  ccqc tail               print this Claude Code session's chat events (monitor)
  ccqc wait               asyncRewake hook: wait for the session's next events, exit 2 to wake Claude
  ccqc hello              SessionStart hook: tell Claude which chats this folder joins automatically
  ccqc remember <token> <nick> [alias]
                          make the current folder join a chat automatically
  ccqc broker             run the broker (normally auto-started)
  ccqc token new <chat>   print a new chat token: <chat>.<secret>
  ccqc session            show the parent process chain and session key (monitor troubleshooting)
  ccqc version`

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "mcp":
		err = mcp.Run()
	case "broker":
		err = broker.Run()
	case "tail":
		err = mcp.RunTail()
	case "wait":
		os.Exit(mcp.RunWait())
	case "hello":
		mcp.RunHello()
	case "remember":
		if len(os.Args) < 4 || len(os.Args) > 5 {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
		alias := ""
		if len(os.Args) == 5 {
			alias = os.Args[4]
		}
		if err = mcp.RunRemember(os.Args[2], os.Args[3], alias); err == nil {
			fmt.Println("this folder now joins the chat automatically")
		}
	case "session":
		err = printSession()
	case "token":
		if len(os.Args) != 4 || os.Args[2] != "new" {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
		var tok string
		if tok, err = proto.NewToken(os.Args[3]); err == nil {
			fmt.Println(tok)
		}
	case "version":
		fmt.Println("ccqc", mcp.Version)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ccqc:", err)
		os.Exit(1)
	}
}

func printSession() error {
	anc, err := proc.Ancestors()
	if err != nil {
		return err
	}
	for _, p := range anc {
		fmt.Printf("%7d  %s\n", p.PID, p.Name)
	}
	key, err := proc.SessionKey()
	if err != nil {
		return err
	}
	fmt.Println("session key:", key)
	return nil
}
