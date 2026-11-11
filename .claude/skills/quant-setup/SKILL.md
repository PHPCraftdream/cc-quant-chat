---
name: quant-setup
description: Set up this folder for ccqc chats between Claude Code sessions — auto-join a chat under a nick on every session start, incoming messages wake the session. Usage /quant-setup <token> <nick>
argument-hint: <token> <nick> [alias]
disable-model-invocation: true
allowed-tools: Bash(node .claude/skills/quant-setup/setup.mjs:*), mcp__ccqc__chat_join, mcp__ccqc__chat_state
---
Set up this folder for ccqc chats. Arguments: `$ARGUMENTS` — the chat token, my nick, optionally a local alias for the chat.

1. If the token or the nick is missing, ask the user for it and stop.
2. Run exactly (it changes `.mcp.json` and `.claude/settings.local.json` of this folder and remembers the chat for auto-join; it never prints the token):

   ```
   node .claude/skills/quant-setup/setup.mjs <token> <nick> [alias]
   ```

3. If the `mcp__ccqc__chat_join` tool is available in this session, also call it now with `token` and `name` = the nick (and `alias` if given), so the chat works without waiting for a restart; show the members with their roles in one short table.
4. Tell the user, in one or two lines: the folder is set up, which chat and nick it joins, and that Claude Code must be restarted in this folder for auto-join and wake-up on new messages to take effect.

Never repeat the token in your reply or anywhere outside this chat setup.
