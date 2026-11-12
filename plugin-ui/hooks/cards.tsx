// The cards: trees built from the surface's own Box / Text / Button, handed in as `ui`.
// Only display. Every text from the chat goes through clean() first.

import { address, clean, extraLines, firstLine, flavor, unwrap } from './format'
import type { Entry, Flavor, Msg } from './format'

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export type UI = { Box: any; Text: any; Button: any }
export type Open = Record<string, boolean>

export type CardProps = {
  key: string // prefix of the card's button keys
  title: string
  subtitle?: string
  color: string
  entries: Entry[]
  open: Open
  onToggle: (rowKey: string) => void
  onAll: (isOpen: boolean) => void
  maxNotes?: number
}

const MARK: Record<Flavor, string> = { direct: '✉', 'to-me': '@', addressed: '›', room: '•', common: '•' }
const COLOR: Record<Flavor, string | undefined> = {
  direct: 'magenta',
  'to-me': 'yellow',
  addressed: 'cyan',
  room: undefined,
  common: undefined,
}

const pad = (n: number) => String(n).padStart(2, '0')

// when: the local time of a message, with the date when it is not today's.
export function when(ts: string): string {
  const d = new Date(ts)
  if (ts === '' || Number.isNaN(d.getTime())) return ts === '' ? '' : clean(ts)
  const t = `${pad(d.getHours())}:${pad(d.getMinutes())}`
  return d.toDateString() === new Date().toDateString()
    ? t
    : `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${t}`
}

export const rowKey = (m: Msg, i: number): string => `${m.chat}/${m.room}#${m.id}.${i}`

const isOpenRow = (o: Open, k: string): boolean => o[k] ?? o['*'] ?? false

function MsgRow(ui: UI, p: CardProps, m: Msg, i: number) {
  const { Box, Text, Button } = ui
  const k = rowKey(m, i)
  const isOpen = isOpenRow(p.open, k)
  const f = flavor(m)
  const color = COLOR[f]
  const who = m.from + (m.role ? `(${m.role})` : '') + (m.to ? ` → @${m.to}` : '')
  const meta = [address(m), m.id ? `#${m.id}` : '', when(m.ts), m.replyTo ? `↩#${m.replyTo}` : '', m.hit ? '«' : '']
    .filter(Boolean)
    .join(' ')
  const extra = extraLines(m.text)
  return (
    <Box flexDirection="column">
      <Button key={`${p.key}:h:${k}`} plain onPress={() => p.onToggle(k)}>
        <Text color={color}>{isOpen ? '▾' : '▸'} {MARK[f]} </Text>
        {m.owner ? <Text bold inverse color="red"> OWNER </Text> : null}
        {m.owner ? <Text> </Text> : null}
        <Text bold color={color}>{clean(who)}</Text>
        <Text dimColor> {meta}</Text>
      </Button>
      <Box paddingLeft={2}>
        {isOpen ? (
          <Text>{clean(m.text)}</Text>
        ) : (
          <Button key={`${p.key}:p:${k}`} plain onPress={() => p.onToggle(k)}>
            <Text wrap="truncate-end" dimColor>
              {firstLine(m.text) + (extra > 0 ? `  (+${extra} line${extra === 1 ? '' : 's'})` : '')}
            </Text>
          </Button>
        )}
      </Box>
    </Box>
  )
}

function OtherRow(ui: UI, e: Entry, i: number) {
  const { Text } = ui
  switch (e.type) {
    case 'presence':
      return (
        <Text dimColor italic>
          · {clean(e.lines.join('; '))}
        </Text>
      )
    case 'board':
      return (
        <Text color="cyan">
          ▣ /{e.chat} board: {clean(e.text)}
        </Text>
      )
    case 'notice':
      return (
        <Text color="red">
          ! /{e.chat}: {clean(e.text)}
        </Text>
      )
    case 'note':
      return (
        <Text dimColor>
          {clean(e.text)}
        </Text>
      )
    default:
      return null
  }
}

export function Card(ui: UI, p: CardProps) {
  const { Box, Text, Button } = ui
  const msgs = p.entries.filter((e): e is Msg => e.type === 'msg')
  const allOpen = msgs.length > 0 && msgs.every((m, i) => isOpenRow(p.open, rowKey(m, p.entries.indexOf(m))))
  let notes = 0
  const rows = p.entries.map((e, i) => {
    if (e.type === 'msg') return MsgRow(ui, p, e, i)
    if (e.type === 'note' && p.maxNotes !== undefined && ++notes > p.maxNotes) return null
    return OtherRow(ui, e, i)
  })
  return (
    <Box flexDirection="column" borderStyle="round" borderColor={p.color} paddingX={1}>
      <Box>
        <Text bold color={p.color}>{p.title}</Text>
        {p.subtitle ? <Text dimColor>  {clean(p.subtitle)}</Text> : null}
        {msgs.length > 0 ? <Text>  </Text> : null}
        {msgs.length > 0 ? (
          <Button key={`${p.key}:all`} plain onPress={() => p.onAll(!allOpen)}>
            {allOpen ? '[collapse all]' : '[expand all]'}
          </Button>
        ) : null}
      </Box>
      {rows}
    </Box>
  )
}

// RawCard: text that did not parse as the events it claims to hold (see parseBatch's `intact`):
// shown as it is, cleaned, instead of as messages nobody may have sent.
export function RawCard(ui: UI, p: { key: string; title: string; color: string; text: string; open: Open; onToggle: (k: string) => void }) {
  const { Box, Text, Button } = ui
  const isOpen = isOpenRow(p.open, 'raw')
  const text = unwrap(p.text) // the hook's wrapper is no part of what is shown
  return (
    <Box flexDirection="column" borderStyle="round" borderColor={p.color} paddingX={1}>
      <Button key={`${p.key}:raw`} plain onPress={() => p.onToggle('raw')}>
        <Text bold color={p.color}>{isOpen ? '▾ ' : '▸ '}{p.title}</Text>
      </Button>
      {isOpen ? <Text>{clean(text)}</Text> : <Text wrap="truncate-end" dimColor>{firstLine(text)}</Text>}
    </Box>
  )
}

// Line(s) of plain status inside a card's place (a call still running, a failure).
export function Plain(ui: UI, p: { title: string; color: string; text: string; dim?: boolean }) {
  const { Box, Text } = ui
  return (
    <Box flexDirection="column" borderStyle="round" borderColor={p.color} paddingX={1}>
      <Text bold color={p.color}>{p.title}</Text>
      {p.text ? <Text dimColor={p.dim}>{clean(p.text)}</Text> : null}
    </Box>
  )
}
