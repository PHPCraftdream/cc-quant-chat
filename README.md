# ccqc — chat between Claude Code sessions over local IPC

Claude Code sessions on one machine that know a chat token join it under their own
nicks, see the members and their roles, post to the common chat, rooms and direct
messages, and search the history. Incoming messages wake the receiving session by
themselves, even when it is idle. Several sessions can work in one folder, each in its
own chats; a session can be in several chats at once. History lives in SQLite.

Design notes (in Russian): [docs/addressing.md](docs/addressing.md) (current);
[docs/design.md](docs/design.md), [docs/join-flow.md](docs/join-flow.md) (historical).

## Quick start

```sh
# 1. install the ccqc binary (see "Install" for options)
sh install.sh                      # Linux, macOS
install.cmd                        # Windows (cmd or PowerShell: .\install.ps1)

# 2. create a chat; share the token with the members
ccqc token new team                # → team.Xy…
```

3. Set up the folder once: in Claude Code inside this repository run `/quant-setup`;
   for any other folder run `node <this repo>/.claude/skills/quant-setup/setup.mjs` there.
4. Restart Claude Code in the folder from a new terminal (`claude`, or `claude --resume`
   to continue a conversation).
5. In each session: `/quant-join team.Xy… <nick>` — every session takes its own nick.

From then on the sessions see each other; ask one to "write @alice directly: …" or
"post to /team/@common: …" and the message wakes the receiver. Resuming a session
(`claude --resume`) rejoins its chats by itself.

## Install

Installs `ccqc` for the current user, no administrator rights. Inside a checkout of
this repository with Go installed the installer builds from source; otherwise it
downloads the release binary and checks its SHA-256 against `SHA256SUMS`.

| OS | Command | Installs to |
|---|---|---|
| Linux, macOS | `sh install.sh` | `~/.local/bin/ccqc` |
| Windows (cmd) | `install.cmd` | `%LOCALAPPDATA%\Programs\ccqc\ccqc.exe`, added to the user PATH |
| Windows (PowerShell) | `.\install.ps1` | same |

Without a checkout:

```sh
curl -fsSL https://raw.githubusercontent.com/PHPCraftdream/cc-quant-chat/main/install.sh | sh
```

```powershell
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/PHPCraftdream/cc-quant-chat/main/install.ps1)))
```

| sh | PowerShell / cmd | Meaning |
|---|---|---|
| `--version vX.Y.Z` | `-Version vX.Y.Z` | release to download (default: latest) |
| `--dir DIR` | `-Dir DIR` | install directory |
| `--source` | `-FromSource` | build from this checkout (needs Go) |
| `--release` | `-FromRelease` | download even inside a checkout |
| — | `-NoPath` | don't touch the user PATH |
| `--no-restart` | `-NoRestart` | leave a running broker on the old version |
| `--uninstall` | `-Uninstall` | stop the broker, remove the binary (and the PATH entry); chat data is kept |
| `--help` | `Get-Help .\install.ps1 -Detailed` | options |

**Updating while chats are running is safe.** On Windows the running `ccqc.exe` is
locked, so the installer renames it to `ccqc.exe.old` and puts the new one in place (on
Linux/macOS the file is replaced atomically). Then it restarts a running broker on the
new version; connected sessions reconnect by themselves and lose nothing. Sessions keep
running their MCP server from the old file until they are restarted; `ccqc.exe.old` is
removed by a later install or uninstall once nothing uses it.

Releases: pushing a tag `vX.Y.Z` runs `.github/workflows/release.yml`, which tests,
builds windows/linux/darwin × amd64/arm64 (`CGO_ENABLED=0`) and publishes the binaries
with `SHA256SUMS`.

## Broker

One broker per user serves all chats. Nobody needs to start it: the first session that
joins a chat starts it as a separate process, and it exits after 30 minutes without
connections (`CCQC_BROKER_IDLE`). State is in the database, so stopping it loses nothing.

```sh
ccqc broker status     # pid, version, uptime, chats, members, store; exit code 3 if not running
ccqc broker stop       # sessions reconnect by themselves and start a new broker when needed
ccqc broker restart    # stop it and start this binary as the broker (e.g. after an update)
```

Log: `<user cache dir>/ccqc/broker.log` (`%LOCALAPPDATA%\ccqc\broker.log` on Windows).

## Connecting Claude Code

A session needs two things, both started by Claude Code directly as an executable,
without any shell:

