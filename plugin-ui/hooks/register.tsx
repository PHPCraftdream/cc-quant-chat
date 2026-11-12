// quant-ui: shows ccqc chat traffic as cards in the transcript, display only.
//
//  - incoming batches (the wake-up the `ccqc wait` hook delivers) as a card per batch,
//  - chat_send as an outgoing card,
//  - chat_history / timeline / search / context / inbox / join as cards of the same message rows.
//
// Rows expand and collapse on click. Nothing here changes what a tool receives or returns, what
// the model reads, or any permission: a hook that does not know a row hands it on with next(e).
import { atom, memberOf, read, update } from 'claude-code'
import type { Register } from 'claude-code'

import type { Stored } from '../types'
import { Card, Plain, RawCard, flip } from './cards'
import type { Open, UI } from './cards'
import { clean, parseBatch, parseLines, parseSent, textOf, toolKind } from './format'
import type { Batch, Entry, Msg, ToolKind } from './format'
import { pickBatch } from './match'

// Which rows of a card are open: one value per drawn row (message or tool call), keyed by the
// row's id. '*' stands for "all rows" (the expand/collapse-all button).
const open = atom({ plugin: 'quant-ui', key: 'open' } as const, {} as Open)

// Batches seen as rows of the conversation (session.append), by the row's id: a way to draw a
// batch when the transcript row itself carries no text.
const stored = atom({ plugin: 'quant-ui', key: 'batches' } as const, [] as Stored[], {
  shape: 'batches-1',
})

// Which stored batch each row has taken (row id -> batch id), in the memory of the module: a write of state is
// refused while a row is drawn, and a row is drawn again and again; see match.ts.
const claimed = new Map<string, string>()

const BATCH_TEXT = /ccqc: \d+ new chat event\(s\)\. /
const KEEP = 50
const KEEP_CHARS = 60000

const str = (v: unknown): string => (typeof v === 'string' ? v : '')

// The text of a row's content. The engine's type says a list of blocks, but a notification row (a Stop hook's
// text, delivered as a task notification) keeps its content as one string, as the transcript file shows; with
// `.filter` on a string the hook threw, nothing was stored and the card had no text. A list is read as before.
function rowText(content: unknown): string {
  if (typeof content === 'string') return content
  if (!Array.isArray(content)) return ''
  return (content as ReadonlyArray<{ type?: string; text?: unknown }>)
    .filter(b => b?.type === 'text' && typeof b.text === 'string')
    .map(b => b.text as string)
    .join('\n')
}

// target: "/team/@room/@alice" → its parts, for the outgoing card.
function target(to: string): { chat: string; room: string; member?: string } {
  const [, chat = '', room = '', member = ''] = to.split('/')
  return { chat, room: room.replace(/^@/, ''), member: member ? member.replace(/^@/, '') : undefined }
}

function heading(kind: ToolKind, input: Record<string, unknown>): { title: string; subtitle?: string } {
  switch (kind) {
    case 'history':
      return { title: 'ccqc · history', subtitle: str(input.target) }
    case 'timeline': {
      const who = str(input.member)
      return { title: 'ccqc · timeline', subtitle: [str(input.chat) && `/${str(input.chat)}`, who && `@${who}`].filter(Boolean).join(' ') }
    }
    case 'search':
      return { title: 'ccqc · search', subtitle: str(input.query) || (str(input.from) && `from @${str(input.from)}`) || '' }
    case 'context': {
      const ids = Array.isArray(input.ids) ? input.ids.join(', ') : ''
      return { title: 'ccqc · context', subtitle: [str(input.chat) && `/${str(input.chat)}`, ids && `#${ids}`].filter(Boolean).join(' ') }
    }
    case 'inbox':
      return { title: 'ccqc · inbox' }
    case 'join':
      // The invitation is a secret: it is not even looked into (it names the chat and the nick, which the
      // broker's answer shows).
      return { title: 'ccqc · join by invitation' }
    default:
      return { title: 'ccqc' }
  }
}

