#!/usr/bin/env node
// Sets up the current folder for ccqc chats: MCP server, rewake/hello hooks, permissions,
// and auto-join of <token> as <nick>. Idempotent. No shell: external calls use execFileSync.
// Usage: node setup.mjs <token> <nick> [alias]
import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, renameSync, writeFileSync } from "node:fs";
import { dirname, isAbsolute, join, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

const [token, nickArg, alias] = process.argv.slice(2);
if (!token || !nickArg) {
  fail("usage: setup.mjs <token> <nick> [alias]");
}
const nick = nickArg.replace(/^@/, "");

const folder = resolve(process.env.CLAUDE_PROJECT_DIR || process.cwd());
const repo = resolve(dirname(fileURLToPath(import.meta.url)), "..", "..", "..");
const exe = join(repo, "plugin", "bin", process.platform === "win32" ? "ccqc.exe" : "ccqc");
if (!existsSync(exe)) {
  fail(`ccqc binary not found at ${exe}; build it first: go build -o plugin/bin/ ./cmd/ccqc`);
}

// Paths without ".exe": Claude Code resolves the extension itself on Windows.
const bare = exe.replace(/\.exe$/i, "");
const relPath = relative(folder, bare); // absolute when on another drive (Windows)
const inside = relPath !== "" && !relPath.startsWith("..") && !isAbsolute(relPath);
const rel = relPath.split(sep).join("/");
const mcpCommand = inside ? `./${rel}` : bare.split(sep).join("/");
const hookCommand = inside ? `\${CLAUDE_PROJECT_DIR}/${rel}` : bare.split(sep).join("/");

// 1. MCP server; CCQC_DELIVERY=hook: events wait for the rewake hook from the start.
const mcpPath = join(folder, ".mcp.json");
const mcp = readJSON(mcpPath);
mcp.mcpServers ??= {};
mcp.mcpServers.ccqc = { command: mcpCommand, args: ["mcp"], env: { CCQC_DELIVERY: "hook" } };
writeJSON(mcpPath, mcp);

// 2. Hooks and permissions in local (personal, not shared) settings.
const settingsPath = join(folder, ".claude", "settings.local.json");
const st = readJSON(settingsPath);
st.permissions ??= {};
st.permissions.allow = addOnce(st.permissions.allow, "mcp__ccqc");
st.enabledMcpjsonServers = addOnce(st.enabledMcpjsonServers, "ccqc");
st.hooks ??= {};
const wait = { type: "command", command: hookCommand, args: ["wait"], async: true, asyncRewake: true, timeout: 86400 };
const hello = { type: "command", command: hookCommand, args: ["hello"], timeout: 10 };
setHooks(st.hooks, "SessionStart", [hello, wait]);
setHooks(st.hooks, "Stop", [wait]);
writeJSON(settingsPath, st);

// 3. Auto-join: the token and nick are kept in the user's ccqc config, not in this folder.
const args = ["remember", token, nick];
if (alias) args.push(alias);
try {
  execFileSync(exe, args, { env: { ...process.env, CLAUDE_PROJECT_DIR: folder }, stdio: ["ignore", "ignore", "pipe"] });
} catch (e) {
  fail(`ccqc remember failed: ${String(e.stderr || e.message).trim()}`);
}

const chat = alias || token.slice(0, token.lastIndexOf("."));
console.log(`ccqc: folder ${folder} set up.
- joins /${chat} as @${nick} automatically on every Claude Code session start here
- incoming messages wake the session (rewake hook), no extra flags needed
- written: .mcp.json (server "ccqc"), .claude/settings.local.json (hooks, permission mcp__ccqc)
Restart Claude Code in this folder to activate.`);

// --- helpers ---

function fail(msg) {
  console.error(msg);
  process.exit(1);
}

function readJSON(p) {
  if (!existsSync(p)) return {};
  try {
    return JSON.parse(readFileSync(p, "utf8"));
  } catch (e) {
    fail(`${p}: not valid JSON (${e.message}); fix it and rerun`);
  }
}

function writeJSON(p, v) {
  mkdirSync(dirname(p), { recursive: true });
  const tmp = p + ".tmp";
  writeFileSync(tmp, JSON.stringify(v, null, 2) + "\n");
  renameSync(tmp, p);
}

function addOnce(list, item) {
  const out = Array.isArray(list) ? list : [];
  if (!out.includes(item)) out.push(item);
  return out;
}

// setHooks replaces earlier ccqc hook handlers of an event and adds the given ones.
function setHooks(hooks, event, handlers) {
  const isOurs = (h) => typeof h?.command === "string" && /(^|[\\/])ccqc(\.exe)?$/i.test(h.command);
  const groups = (hooks[event] ?? [])
    .map((g) => ({ ...g, hooks: (g.hooks ?? []).filter((h) => !isOurs(h)) }))
    .filter((g) => g.hooks.length > 0);
  groups.push({ hooks: handlers });
  hooks[event] = groups;
}