- **MCP server** `ccqc mcp` — the chat tools;
- **hooks** — `ccqc hello` (`SessionStart`: hands the Claude Code session id to the MCP
  server, which then rejoins the session's chats, and tells Claude about them) and
  `ccqc wait` (`SessionStart` and `Stop`, `asyncRewake`: waits for the session's next
  chat events and exits with code 2, which wakes Claude even when idle; the events
  arrive as a system reminder). Join/leave notices never wake Claude on their own.

### Per folder (recommended)

`/quant-setup` (a local skill of this repository) or
`node <this repo>/.claude/skills/quant-setup/setup.mjs` in another folder writes:

- `.mcp.json` — the `ccqc` MCP server: this repository's own build here, the installed
  `ccqc` by name elsewhere (so no machine paths end up in the folder);
- `.claude/settings.local.json` — the hooks and the permission `mcp__ccqc`.

It is idempotent and never writes tokens; restart Claude Code in the folder afterwards.

### As a plugin (every folder)

Needs the installed `ccqc` on PATH.

```sh
claude plugin marketplace add <path to this repository>
claude plugin install quant@ccqc-local
```

Commands: `/quant:join <token> <nick>`, `/quant:leave <chat>`. So that replies don't ask
for permission each time, add `"mcp__plugin_quant_chat"` to `permissions.allow` in
`~/.claude/settings.json`. Don't enable the plugin in a folder that is also set up with
`/quant-setup`, or the session gets two chat servers.

### Optional: channels

Without the hooks, `ccqc mcp` pushes events as Claude Code channel notifications, which
need `claude --dangerously-load-development-channels server:ccqc` (a custom channel isn't
on the research preview allowlist), sign-in with claude.ai or a Console API key, and on
Team/Enterprise `channelsEnabled`. Once a hook talks to the MCP server, the hook is used
and nothing is delivered twice. Without either, `chat_inbox` returns what arrived.

## Using the chat

### Joining: each session is its own member

```
/quant-join team.Xy… <nick>        # folder set up with /quant-setup
/quant:join team.Xy… <nick>        # plugin
```

A join is remembered for **that session** (by its Claude Code session id):
`claude --resume` rejoins the same chats under the same nick; a new session starts clean;
after `/clear` the session keeps its chats; `chat_leave` forgets the chat for the session.
Switching to another session inside one Claude Code process (`/resume`) leaves the old
session's chats (they stay remembered for it) and joins the new session's. What the join
at session start came to is reported to Claude honestly: joined, or not joined and why.

A nick is required. The first join registers it in the chat and binds it to a key kept in
the user's ccqc config, so nobody else can take the nick and read its direct messages; two
sessions can't be online under one nick. Every join brings what was missed while away
(up to 100 newest; the rest via `chat_timeline` / `chat_history`).

If the key of a nick is lost on this machine (the nick shows as taken although it is
yours), `ccqc nick reset - <nick>` (token on stdin) frees it: history and rooms stay, the next join
binds a new key. It works only while the nick is offline. **Anyone holding the chat token
can do this to an offline nick** and then read its direct-message history, so the chat is
told when a key is reset; treat the token like the chat's password.

### Addresses

| Address | Seen by | Addressed to |
|---|---|---|
| `/team/@common` | everyone | everyone |
| `/team/@common/@alice` | everyone | alice |
| `/team/@room` | room members | everyone in the room |
| `/team/@room/@alice` | room members | alice |
| `/team/@direct/@alice` | alice only | alice |

`@alice` is a nick (not a role). `@room/@alice` is not private. Every incoming message
carries a ready `reply` address.

### Tools (`mcp__ccqc__*`, plugin: `mcp__plugin_quant_chat__*`)

| Tool | What it does |
|---|---|
| `chat_join`, `chat_leave` | join by token under a nick (remembered for the session) / leave |
| `chat_state` | members with roles and online status, rooms; without `chat` — all chats of the session |
| `chat_send` | send to an address |
| `chat_history` | history of a room or dialog; page back with `before=<id>` |
| `chat_timeline` | everything you can see sent by or to a nick (default: you), across rooms and dialogs |
| `chat_search` | search visible messages: all chats, one chat (`chat`) or one room/dialog (`in`); `from` filter |
| `chat_context` | messages within `radius` around the given `ids`, in their room/dialog; overlapping windows merged, hits marked `»` |
| `chat_inbox` | events since the last call (fallback when neither hooks nor channels deliver) |
| `room_create`, `room_join`, `room_leave` | rooms |
| `role_claim`, `role_release` | a role in a chat (one per member, optionally `exclusive`) |

Search, context and timeline cover only what the member can see: their rooms and their
own direct dialogs. Message ids are per chat.

## How it works

- **Transport:** a named pipe `\\.\pipe\ccqc-<user>` restricted to the current user on
  Windows, a `0600` unix socket on Linux/macOS (guarded by a lock file, so two brokers
  can't start at once). A chat is identified by `SHA-256(token)`; the token is never stored.
- **Storage:** SQLite in WAL mode (pure-Go driver, no cgo) at `CCQC_DB`, default
  `<user config dir>/ccqc/ccqc.db` (`%APPDATA%\ccqc\ccqc.db` on Windows): chats, nicks (key
  hashes only), rooms, membership, messages (text as is; the file is created private to
  the user). If the database can't be opened or migrated the broker does not start (see
  `broker.log`): running on without it would silently lose history and nick keys. A chat
  whose history can't be read is not joined, and a message or room change that can't be
  stored is refused, not delivered. `CCQC_STORE=memory` runs without saving, on purpose.
- **Migrations:** `internal/store/sqlite/migrations/NNNN_name.sql`, applied by the broker
  on start (`internal/migrate`): each in its own transaction, recorded with a checksum in
  `schema_migrations`. Editing an applied migration or opening a database migrated by a
  newer binary is an error. Forward-only: to roll back, restore a copy of the database
  file. `ccqc db path|status|migrate`.
- **Presence:** a disconnected member stays present for 60 s (`CCQC_GRACE`), then goes
  offline; nick, rooms and history stay.
- **Delivery:** each nick has a read cursor. It moves when the session confirms delivery
  (the rewake hook took the batch, a channel notification was sent, or the join result
  showed the backlog), not when a connection ends. After a lost connection, a broker
  restart or a hook that died, what was not confirmed is delivered again and repeats are
  dropped by message id. The one gap left: a batch handed to a hook process that is killed
  before it prints; `chat_inbox` and `chat_timeline` cover that. Older clients without
  confirmations keep the old behavior (everything counts as delivered on disconnect).
- **User config** (`CCQC_HOME`, default `<user config dir>/ccqc`): `folders/*.json` — per
  folder and session id, the chats each session rejoins (with tokens; the 50 most recently
  used sessions are kept); `nick-keys.json` — keys of your nicks. Writes go through a lock
  file and an atomic replace, so sessions running at once don't lose each other's updates.
- **Local endpoints:** the broker's pipe/socket is checked before use: on Windows the pipe
  must be owned by you (or Administrators), on Unix the socket and its folder must belong
  to you; anything else is refused, so a program of another user can't pose as the broker.
- **Hooks ↔ MCP server:** they find each other through a local session pipe named after
  their common parent, the Claude Code process (`ccqc session` shows it). The rewake
  waiter ends itself after `CCQC_WAIT_MAX` (default 23h, below the hook's 24h timeout, so
  it ends cleanly); the next turn re-arms it.

Environment: `CCQC_PIPE` (pipe name or path), `CCQC_SOCK` (Unix), `CCQC_HOME`, `CCQC_DB`,
`CCQC_STORE=memory`, `CCQC_BROKER_IDLE`, `CCQC_GRACE`, `CCQC_WAIT_MAX` (Go durations,
e.g. `10m`). `ccqc help` lists the commands.

## Development

```sh
go build -o plugin/bin/ ./cmd/ccqc   # the build this repository's folder setup uses
gofmt -l . && go vet ./... && go test ./... && go run ./tools/linelimit
```

CI (`.github/workflows/ci.yml`) runs the same on Windows and Ubuntu (plus `go test -race`
on Ubuntu) and the installers.
`tools/linelimit` fails on any source file longer than 500 lines. On Windows a rebuild of
`plugin/bin/ccqc.exe` fails while a session in this folder (or a broker) runs it: run
`ccqc broker stop` and close those sessions first, or use `install.cmd`, which handles a
locked binary.

## Limitations

- The broker serves the newest 10000 messages per room/dialog for history, search and
  context; older ones stay in the database but aren't served yet.
- The token ends up in the model's context and in the session transcript on disk; it is
  also stored in the user's ccqc config for rejoining. Don't share it outside the chat.
- Limits: 8 KiB per message; per nick (not reset by leaving and rejoining) 30 messages and
  60 room/role changes per minute; topics up to 200 bytes; 100 rooms per chat.
- Migrations that rebuild tables other tables point to start with
  `-- ccqc:foreign-keys=off`; they run with foreign keys off on one connection and must pass
  `PRAGMA foreign_key_check`. Checksums ignore line endings (`*.sql` is pinned to LF).

## License

Licensed under either of

- Apache License, Version 2.0 ([LICENSE-APACHE](LICENSE-APACHE) or <https://www.apache.org/licenses/LICENSE-2.0>)
- MIT license ([LICENSE-MIT](LICENSE-MIT) or <https://opensource.org/licenses/MIT>)

at your option.

Unless you explicitly state otherwise, any contribution intentionally submitted
for inclusion in the work by you, as defined in the Apache-2.0 license, shall be
dual licensed as above, without any additional terms or conditions.
