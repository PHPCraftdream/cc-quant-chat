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
import { Card, Plain, RawCard } from './cards'
import type { Open, UI } from './cards'
import { chatOfToken, clean, parseBatch, parseLines, parseSent, textOf, toolKind } from './format'
import type { Batch, Entry, Msg, ToolKind } from './format'

// Which rows of a card are open: one value per drawn row (message or tool call), keyed by the
// row's id. '*' stands for "all rows" (the expand/collapse-all button).
const open = atom({ plugin: 'quant-ui', key: 'open' } as const, {} as Open)

// Batches seen as rows of the conversation (session.append), by the row's id: a way to draw a
// batch when the transcript row itself carries no text.
const stored = atom({ plugin: 'quant-ui', key: 'batches' } as const, [] as Stored[], {
  shape: 'batches-1',
})

const BATCH_TEXT = /ccqc: \d+ new chat event\(s\)\. /
const KEEP = 20
const KEEP_CHARS = 60000

const str = (v: unknown): string => (typeof v === 'string' ? v : '')

function rowText(content: ReadonlyArray<{ type: string; text?: string }>): string {
  return content
    .filter(b => b.type === 'text' && typeof b.text === 'string')
    .map(b => b.text)
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
      // The token and the invitation are secrets: the invitation is not even looked into (it names the
      // chat and the nick, which the broker's answer shows); of a token, only the chat name and the nick.
      if (str(input.invite)) return { title: 'ccqc · join by invitation' }
      return { title: `ccqc · join /${chatOfToken(input.token) || '?'}`, subtitle: `as @${str(input.name)}` }
    default:
      return { title: 'ccqc' }
  }
}

export const register: Register = on => {
  on('session.append', async ($, e, next) => {
    const text = rowText(e.message.content as ReadonlyArray<{ type: string; text?: string }>)
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
    if (!batch) {
      const hit = (await read($, stored)).find(s => s.id === e.requestId)
      if (hit) {
        batch = parseBatch(hit.text)
        raw = hit.text
      }
    }
    if (!batch) return next(e)

    const ui = $.ui.resolve(e) as UI
    const state = memberOf(open, e)
    const o = await read($, state)
    const toggle = (k: string) => update($, state, m => ({ ...m, [k]: !(m[k] ?? m['*'] ?? false) }))
    const title = batch.omitted > 0 ? `ccqc ← ${batch.count} (last ${batch.count - batch.omitted} shown)` : `ccqc ← ${batch.count}`
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
      onAll: v => update($, state, () => ({ '*': v })),
    })
  })

  // Outgoing and reading: the tool's row becomes the card.
  on('ui.render', { component: 'ToolUse' }, async ($, e, next) => {
    const kind = toolKind(e.props.tool)
    if (!kind) return next(e)
    // ctrl+o shows the engine's own full row; a join keeps the card there too: its row would
    // carry the token.
    if (e.props.isExpanded && kind !== 'join') return next(e)

    const ui = $.ui.resolve(e) as UI
    const input = (e.props.input ?? {}) as Record<string, unknown>
    const text = textOf(e.props.output)
    const state = memberOf(open, e)
    const o = await read($, state)
    const toggle = (k: string) => update($, state, m => ({ ...m, [k]: !(m[k] ?? m['*'] ?? false) }))
    const onAll = (v: boolean) => update($, state, () => ({ '*': v }))
    const { title, subtitle } = kind === 'send' ? { title: 'ccqc →', subtitle: str(input.to) } : heading(kind, input)

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
        title: `ccqc → ${clean(str(input.to))}`,
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

  // The result is part of the card: nothing is drawn under it again.
  on('ui.render', { component: 'ToolResult' }, ($, e, next) => {
    if (!toolKind(e.props.tool) || e.props.isExpanded) return next(e)
    const { Box } = $.ui.resolve(e)
    return <Box />
  })
}
