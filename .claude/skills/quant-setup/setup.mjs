#!/usr/bin/env node
// Sets up the current folder for ccqc chats: MCP server, rewake/hello hooks, permission.
// Chats and nicks are per Claude Code session (chat_join remembers them), not per folder,
// so several sessions in one folder can each be someone else. Idempotent.
// Usage: node setup.mjs
import { existsSync, mkdirSync, readFileSync, renameSync, writeFileSync } from "node:fs";
import { dirname, isAbsolute, join, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

const isWin = process.platform === "win32";
const folder = resolve(process.env.CLAUDE_PROJECT_DIR || process.cwd());
const repo = resolve(dirname(fileURLToPath(import.meta.url)), "..", "..", "..");
const repoExe = join(repo, "plugin", "bin", isWin ? "ccqc.exe" : "ccqc");

// Which ccqc the folder runs (paths without ".exe": Claude Code adds it on Windows):
// 1. the repository's own build, when setting up the repository itself (relative paths);
// 2. the globally installed ccqc (install.sh / install.cmd), by name — no machine paths
//    end up in the folder's files;
// 3. otherwise the repository build by absolute path.
const toSlash = (p) => p.split(sep).join("/");
const repoBare = repoExe.replace(/\.exe$/i, "");
const relPath = relative(folder, repoBare); // absolute when on another drive (Windows)
const inside = relPath !== "" && !relPath.startsWith("..") && !isAbsolute(relPath);
let mcpCommand, hookCommand, using;
if (inside && existsSync(repoExe)) {
  mcpCommand = `./${toSlash(relPath)}`;
  hookCommand = `\${CLAUDE_PROJECT_DIR}/${toSlash(relPath)}`;
  using = `this repository's build (${mcpCommand})`;
} else if (globalCcqc().path && globalCcqc().onPath) {
  mcpCommand = hookCommand = "ccqc";
  using = `the installed ccqc (${globalCcqc().path})`;
} else if (globalCcqc().path) {
  // Installed but its folder is not on PATH: a bare name would not start, so the path is written.
  mcpCommand = hookCommand = toSlash(globalCcqc().path.replace(/\.exe$/i, ""));
  using = `${mcpCommand} (the installed ccqc is not on PATH; add its folder to PATH to keep machine paths out of .mcp.json)`;
} else if (existsSync(repoExe)) {
  mcpCommand = hookCommand = toSlash(repoBare);
  using = `${mcpCommand} (install ccqc globally to keep machine paths out of this folder)`;
} else {
  fail("ccqc not found: run install.cmd / install.sh, or build it: go build -o plugin/bin/ ./cmd/ccqc");
}

// 1. MCP server.
const mcpPath = join(folder, ".mcp.json");
const mcp = readJSON(mcpPath);
mcp.mcpServers ??= {};
const before = mcp.mcpServers.ccqc;
if (before && (before.command !== mcpCommand || JSON.stringify(before.args) !== JSON.stringify(["mcp"]))) {
  console.warn(`ccqc: replacing the existing MCP server "ccqc" in .mcp.json: ${JSON.stringify(before)}`);
}
mcp.mcpServers.ccqc = { command: mcpCommand, args: ["mcp"] };
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
const busy = { type: "command", command: hookCommand, args: ["busy"], timeout: 5 };
setHooks(st.hooks, "SessionStart", [hello, wait]);
setHooks(st.hooks, "UserPromptSubmit", [busy]);
setHooks(st.hooks, "Stop", [wait]);
writeJSON(settingsPath, st);

console.log(`ccqc: folder ${folder} set up.
- runs ${using}
- written: .mcp.json (server "ccqc"), .claude/settings.local.json (hooks hello/wait/busy, permission mcp__ccqc)
- restart Claude Code here (from a new terminal); then, in each session: /quant-join <token> <nick>
- every session keeps its own chats and nick; resuming a session rejoins them automatically
- incoming messages wake the session (rewake hook), no extra flags needed
- optional, once per machine: chat messages as expandable cards in the transcript (display only,
  separate from the chat plugin, so no second chat server starts):
    claude plugin marketplace add ${JSON.stringify(repo)}
    claude plugin install quant-ui@ccqc-local`);

// --- helpers ---

// globalCcqc: an installed ccqc: {path, onPath}; path is "" if there is none. Looks on PATH
// first, then in the installers' default place.
function globalCcqc() {
  const name = isWin ? "ccqc.exe" : "ccqc";
  const dirs = (process.env.PATH || "").split(isWin ? ";" : ":").filter(Boolean);
  const pathDirs = dirs.length;
  dirs.push(isWin ? join(process.env.LOCALAPPDATA || "", "Programs", "ccqc") : join(process.env.HOME || "", ".local", "bin"));
  for (const [i, d] of dirs.entries()) {
    const p = join(d, name);
    if (resolve(p) !== resolve(repoExe) && existsSync(p)) return { path: p, onPath: i < pathDirs };
  }
  return { path: "", onPath: false };
}

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
  if (existsSync(p) && JSON.stringify(readJSON(p)) === JSON.stringify(v)) return; // keep formatting
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
