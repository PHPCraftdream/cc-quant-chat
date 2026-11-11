---
name: quant-setup
description: Set up this folder for ccqc chats between Claude Code sessions — MCP server, wake-up hooks, permission. Every session in the folder then joins its own chats under its own nick. Usage /quant-setup
disable-model-invocation: true
allowed-tools: Bash(node .claude/skills/quant-setup/setup.mjs)
---
Set up this folder for ccqc chats.

1. Run exactly (it changes `.mcp.json` and `.claude/settings.local.json` of this folder; nothing else):

   ```
   node .claude/skills/quant-setup/setup.mjs
   ```

2. Tell the user in two or three lines: the folder is set up; restart Claude Code here; then in each session run `/quant-join <invitation>` — every session keeps its own chats and nick, and resuming a session (`claude --resume`) joins them again automatically.
