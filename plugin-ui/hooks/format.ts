// Pure parsing and cleaning of what ccqc prints, for the cards. No engine calls here.
//
// WIRE FORMAT: the text comes from internal/mcp/render.go (fmtMsg, eventLine), internal/mcp/
// sessionpipe.go (renderBatch), query.go and join.go. Go's TestWireFormat (internal/mcp/
// format_test.go) pins that output against hooks/fixtures.ts; this file's tests parse the same
// fixtures. Change one side and the other fails until both agree.

export type Msg = {
  type: 'msg'
  chat: string
  room: string
  id: number
  ts: string
  replyTo?: number
  from: string
  role?: string
  to?: string
  text: string
  reply?: string // address to answer on (batches and chat_inbox only)
  hit?: boolean // chat_context marks the messages it was asked about
  owner?: boolean // sent over a connection the broker verified as the owner's (marker OWNER)
}
export type Presence = { type: 'presence'; chat: string; lines: string[] }
export type Notice = { type: 'notice'; chat: string; text: string }
export type Note = { type: 'note'; text: string }
export type Entry = Msg | Presence | Notice | Note

export type Batch = {
  count: number // what the header says: events in the batch
  omitted: number // older events only named ("call chat_inbox")
  entries: Entry[]
  intact: boolean // the parse found exactly the events the header promised
}

// What ccqc's renderBatch opens with.
const BATCH_HEAD = /ccqc: (\d+) new chat event\(s\)\. [^\n]*\n/
const OMITTED = /^\((\d+) older events omitted: call chat_inbox\)$/
const MSG_HEAD =
  /^( {2}| {3}| » )?\[\/([a-z0-9-]+)\/@([a-z0-9-]+) #(\d+) ([^\s\]]+)(?: ↩#(\d+))?\] (?:(OWNER) )?([a-z0-9-]+)(?:\(([^)\n]*)\))?(?: → @([a-z0-9-]+))?: ?(.*)$/
const PRESENCE = /^\[\/([a-z0-9-]+) presence\] (.*)$/
const NOTICE = /^\[\/([a-z0-9-]+)\] (.*)$/
const REPLY = / {3}\(reply: (\/[a-z0-9-]+\/@[a-z0-9-]+(?:\/@[a-z0-9-]+)?)\)$/

// A chat token is <chat>.<43 base64url characters>.
// (Not \b: the secret may end in "-" or "_", which is no word boundary.)
const TOKEN = /(?<![A-Za-z0-9_.-])([a-z0-9][a-z0-9-]{0,31})\.[A-Za-z0-9_-]{43}(?![A-Za-z0-9_-])/g

export function maskToken(s: string): string {
  return s.replace(TOKEN, '$1.' + '•'.repeat(8))
}

function isInvisible(c: number): boolean {
  return (
    (c >= 0x200b && c <= 0x200f) ||
    (c >= 0x202a && c <= 0x202e) ||
    (c >= 0x2060 && c <= 0x206f) ||
    c === 0xfeff ||
    c === 0x061c ||
    c === 0x180e ||
    (c >= 0xe0000 && c <= 0xe007f)
  )
}

// clean makes text from another agent safe for the person's terminal: no escape sequence or
// other control character survives (they show as their visible symbols), bidirectional and
// invisible format characters are dropped, tokens are masked. Newlines stay.
export function clean(s: string): string {
  let out = ''
  for (const ch of s) {
    const c = ch.codePointAt(0) as number
    if (ch === '\n') out += ch
    else if (ch === '\t') out += '  '
    else if (c === 0x0d) continue
    else if (c < 0x20) out += String.fromCodePoint(0x2400 + c)
    else if (c === 0x7f) out += '␡'
    else if (c >= 0x80 && c <= 0x9f) out += '·'
    else if (isInvisible(c)) continue
    else out += ch
  }
  return maskToken(out)
}

export function firstLine(text: string): string {
  for (const line of clean(text).split('\n')) {
    if (line.trim() !== '') return line.trim()
  }
  return ''
}

// extraLines: how many lines of text the collapsed form leaves out.
export function extraLines(text: string): number {
  const lines = clean(text)
    .split('\n')
    .filter(l => l.trim() !== '')
  return Math.max(0, lines.length - 1)
}

export type Flavor = 'direct' | 'to-me' | 'addressed' | 'room' | 'common'

// flavor: how a message relates to the reader. A reply address ending in /@<sender> marks a
// message addressed to the reader (replyAddr in render.go); without one (history) only
// "addressed to somebody" can be told.
export function flavor(m: Msg): Flavor {
  if (m.room === 'direct') return 'direct'
  if (m.reply !== undefined && m.reply.endsWith('/@' + m.from)) return 'to-me'
  if (m.to) return 'addressed'
  return m.room === 'common' ? 'common' : 'room'
}

export function address(m: Msg): string {
  return `/${m.chat}/@${m.room}`
}

// at: capture group i of a match, "" when it did not take part.
const at = (g: RegExpExecArray, i: number): string => g[i] ?? ''

function msgFrom(g: RegExpExecArray): Msg {
  const m: Msg = {
    type: 'msg',
    chat: at(g, 2),
    room: at(g, 3),
    id: Number(at(g, 4)),
    ts: at(g, 5),
    from: at(g, 8),
    text: at(g, 11),
  }
  if (at(g, 6)) m.replyTo = Number(at(g, 6))
  if (at(g, 7)) m.owner = true // the broker's mark, before the sender; a text can't make it
  if (at(g, 9)) m.role = at(g, 9)
  if (at(g, 10)) m.to = at(g, 10)
  if (at(g, 1) === ' » ') m.hit = true
  return m
}

// parseLines reads a run of ccqc's lines: message lines (indented or marked by the reading
// tools; the text goes on over the following lines until the next header), presence lines and,
// with notices on, the bracketed notices of a batch. Everything else is a Note.
export function parseLines(text: string, notices = false): Entry[] {
  const out: Entry[] = []
  let cur: Msg | null = null
  for (const line of text.split('\n')) {
    let g: RegExpExecArray | null
    if ((g = MSG_HEAD.exec(line))) {
      cur = msgFrom(g)
      out.push(cur)
    } else if ((g = PRESENCE.exec(line))) {
      cur = null
      out.push({ type: 'presence', chat: at(g, 1), lines: at(g, 2).split('; ') })
    } else if (notices && (g = NOTICE.exec(line))) {
      cur = null
      out.push({ type: 'notice', chat: at(g, 1), text: at(g, 2) })
    } else if (cur) {
      cur.text += '\n' + line
    } else if (line.trim() !== '') {
      out.push({ type: 'note', text: line })
    }
  }
  for (const e of out) {
    if (e.type !== 'msg') continue
    e.text = e.text.replace(/\n+$/, '')
    const r = REPLY.exec(e.text)
    if (r) {
      e.reply = r[1]
      e.text = e.text.slice(0, r.index)
    }
  }
  // A text that runs into a structural line of the reading tools ends there.
  return splitFooters(out)
}

// The reading tools end a listing with these lines; they are not part of the last message.
const FOOTER = /^(\(older: [^)]*\)|\(surroundings: [^)]*\)|not found or not visible to you: .*)$/m

