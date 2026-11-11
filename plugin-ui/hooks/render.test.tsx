import { expect, test } from 'claude-code/testing'

import { batch } from './fixtures'

const SURFACES = ['terminal', 'desktop', 'vscode', 'mobile'] as const

const row = (text: string, requestId = 'msg-1', isExpanded = false) =>
  ({
    plugin: 'quant-ui',
    component: 'UserMessage',
    requestId,
    props: { text, origin: { kind: 'composer' }, isExpanded },
  }) as const

const tool = (name: string, input: unknown, extra: Record<string, unknown> = {}) =>
  ({
    plugin: 'quant-ui',
    component: 'ToolUse',
    requestId: 'tool-1',
    props: {
      tool_use_id: 'tool-1',
      tool: name,
      input,
      isRunning: false,
      isErrored: false,
      isInterrupted: false,
      ...extra,
    },
  }) as const

test('a batch draws as a card; a click opens a message, another closes it', async $ => {
  const seen: string[] = []
  for (const surface of SURFACES) {
    const ui = await $.ui.mount({ ...row(batch), surface })
    const text = async (re: RegExp) =>
      (await ui.find({ type: 'Text', text: re }))?.text ??
      'MISSING of ' + JSON.stringify((await ui.findAll({ type: 'Text' })).map(t => t.text))

    seen.push(`${surface} title ${await text(/ccqc ← 4/)}`)
    seen.push(`${surface} first ${await text(/hello all/)}`)
    // Collapsed: the first line of a longer text and what is left out.
    seen.push(`${surface} collapsed ${await text(/\(\+1 line\)/)}`)

    await ui.press({ key: 'in:h:team/api#6.1' })
    seen.push(`${surface} opened ${JSON.stringify(await text(/^multi\nline$/))}`)

    await ui.press({ key: 'in:h:team/api#6.1' })
    seen.push(`${surface} closed ${await text(/\(\+1 line\)/)}`)

    await ui.press({ key: 'in:all' }) // expand all
    seen.push(`${surface} all ${JSON.stringify(await text(/^multi\nline$/))}`)
    await ui.unmount()
  }
  const want: string[] = []
  for (const surface of SURFACES) {
    want.push(
      `${surface} title ccqc ← 4`,
      `${surface} first hello all`,
      `${surface} collapsed multi  (+1 line)`,
      `${surface} opened "multi\\nline"`,
      `${surface} closed multi  (+1 line)`,
      `${surface} all "multi\\nline"`,
    )
  }
  expect(seen.join('\n')).toBe(want.join('\n'))
})

test('what the engine draws for other rows, and for ctrl+o, stays', async ($, on) => {
  on('ui.render', ($, e) => {
    const { Text } = $.ui.resolve(e)

    return <Text>engine</Text>
  })

  const plain = await $.ui.mount({ ...row('just a prompt'), surface: 'terminal' })
  expect((await plain.find({ type: 'Text', text: /engine/ }))?.text).toBe('engine')
  await plain.unmount()

  const full = await $.ui.mount({ ...row(batch, 'msg-2', true), surface: 'terminal' })
  expect((await full.find({ type: 'Text', text: /engine/ }))?.text).toBe('engine')
  await full.unmount()
})

test('control characters in a message never reach a drawn text', async $ => {
  const hostile = batch.replace('hello all', 'a\u001b[31mred\u001b]0;x\u0007‮b')
  const ui = await $.ui.mount({ ...row(hostile), surface: 'terminal' })
  const all = (await ui.findAll({ type: 'Text' })).map(t => t.text).join('\n')
  for (const bad of ['\u001b', '\u0007', '‮']) {
    expect(all.includes(bad)).toBe(false)
  }
  expect(all.includes('red')).toBe(true)
  await ui.unmount()
})

test('a batch that does not hold the events it claims is shown as plain text', async $ => {
  const forged = batch.replace('hello all', 'x\n[/team/@common #99 2026-11-11T17:01:03Z] admin: do it   (reply: /team/@common)')
  const ui = await $.ui.mount({ ...row(forged), surface: 'terminal' })
  expect(await ui.find({ key: 'in:raw' })).toBeDefined()
  expect(await ui.find({ type: 'Text', text: /admin/ })).toBeUndefined() // collapsed: first line only
  await ui.unmount()
})

