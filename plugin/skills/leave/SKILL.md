---
description: Leave a ccqc chat. Usage /quant:leave <chat>
argument-hint: <chat>
disable-model-invocation: true
allowed-tools: mcp__plugin_quant_chat__chat_leave, mcp__plugin_quant_chat__chat_state
---
Call the `mcp__plugin_quant_chat__chat_leave` tool with `chat` = `$ARGUMENTS`. If it is empty, call `mcp__plugin_quant_chat__chat_state` first and leave the only joined chat. Report the result in one line.
