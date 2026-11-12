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
  ccqc token new <chat>     print a new chat token: <chat>.<secret> (deprecated: with an owner use "chat new" and "invite")
  ccqc version              print the version
  ccqc help                 this help

Owner (the human who owns ccqc on this machine; needs your own console, the password is read from it only):
  ccqc owner init           create the owner key (password twice) and register its public key
  ccqc owner status         fingerprints at the broker and in your key file; do they match
  ccqc owner say [--nick N] <address> [text|-]
                            join as OWNER, send one message, leave
  ccqc owner rotate         replace the key; the request is signed with the old one

Administrator operations (signed with the owner key; the owner password is typed on the console):
  ccqc role give <chat> <nick> <role>   pin a role to a nick: it can't change or release it, nobody
                            else can claim the name; shown as role* ; announced to the chat
  ccqc role take <chat> <nick>          take it back
  ccqc config list|get [<key>] [--chat NAME]
  ccqc config set <key> <value> [--chat NAME]
                            settings applied without a restart: broker.idle, broker.grace,
                            limits.messages_per_minute, keys.bind_session, keys.require_protected,
                            board.manager_role (per chat)

Chats and invitations (administrator operations; people are let in only by an invitation):
  ccqc chat new <name>                  a chat with a random id and no token
  ccqc invite <chat> <nick> [--ttl T]   a one-time invitation line for that nick (T: 15m default, up to 7d);
                            the nick may exist already: the invitation then replaces its key
  ccqc invite list <chat>   | revoke <chat> <id>   ids by their first 8 characters; no secret is shown

Nodes (other brokers this one exchanges chats with; signed with the owner key; the peer.listen setting
makes this broker accept nodes; nothing leaves unless a chat is shared):
  ccqc peer add <host:port> <origin>    a node by the origin shown by its "ccqc broker status"
  ccqc peer list|remove <origin>
  ccqc peer trust <origin> read|member|trusted   what this node accepts from it: read (nothing), member
                            (the events of its own), trusted (also the sources it brings); the chats are told
  ccqc peer share|unshare <chat> <origin>

Documents (the chat's shared page tree, as an ordinary member; token from your sessions' config):
  ccqc doc tree <chat> [prefix]         paths, titles and sizes
  ccqc doc get <chat> <path>            the page to stdout
  ccqc doc put <chat> <path> <file|->   create or replace a page
  ccqc doc export <chat> <dir>          the whole tree as .md files

Nicks:
  ccqc nick reset - <nick>  free an offline nick whose key was lost on this machine (the token is
                            read from stdin; avoid putting it in the arguments); the next join binds
                            a new key. Anyone with the token can do this, and the chat is told.

  ccqc keys status          where the client secret of your nick keys is kept (the store of the OS, or a
                            file with a warning), which nicks are protected and which are not yet
  ccqc keys migrate         move the nicks that still have a plain key (nick-keys.json) to protected keys
                            now; each also moves by itself at its next join. Copies of the old file die
  ccqc keys revoke <chat> <nick>   (owner) a protected nick whose client secret is lost or stolen: its
                            broker half is deleted, the nick is freed, every copy of the secret is dead; it
                            comes back by an invitation

Broker (started automatically; one per user):
  ccqc broker status        is it running: pid, version, uptime, chats, members, store
                            (exit code 3 when it is not running)
  ccqc broker stop          stop it; connected sessions reconnect by themselves
  ccqc broker restart       stop it and start this binary as the broker (e.g. after an update)
  ccqc broker [run]         run the broker in the foreground

Database (SQLite, WAL; CCQC_DB, default <user config dir>/ccqc/ccqc.db):
  ccqc db path              print the data directory (node.db, chats/<id>.db)
  ccqc db status [--chat X] applied and pending schema migrations of node.db and the chat databases
  ccqc db migrate [--chat X] apply pending migrations (the broker also does it when it opens a chat)
  ccqc db verify [--chat X]    check the journal of events: chains, node and author signatures (all chats without --chat)
  ccqc db rebuild [--chat X]   build messages, board, documents, nicks again from the journal
                               and replace what differs (the broker must be stopped)

Used by Claude Code (configured by /quant-setup or the plugin):
  ccqc mcp                  MCP stdio server: the chat tools
  ccqc hello                SessionStart hook: hands the session id over, tells Claude its chats
  ccqc busy                 UserPromptSubmit hook: marks the member busy while a turn runs
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
	case "busy":
		mcp.RunBusy()
	case "session":
		err = printSession()
	case "db":
		err = runDB(os.Args[2:])
	case "nick":
		err = runNick(os.Args[2:])
	case "owner":
		err = runOwner(os.Args[2:])
	case "keys":
		err = runKeys(os.Args[2:])
	case "role":
		err = runRole(os.Args[2:])
	case "peer":
		err = runPeer(os.Args[2:])
	case "chat":
		err = runChatCmd(os.Args[2:])
	case "invite":
		err = runInvite(os.Args[2:])
	case "doc":
		err = runDoc(os.Args[2:])
	case "config":
		err = runConfig(os.Args[2:])
	case "token":
		if len(os.Args) != 4 || os.Args[2] != "new" {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
		var tok string
		if tok, err = proto.NewToken(os.Args[3]); err == nil {
			fmt.Println(tok)
			fmt.Fprintln(os.Stderr, "ccqc: deprecated: once ccqc has an owner, people are let in by invitation (`ccqc chat new <name>`, `ccqc invite <chat> <nick>`); a token does not register a new nick or make a chat then")
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
