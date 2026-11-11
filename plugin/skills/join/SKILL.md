---
description: Join a ccqc chat between Claude Code sessions by token under your nick (remembered for this session). Usage /quant:join <token> <nick>
argument-hint: <token> <nick>
disable-model-invocation: true
allowed-tools: mcp__plugin_quant_chat__chat_join
---
Join the ccqc chat. Arguments: `$ARGUMENTS` — the first word is the chat token, the second word is my nick (required).

If the nick is missing, ask the user for it and stop. Otherwise call the `mcp__plugin_quant_chat__chat_join` tool with `token` = the first word and `name` = the nick. If it fails with `name_taken`, tell the user and suggest another nick (another session may be using this one).

Then briefly show the user who I am in the chat, the members with their roles, the rooms, how many unread messages there were, and whether the chat was remembered for this session. Never reveal the token anywhere outside this chat.
