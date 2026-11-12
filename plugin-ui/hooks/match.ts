import type { Stored } from '../types'

// Which batch that session.append stored belongs to a row that is being drawn.
//
// The two ids should be one: the row's uuid. In the live engine they are not always: the row of a Stop hook's text
// (a task notification) is drawn under an id that shares only the first segment of the uuid with the one
// session.append saw (36 characters each, the first 8 hex the same, the rest not: observed). So the match goes by
// what is known, and then by a guess that is made once and kept:
//
//  1. the row was matched before: the same batch again (a row is drawn many times);
//  2. the whole id, or one id holding the other (an id with a prefix or a suffix);
//  3. the first segment of the uuid, when exactly one stored batch has it;
//  4. otherwise the newest stored batch that no other row has taken. This admits that a row can get a batch that
//     is not its own (a row drawn again after a restart of the module, with batches stored for other rows meanwhile);
//     it never takes one that another row has, and a batch is not removed, since a row is drawn again and again.
export type Pick = { hit?: Stored; how: 'claimed' | 'id' | 'segment' | 'newest unused' | 'none' }

const segment = (id: string): string => id.split('-', 1)[0] ?? ''

const holds = (a: string, b: string): boolean => a.length >= 32 && b.length >= 32 && (a.includes(b) || b.includes(a))

// claimed: row id -> id of the stored batch it took; pickBatch writes to it (the same answer every time after).
export function pickBatch(rowId: string, stored: readonly Stored[], claimed: Map<string, string>): Pick {
  const take = (hit: Stored, how: Pick['how']): Pick => {
    claimed.set(rowId, hit.id)
    return { hit, how }
  }
  const before = claimed.get(rowId)
  if (before !== undefined) {
    const hit = stored.find(s => s.id === before)
    if (hit) return { hit, how: 'claimed' }
  }
  const whole = stored.find(s => s.id === rowId) ?? stored.find(s => holds(s.id, rowId))
  if (whole) return take(whole, 'id')
  const seg = segment(rowId)
  if (seg.length >= 8) {
    const same = stored.filter(s => segment(s.id) === seg)
    if (same.length === 1 && same[0]) return take(same[0], 'segment')
  }
  const used = new Set(claimed.values())
  for (let i = stored.length - 1; i >= 0; i--) {
    const s = stored[i]
    if (s && !used.has(s.id)) return take(s, 'newest unused')
  }
  return { how: 'none' }
}