export const register: Register = on => {
  on('session.append', async ($, e, next) => {
    const text = rowText(e.message.content)
    if (BATCH_TEXT.test(text)) {
      const at = text.search(BATCH_TEXT)
      await update($, stored, list =>
        [...list, { id: e.uuid, text: text.slice(at, at + KEEP_CHARS) }].slice(-KEEP),
      )
    }
    return next(e)
  }).catch(($, e, next) => next(e)) // an observer: if it fails the row is stored as it is

  // Incoming messages: the row the model's wake-up shows as "Stop hook feedback".
  on('ui.render', { component: 'UserMessage' }, async ($, e, next) => {
    if (e.props.isExpanded) return next(e)
    let batch: Batch | null = BATCH_TEXT.test(e.props.text) ? parseBatch(e.props.text) : null
    let raw = e.props.text
    const seen = batch ? [] : await read($, stored)
    // Which stored batch is this row's: see match.ts (the ids are not always one).
    const pick = batch ? undefined : pickBatch(String(e.requestId), seen, claimed)
    const hit = pick?.hit
    if (hit) {
      batch = parseBatch(hit.text)
      raw = hit.text
    }
    if (!batch) {
      // A Stop hook's text arrives as a task notification: the row's text is its summary alone, the batch
      // is in the content that session.append saw. If that was not seen (a session resumed from its file,
      // state lost), say so rather than leave a bare summary that looks like nothing was said.
      if (e.props.origin?.kind === 'task-notification' && /^Stop hook feedback/.test(e.props.text)) {
        const ui = $.ui.resolve(e) as UI
        // What a person can send back when it still happens: whole ids (uuids, no secret), lengths and how the match
        // went; of a stored batch only its first 60 characters.
        const debug = [
          `row ${String(e.requestId)}, text ${e.props.text.length} chars, props: ${Object.keys(e.props).join(',')}`,
          `origin ${JSON.stringify(e.props.origin)}, task ${JSON.stringify(e.props.task ?? null)}`,
          `batches seen by session.append: ${seen.length}${seen.length ? ' ' + JSON.stringify(seen.map(s => s.id)) : ''}`,
          hit
            ? `matched by ${pick?.how} a stored batch of ${hit.text.length} chars, starts ${JSON.stringify(hit.text.slice(0, 60))}; read as ${batch ? 'a batch' : 'null'}`
            : 'no stored batch matched this row',
        ].join('\n')
        return Plain(ui, {
          title: 'Stop hook feedback',
          color: 'gray',
          text: 'the chat cards do not have the text of this row (ctrl+o shows it)\n' + debug,
          dim: true,
        })
      }
      return next(e)
    }

    const ui = $.ui.resolve(e) as UI
    const state = memberOf(open, e)
    const o = await read($, state)
    const toggle = (k: string) => update($, state, m => flip(m, k))
    const title = batch.omitted > 0 ? `ccqc in ${batch.count} (last ${batch.count - batch.omitted} shown)` : `ccqc in ${batch.count}`
    if (!batch.intact) {
      return RawCard(ui, { key: 'in', title: `${title} · unparsed`, color: 'yellow', text: raw, open: o, onToggle: toggle })
    }
    return Card(ui, {
      key: 'in',
      title,
      color: 'cyan',
      entries: batch.entries,
      open: o,
      onToggle: toggle,
      onAll: v => update($, state, () => ({ '*': v, '*+': v })),
    })
  })

  // Outgoing and reading: the tool's row becomes the card.
  on('ui.render', { component: 'ToolUse' }, async ($, e, next) => {
    const kind = toolKind(e.props.tool)
    if (!kind) return next(e)
    // The card is drawn whatever isExpanded says: the group unfolded below makes every row of it expanded,
    // so isExpanded cannot tell that from ctrl+o, and passing on would draw the engine's own full row.

    const ui = $.ui.resolve(e) as UI
    const input = (e.props.input ?? {}) as Record<string, unknown>
    const text = textOf(e.props.output)
    const state = memberOf(open, e)
    const o = await read($, state)
    const toggle = (k: string) => update($, state, m => flip(m, k))
    const onAll = (v: boolean) => update($, state, () => ({ '*': v, '*+': v }))
    const { title, subtitle } = kind === 'send' ? { title: 'ccqc out', subtitle: str(input.to) } : heading(kind, input)

    if (e.props.isRunning) {
      return Plain(ui, { title: `${title} ${clean(subtitle ?? '')}`.trim(), color: 'gray', text: '…', dim: true })
    }
    if (e.props.isErrored || e.props.isInterrupted) {
      return Plain(ui, { title: `${title} ${clean(subtitle ?? '')}`.trim(), color: 'red', text: text || 'failed' })
    }

    if (kind === 'send') {
      const id = parseSent(text)
      const t = target(str(input.to))
      const msg: Msg = {
        type: 'msg',
        chat: t.chat,
        room: t.room,
        id: id ?? 0,
        ts: '',
        from: 'you',
        to: t.member,
        text: str(input.text),
        replyTo: typeof input.reply_to === 'number' && input.reply_to > 0 ? input.reply_to : undefined,
      }
      return Card(ui, {
        key: 'out',
        title: `ccqc out ${clean(str(input.to))}`,
        subtitle: id ? `#${id}` : undefined,
        color: 'green',
        entries: [msg],
        open: o,
        onToggle: toggle,
        onAll,
      })
    }

    const entries: Entry[] = parseLines(text, true)
    const n = entries.filter(x => x.type === 'msg').length
    return Card(ui, {
      key: kind,
      title,
      subtitle: [subtitle, n > 0 ? `${n} message${n === 1 ? '' : 's'}` : ''].filter(Boolean).join(' · '),
      color: 'cyan',
      entries,
      open: o,
      onToggle: toggle,
      onAll,
      maxNotes: kind === 'join' ? 14 : undefined,
    })
  })

  // Claude Code folds a run of calls (MCP calls among them: "Called ccqc") into one count line, and a
  // ToolUse hook is not asked for a call that is folded. A group that holds a chat call is unfolded where it
  // is: each call is then a ToolUse row of its own and the cards above draw it.
  on('ui.render', { component: 'ToolGroup' }, ($, e, next) => {
    if (e.props.isExpanded || !e.props.calls.some(c => toolKind(c.tool))) return next(e)
    return next({ ...e, props: { ...e.props, isExpanded: true } })
  })

  // The result is part of the card: nothing is drawn under it again.
  on('ui.render', { component: 'ToolResult' }, ($, e, next) => {
    if (!toolKind(e.props.tool)) return next(e)
    const { Box } = $.ui.resolve(e)
    return <Box />
  })
}
