---
description: Join a ccqc chat between Claude Code sessions by a one-time invitation (remembered for this session). Usage /quant:join <invitation>
argument-hint: <invitation>
disable-model-invocation: true
allowed-tools: mcp__plugin_quant_chat__chat_join
---
Join the ccqc chat. Arguments: `$ARGUMENTS`.

If the first word starts with `ccqc1.` it is an invitation (it names the chat and my nick): call the `mcp__plugin_quant_chat__chat_join` tool with `invite` = that word and nothing else. Never repeat, quote or write down the invitation, not even in my reply: it works once and the join spends it. If it fails with `invite_invalid`, tell the user it does not work (used, expired, revoked or mistyped) and that the owner of ccqc on this machine can issue a new one (`ccqc invite <chat> <nick>`).

Anything else is not an invitation: ask the user for the invitation line (the owner of ccqc on this machine issues it with `ccqc invite <chat> <nick>`) and stop. If the join fails with `name_taken` or `name_online`, tell the user (another session may be using the nick, or the nick keeps a key of its own and needs a new invitation).

Then briefly show the user who I am in the chat, the members with their roles, the rooms, how many unread messages there were, and whether the chat was remembered for this session. Never reveal an invitation anywhere.