function splitFooters(entries: Entry[]): Entry[] {
  const out: Entry[] = []
  for (const e of entries) {
    if (e.type === 'msg') {
      const at = e.text.search(FOOTER)
      if (at >= 0) {
        const rest = e.text.slice(at)
        e.text = e.text.slice(0, at).replace(/\n+$/, '')
        out.push(e)
        for (const l of rest.split('\n')) if (l.trim() !== '') out.push({ type: 'note', text: l })
        continue
      }
    }
    out.push(e)
  }
  return out
}

// parseBatch reads the text ccqc's wait hook (or the channel) hands over: a header with the
// event count, then one entry per event. null when the text holds no batch. `intact` is false
// when the entries found are not the number the header promised: a message text that imitates
// a header would otherwise draw as a message nobody sent.
export function parseBatch(text: string): Batch | null {
  const h = BATCH_HEAD.exec(text)
  if (!h) return null
  const count = Number(h[1])
  let body = text.slice(h.index + h[0].length)
  let omitted = 0
  const first = body.split('\n', 1)[0] ?? ''
  const o = OMITTED.exec(first)
  if (o) {
    omitted = Number(o[1])
    body = body.slice(first.length + 1)
  }
  const entries = parseLines(body, true).filter(e => e.type !== 'note')
  const intact =
    entries.length === count - omitted &&
    entries.every(e => e.type !== 'msg' || e.reply !== undefined)
  return { count, omitted, entries, intact }
}

// parseSent: the id in chat_send's answer ("sent #12 to /team/@common").
export function parseSent(text: string): number | null {
  const m = /^sent #(\d+) to /.exec(text.trim())
  return m ? Number(m[1]) : null
}

// textOf: the text of a tool's output, whatever shape the engine hands it in.
export function textOf(output: unknown): string {
  if (typeof output === 'string') return output
  if (Array.isArray(output)) return output.map(textOf).filter(Boolean).join('\n')
  if (output && typeof output === 'object') {
    const o = output as Record<string, unknown>
    if (typeof o.text === 'string') return o.text
    if ('content' in o) return textOf(o.content)
    if ('result' in o) return textOf(o.result)
  }
  return ''
}

// The tools the cards draw, under either name the chat server has: `ccqc` (the folder set up
// by /quant-setup) or `plugin_quant_chat` (the plugin).
const TOOL = /^mcp__(?:ccqc|plugin_quant_chat)__(chat_send|chat_history|chat_timeline|chat_search|chat_context|chat_inbox|chat_join)$/

export type ToolKind = 'send' | 'history' | 'timeline' | 'search' | 'context' | 'inbox' | 'join'

export function toolKind(tool: string): ToolKind | null {
  const m = TOOL.exec(tool)
  return m ? ((m[1] ?? '').slice('chat_'.length) as ToolKind) : null
}

// chatOfToken: the chat name in a token, never the secret.
export function chatOfToken(token: unknown): string {
  if (typeof token !== 'string') return ''
  const dot = token.indexOf('.')
  return dot > 0 ? token.slice(0, dot).slice(0, 32) : ''
}
