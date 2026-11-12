import { expect, test } from 'claude-code/testing'

import type { Stored } from '../types'
import { pickBatch } from './match'

const st = (id: string): Stored => ({ id, text: 'text of ' + id })
const A = 'a1111111-0000-4000-8000-000000000001'
const B = 'b2222222-0000-4000-8000-000000000002'
const C = 'c3333333-0000-4000-8000-000000000003'

test('a row is matched by its whole id, or by an id that holds the other', () => {
  const m = new Map<string, string>()
  expect(pickBatch(B, [st(A), st(B)], m)).toMatchObject({ how: 'id', hit: { id: B } })
  expect(pickBatch('user-' + A, [st(A), st(B)], m)).toMatchObject({ how: 'id', hit: { id: A } })
  expect(pickBatch(C + ':0', [st(C)], m)).toMatchObject({ how: 'id', hit: { id: C } })
})

test('the first segment of the uuid names the batch when only one stored batch has it (as in live rows)', () => {
  const m = new Map<string, string>()
  const stored = [st('755d3bba-1111-4222-8333-444444444444'), st('b93aa80f-1111-4222-8333-444444444444')]
  const got = pickBatch('b93aa80f-aaaa-4bbb-8ccc-dddddddddddd', stored, m)
  expect(got.how).toBe('segment')
  expect(got.hit?.id).toBe('b93aa80f-1111-4222-8333-444444444444')
})

test('without a match the newest batch that no other row has taken is given, and the same row gets it again', () => {
  const m = new Map<string, string>()
  const stored = [st(A), st(B)]
  const r1 = pickBatch('ffff0001-0000-4000-8000-000000000000', stored, m)
  expect(r1).toMatchObject({ how: 'newest unused', hit: { id: B } })
  expect(pickBatch('ffff0001-0000-4000-8000-000000000000', stored, m)).toMatchObject({ how: 'claimed', hit: { id: B } }) // drawn again
  expect(pickBatch('ffff0002-0000-4000-8000-000000000000', stored, m)).toMatchObject({ how: 'newest unused', hit: { id: A } })
  expect(pickBatch('ffff0003-0000-4000-8000-000000000000', stored, m).hit).toBeUndefined() // nothing left that is not taken
})

test('a batch that a row took by id is not given to another row by the last resort', () => {
  const m = new Map<string, string>()
  const stored = [st(A), st(B)]
  expect(pickBatch(B, stored, m).hit?.id).toBe(B)
  expect(pickBatch('ffff0004-0000-4000-8000-000000000000', stored, m)).toMatchObject({ how: 'newest unused', hit: { id: A } })
})

test('two stored batches with the same first segment do not name one: the segment is no guess, the last resort decides', () => {
  const m = new Map<string, string>()
  const stored = [st('abcd0000-1111-4222-8333-444444444444'), st('abcd0000-5555-4666-8777-888888888888')]
  const got = pickBatch('abcd0000-9999-4aaa-8bbb-cccccccccccc', stored, m)
  expect(got.how).toBe('newest unused')
})