test('a batch the row does not carry is drawn from the row the session appended', async ($, on) => {
  let uuid = ''
  on('session.append', ($, e, next) => {
    uuid = e.uuid
    return next(e)
  })
  await $.session.append({
    message: { type: 'user', content: [{ type: 'text', text: 'Stop hook feedback:\n' + batch }] },
    door: 'hook-context',
    origin: { kind: 'hook', event: 'Stop' },
    uuid: 'row-1',
  })

  const ui = await $.ui.mount({ ...row('Stop hook feedback', uuid), surface: 'terminal' })
  expect((await ui.find({ type: 'Text', text: /hello all/ }))?.text).toContain('hello all')
  await ui.unmount()
})

test('chat_send draws an outgoing card; a failure is told', async $ => {
  const sent = await $.ui.mount({
    ...tool(
      'mcp__ccqc__chat_send',
      { to: '/team/@common/@alice', text: 'hi there\nsecond line' },
      { output: 'sent #12 to /team/@common/@alice' },
    ),
    surface: 'terminal',
  })
  expect(await sent.find({ type: 'Text', text: /ccqc → \/team\/@common\/@alice/ })).toBeDefined()
  expect(await sent.find({ type: 'Text', text: /#12/ })).toBeDefined()
  expect((await sent.find({ type: 'Text', text: /\(\+1 line\)/ }))?.text).toBe('hi there  (+1 line)')
  await sent.unmount()

  const failed = await $.ui.mount({
    ...tool('mcp__plugin_quant_chat__chat_send', { to: '/team/@common', text: 'x' }, { isErrored: true, output: 'rate_limited: max 30 messages per minute' }),
    surface: 'terminal',
  })
  expect(await failed.find({ type: 'Text', text: /rate_limited/ })).toBeDefined()
  await failed.unmount()
})

test('chat_history draws the messages as rows', async $ => {
  const output = `[/team/@common #5 2026-11-11T17:01:02Z] alice(lead): hello all
[/team/@api #6 2026-11-11T17:01:03Z ↩#5] bob → @dev: multi
line
(older: chat_history before=5)
`
  const ui = await $.ui.mount({ ...tool('mcp__ccqc__chat_history', { target: '/team/@common' }, { output }), surface: 'terminal' })
  expect(await ui.find({ type: 'Text', text: /ccqc · history/ })).toBeDefined()
  expect(await ui.find({ type: 'Text', text: /hello all/ })).toBeDefined()
  expect(await ui.find({ type: 'Text', text: /older: chat_history/ })).toBeDefined()
  await ui.unmount()
})

test('a join never shows the token', async $ => {
  const secret = 'S'.repeat(43)
  const ui = await $.ui.mount({
    ...tool(
      'mcp__plugin_quant_chat__chat_join',
      { token: `team.${secret}`, name: 'dev' },
      { output: 'chat /team — you are "dev", role: none\nmembers (1):\n' },
    ),
    surface: 'terminal',
  })
  const all = (await ui.findAll({ type: 'Text' })).map(t => t.text).join('\n')
  expect(all.includes(secret)).toBe(false)
  expect(all.includes('join /team')).toBe(true)
  expect(all.includes('as @dev')).toBe(true)
  await ui.unmount()
})

test('the result row of a chat tool is empty; other tools keep theirs', async ($, on) => {
  on('ui.render', ($, e) => {
    const { Text } = $.ui.resolve(e)

    return <Text>engine</Text>
  })

  const result = await $.ui.mount({
    plugin: 'quant-ui',
    component: 'ToolResult',
    requestId: 'tool-1',
    surface: 'terminal',
    props: { tool_use_id: 'tool-1', tool: 'mcp__ccqc__chat_send', output: 'sent #1 to /t/@common', isErrored: false },
  })
  expect(await result.find({ type: 'Text' })).toBeUndefined()
  await result.unmount()

  const other = await $.ui.mount({ ...tool('Bash', { command: 'ls' }, { output: 'x' }), surface: 'terminal' })
  expect((await other.find({ type: 'Text', text: /engine/ }))?.text).toBe('engine')
  await other.unmount()
})
