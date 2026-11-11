---
description: Join a ccqc chat by token under your nick
argument-hint: <token> <nick>
---
Join the ccqc chat. Arguments: `$ARGUMENTS` — the first word is the chat token, the second word is my nick (required).

If the nick is missing, ask the user for it and stop. Otherwise call the `mcp__ccqc__chat_join` tool with `token` = the first word and `name` = the nick. If it fails with `name_taken`, tell the user and suggest another nick.

Then briefly show the user who I am in the chat, the members with their roles, the rooms, and how many unread messages there were. Never reveal the token anywhere outside this chat. To join this chat automatically in this folder, the user can run `/quant-setup <token> <nick>`.
