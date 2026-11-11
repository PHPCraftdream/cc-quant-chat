// ccqc: token-gated chat between Claude Code sessions over local IPC.
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/PHPCraftdream/cc-quant-chat/internal/mcp"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proc"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/version"
)

const usage = `ccqc — chat between Claude Code sessions over local IPC

Setup:
  ccqc token new <chat>     print a new chat token: <chat>.<secret> (share it with the members)
  ccqc version              print the version
  ccqc help                 this help

Nicks:
  ccqc nick reset - <nick>  free an offline nick whose key was lost on this machine (the token is
                            read from stdin; avoid putting it in the arguments); the next join binds
                            a new key. Anyone with the token can do this, and the chat is told.

Broker (started automatically; one per user):
  ccqc broker status        is it running: pid, version, uptime, chats, members, store
                            (exit code 3 when it is not running)
  ccqc broker stop          stop it; connected sessions reconnect by themselves
  ccqc broker restart       stop it and start this binary as the broker (e.g. after an update)
  ccqc broker [run]         run the broker in the foreground

Database (SQLite, WAL; CCQC_DB, default <user config dir>/ccqc/ccqc.db):
  ccqc db path              print the database location
  ccqc db status            applied and pending schema migrations
  ccqc db migrate           apply pending migrations (the broker also does it on start)

Used by Claude Code (configured by /quant-setup or the plugin):
  ccqc mcp                  MCP stdio server: the chat tools
  ccqc hello                SessionStart hook: hands the session id over, tells Claude its chats
  ccqc wait                 asyncRewake hook: waits for the session's next chat events
  ccqc session              troubleshooting: process chain and session key of the hooks

Docs: https://github.com/PHPCraftdream/cc-quant-chat`

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
		err = runBroker(os.Args[2:])
	case "help", "-h", "--help":
		fmt.Println(usage)
	case "wait":
		os.Exit(mcp.RunWait())
	case "hello":
		mcp.RunHello()
	case "session":
		err = printSession()
	case "db":
		err = runDB(os.Args[2:])
	case "nick":
		err = runNick(os.Args[2:])
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
		fmt.Println("ccqc", version.Version)
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
