import assert from 'node:assert/strict'
import test from 'node:test'
import type { MainOutput, Message, StreamEvent } from '../client/types.ts'
import { createConversationState, pruneConversationWindow, reduceConversationEvent, replyTraceSegments } from './conversation.ts'
import { conversationItems, hydrateMainOutput, mainOutputText, mergeMainOutputs, reduceMainOutputEvent } from './mainOutputs.ts'
import { buildRunTrace } from './runTrace.ts'
import { conversationTimeline } from './conversationTimeline.ts'

const event = (sequence: number, kind: string, data: object): StreamEvent => ({ sequence, kind, createdAt: '2026-10-08T00:00:00Z', data: { runId: 'run', taskId: 'main', ...data } })
const message = (id: string, sequence: number, role = 'user', outputTurn?: number): Message => ({ id, runId: 'run', sequence, role, outputTurn, createdAt: '', content: id })
const output = (turn = 1, sequence = 2, lastSequence = sequence): MainOutput => ({ runId: 'run', taskId: 'main', turn, sequence, lastSequence, bytes: 3, createdAt: '', complete: false })

test('text around reasoning stays in chronological spans before and after the final reply', () => {
  const events = [
    event(2, 'model_delta', { turn: 1, content: 'Opening🙂' }),
    event(3, 'model_reasoning_delta', { turn: 1, content: 'Checking one. ' }),
    event(4, 'model_reasoning_delta', { turn: 1, content: 'Checking two. ' }),
    event(5, 'model_reasoning_delta', { turn: 1, content: 'Checking three.' }),
    event(6, 'model_delta', { turn: 1, content: 'Final answer.' }),
  ]
  let state = createConversationState()
  state = reduceConversationEvent(state, event(1, 'message', message('user', 1)))
  for (const item of events) state = reduceConversationEvent(state, item)
  function ordered() {
    const trace = buildRunTrace(events, { runId: 'run', active: false })
    return conversationTimeline(conversationItems(state.messages, state.mainOutputs), { run: trace.nodes.map(node => ({ key: node.key, sequence: node.sequence, lastSequence: node.lastSequence, estimate: 80 })) }, 1)
      .map(row => row.message ? row.message.content : 'reasoning')
  }
  assert.deepEqual(ordered(), ['user', 'Opening🙂', 'reasoning', 'Final answer.'])
  state = reduceConversationEvent(state, event(7, 'model_result', { turn: 1 }))
  state = reduceConversationEvent(state, event(8, 'run_finished', { status: 'succeeded', message: {
    ...message('reply', 8, 'assistant', 1), content: 'Opening🙂Final answer.', outputBytes: Buffer.byteLength('Opening🙂Final answer.'),
  } }))
  assert.deepEqual(ordered(), ['user', 'Opening🙂', 'reasoning', 'Final answer.'])
  assert.equal(conversationItems(state.messages, state.mainOutputs).filter(row => row.role === 'assistant' && !row.progress).length, 1)
  assert.equal(state.messages.at(-1)!.content, 'Opening🙂Final answer.')
})

test('repeated text, reasoning and child alternations match restored spans without changing the canonical reply', () => {
  const events = [
    event(2, 'model_delta', { turn: 1, offset: 0, content: '开场🙂' }),
    event(3, 'model_reasoning_delta', { turn: 1, content: 'Reason one' }),
    event(4, 'model_delta', { turn: 1, offset: Buffer.byteLength('开场🙂'), content: 'Middle' }),
    event(5, 'model_delta', { taskId: 'child', depth: 1, turn: 2, content: 'Child output' }),
    event(6, 'model_delta', { turn: 1, offset: Buffer.byteLength('开场🙂Middle'), content: '结尾' }),
    event(7, 'model_reasoning_delta', { turn: 1, content: 'Reason two' }),
    event(8, 'model_delta', { turn: 1, offset: Buffer.byteLength('开场🙂Middle结尾'), content: 'Done' }),
    event(9, 'model_result', { turn: 1 }),
  ]
  let live = events.reduce((outputs, item) => reduceMainOutputEvent(outputs, item), mergeMainOutputs([], []))
  const full = '开场🙂Middle结尾Done'
  const reply = { ...message('reply', 10, 'assistant', 1), content: full, outputBytes: Buffer.byteLength(full) }
  const index = live.flatMap(record => record.spans.map(span => ({ ...output(1, span.sequence, span.lastSequence),
    offset: span.offset, endOffset: span.endOffset, bytes: record.bytes, complete: true })))
  let restored = mergeMainOutputs([], index)
  restored = restored.map(record => hydrateMainOutput(record, { content: full, offset: 0, nextOffset: Buffer.byteLength(full), availableBytes: Buffer.byteLength(full), lastSequence: 8 }))
  const nodes = buildRunTrace(events, { runId: 'run', active: false }).nodes
  const chronological = (outputs: typeof live) => conversationTimeline(conversationItems([reply], outputs), {
    run: nodes.map(node => ({ key: node.key, sequence: node.sequence, lastSequence: node.lastSequence, estimate: 80 })),
  }, 0).map(row => row.message?.content ?? nodes.find(node => node.key === row.step)!.body)
  assert.deepEqual(chronological(live), ['开场🙂', 'Reason one', 'Middle', 'Child output', '结尾', 'Reason two', 'Done'])
  assert.deepEqual(chronological(restored), chronological(live))
  assert.deepEqual(conversationItems([reply], restored).map(row => row.displayKey), conversationItems([reply], live).map(row => row.displayKey))
  assert.equal(mainOutputText(restored[0]!), full)
  assert.equal(conversationItems([reply], restored).filter(row => !row.progress).length, 1)
})

test('a body snapshot ahead of SSE does not swallow later text boundaries or count its bytes twice', () => {
  let records = reduceMainOutputEvent([], event(2, 'model_delta', { turn: 1, offset: 0, content: 'A🙂' }))
  const full = 'A🙂BC'
  records[0] = hydrateMainOutput(records[0]!, { content: full, offset: 0, nextOffset: Buffer.byteLength(full), availableBytes: Buffer.byteLength(full), lastSequence: 7 })
  for (const item of [
    event(3, 'model_reasoning_delta', { turn: 1, content: 'Reason one' }),
    event(4, 'model_delta', { turn: 1, offset: 5, content: 'B' }),
    event(5, 'model_reasoning_delta', { turn: 1, content: 'Reason two' }),
    event(7, 'model_delta', { turn: 1, offset: 6, content: 'C' }),
  ]) records = reduceMainOutputEvent(records, item)
  const expected = records[0]!
  assert.equal(mainOutputText(expected), full)
  assert.equal(expected.bytes, Buffer.byteLength(full))
  assert.deepEqual(conversationItems([], records).map(row => row.content), ['A🙂', 'B', 'C'])
  const retained = structuredClone(records)
  records = mergeMainOutputs(records, [{ ...output(1, 2), offset: 0, endOffset: 5, bytes: 5 }])
  const bounds = (record: typeof expected) => record.spans.map(span => [span.sequence, span.lastSequence, span.offset, span.endOffset, !!span.closed])
  assert.deepEqual(bounds(records[0]!), bounds(retained[0]!))
  assert.equal(records[0]!.loaded, true)
  assert.deepEqual([expected], retained, 'Merging an older index does not mutate the live record')
})

test('late prefix metadata preserves newer live tail bytes and separate reasoning spans', () => {
  let records = reduceMainOutputEvent([], event(5, 'model_delta', { turn: 1, offset: 6, content: 'tail' }))
  records = reduceMainOutputEvent(records, event(7, 'model_delta', { turn: 1, offset: 10, content: '++' }))
  records = mergeMainOutputs(records, [
    { ...output(1, 2), offset: 0, endOffset: 6, bytes: 10 },
    { ...output(1, 5), offset: 6, endOffset: 10, bytes: 10 },
  ])
  assert.equal(records[0]!.bytes, 12)
  assert.equal(records[0]!.loaded, false)
  assert.deepEqual(records[0]!.spans.map(span => [span.sequence, span.lastSequence, span.offset, span.endOffset]), [[2, 2, 0, 6], [5, 7, 6, 12]])
  records[0] = hydrateMainOutput(records[0]!, { content: 'prefixtail', offset: 0, nextOffset: 10, availableBytes: 10, lastSequence: 5 })
  assert.equal(mainOutputText(records[0]!), 'prefixtail++')
  assert.deepEqual(conversationItems([], records).map(row => row.content), ['prefix', 'tail++'])
})

test('several index pages can describe one turn without losing spans or duplicating its footer', () => {
  const page = Array.from({ length: 107 }, (_, i) => ({ ...output(1, i * 2 + 2), offset: i, endOffset: i + 1, bytes: 107, complete: true }))
  let records = mergeMainOutputs([], page.slice(7))
  records = mergeMainOutputs(records, page.slice(0, 7))
  records = mergeMainOutputs(records, page.slice(7))
  assert.equal(records.length, 1)
  assert.equal(records[0]!.spans.length, 107)
  const full = 'x'.repeat(107)
  records[0] = hydrateMainOutput(records[0]!, { content: full, offset: 0, nextOffset: 107, availableBytes: 107, lastSequence: 214 })
  const reply = { ...message('reply', 216, 'assistant', 1), content: full, outputBytes: 107 }
  const rows = conversationItems([reply], records)
  assert.equal(rows.length, 107)
  assert.equal(new Set(rows.map(row => row.displayKey)).size, 107)
  assert.ok(rows.every(row => row.content === 'x'))
  assert.deepEqual(rows.filter(row => !row.progress).map(row => row.id), ['reply'])
})

test('main text survives more than 40 calls, 8 KiB, tool execution, cancellation and duplicate SSE', () => {
  let state = createConversationState()
  const text = '正文🙂'.repeat(2000)
  state = reduceConversationEvent(state, event(1, 'message', message('user', 1)))
  for (let turn = 1; turn <= 105; turn++) {
    const delta = event(turn * 3, 'model_delta', { turn, content: text })
    state = reduceConversationEvent(state, delta)
    assert.equal(reduceConversationEvent(state, delta), state)
    state = reduceConversationEvent(state, event(turn * 3 + 1, 'model_result', { turn, modelResult: { provider: 'test', id: 'model' } }))
  }
  state = reduceConversationEvent(state, event(400, 'tool', { tool: { name: 'shell' }, status: 'started' }))
  state = reduceConversationEvent(state, event(401, 'model_delta', { turn: 106, depth: 1, taskId: 'child', content: 'child' }))
  state = reduceConversationEvent(state, event(402, 'model_reasoning_delta', { turn: 107, content: 'reasoning' }))
  state = reduceConversationEvent(state, event(403, 'run_finished', { status: 'canceled' }))
  assert.equal(state.mainOutputs.length, 105)
  assert.ok(state.mainOutputs.every(record => record.complete && mainOutputText(record) === text))
  assert.ok(state.processEvents.some(event => event.kind === 'model_reasoning_delta'))
  assert.ok(state.mainOutputs.every(record => record.complete))
  assert.equal(state.messages.length, 1)
  assert.equal(state.modelResults.length, 105)
})

test('reasoning stays in process events and never creates or reorders message rows', () => {
  let records = mergeMainOutputs([], [output(1, 4), output(2, 8)])
  const messages = [message('user', 1), message('interjection', 3), message('final', 10, 'assistant', 2)]
  const before = conversationItems(messages, records)
  for (const taskId of ['main', 'child']) {
    records = reduceMainOutputEvent(records, event(2, 'model_reasoning_delta', { taskId, turn: 1, content: 'Checking. ' }))
    records = reduceMainOutputEvent(records, event(5, 'model_reasoning_delta', { taskId, turn: 1, content: 'Checked.' }))
  }
  const display = (rows: ReturnType<typeof conversationItems>) => rows.map(({ id, displayKey, displaySequence, role, content }) => ({ id, displayKey, displaySequence, role, content }))
  assert.deepEqual(display(conversationItems(messages, records)), display(before))
  assert.equal(records[0]!.spans[0]!.closed, true, 'Later reasoning closes the preceding text without creating another message')
  assert.deepEqual(before.map(row => row.id), ['user', 'interjection', 'output/run/main/1', 'final'])
  records = reduceMainOutputEvent(records, event(6, 'model_result', { turn: 1, modelResult: { provider: 'test', id: 'model' } }))
  assert.equal(records[0]!.complete, true)
})

test('final replies replace their original rows across interjections and several replies in one run', () => {
  const records = mergeMainOutputs([], [output(1, 3), output(2, 7), output(3, 11)])
  const before = conversationItems([message('user', 1), message('interjection', 4)], records)
  const messages = [message('user', 1), message('interjection', 4), message('reply1', 6, 'assistant', 1), message('reply2', 12, 'assistant', 3)]
  const after = conversationItems(messages, records)
  assert.deepEqual(after.map(row => row.id), ['user', 'reply1', 'interjection', 'output/run/main/2', 'reply2'])
  assert.equal(after[1].displayKey, before[1].displayKey)
  assert.equal(after.filter(row => row.progress).length, 1)
  assert.equal(after[1].sequence, 6, 'formal reply boundaries remain unchanged')
  assert.equal(after[1].displaySequence, 3)
  const restored = conversationItems(messages, mergeMainOutputs([], records.map(({ content, deltas, loaded, contentSequence, ...index }) => index)))
  assert.deepEqual(restored.map(row => row.id), after.map(row => row.id))
})

test('a fully streamed turn keeps its complete body when a final-message preview arrives', () => {
  const content = 'Complete streamed paragraph.\n'.repeat(600)
  let records = reduceMainOutputEvent([], event(2, 'model_delta', { turn: 1, content }))
  records = reduceMainOutputEvent(records, event(3, 'model_result', { turn: 1, modelResult: { provider: 'test', id: 'model' } }))
  const reply = { ...message('reply', 4, 'assistant', 1), content: content.slice(0, 200), outputBytes: Buffer.byteLength(content) }
  const rows = conversationItems([reply], records)
  assert.equal(rows.length, 1)
  assert.equal(rows[0]!.content, content)
  assert.equal(records[0]!.loaded, true)
})

test('a late index exposing an earlier missing prefix invalidates a streamed body', () => {
  let records = reduceMainOutputEvent([], event(5, 'model_delta', { turn: 1, content: 'tail' }))
  records = reduceMainOutputEvent(records, event(6, 'model_result', { turn: 1, modelResult: { provider: 'test', id: 'model' } }))
  assert.equal(records[0]!.loaded, true)
  records = mergeMainOutputs(records, [{ ...output(1, 2), lastSequence: 5, bytes: 10, complete: true }])
  assert.equal(records[0]!.loaded, false)
})

test('settled checklists keep their chronological position independently of the terminal reply and page cursor', () => {
  let state = createConversationState()
  const updatedAt = '2026-11-08T04:17:48Z'
  const finishedAt = '2026-11-08T04:18:06Z'
  state = reduceConversationEvent(state, event(1, 'message', message('user', 1)))
  state = reduceConversationEvent(state, { ...event(2, 'checklist', { items: [{ text: 'Review', status: 'done' }] }), createdAt: updatedAt })
  assert.equal(conversationItems(state.messages, state.mainOutputs).length, 1, 'Active checklists stay above the composer')
  state = reduceConversationEvent(state, event(3, 'message', message('followup', 3)))
  state = reduceConversationEvent(state, event(4, 'model_delta', { turn: 1, content: 'answer' }))
  state = reduceConversationEvent(state, { ...event(5, 'run_finished', {
    status: 'succeeded', message: { ...message('reply', 5, 'assistant', 1), createdAt: finishedAt },
  }), createdAt: finishedAt })
  const live = conversationItems(state.messages, state.mainOutputs)
  assert.deepEqual(live.map(row => row.id), ['user', 'checklist-summary/5', 'followup', 'reply'])
  const summary = live[1]!
  assert.equal(summary.createdAt, updatedAt)
  assert.equal(summary.displaySequence, 2)
  assert.equal(summary.sequence, 5, 'The paging cursor remains with the terminal reply')
  assert.equal(live.at(-1)!.displayKey, 'output/run/main/1', 'Streaming text retains its existing row')
  assert.equal(live.at(-1)!.checklist, undefined, 'The reply does not render another checklist')
  const restored = conversationItems(JSON.parse(JSON.stringify(state.messages)), mergeMainOutputs([], state.mainOutputs))
  assert.deepEqual(restored.map(row => [row.id, row.displaySequence, row.createdAt]), live.map(row => [row.id, row.displaySequence, row.createdAt]))
  assert.equal(replyTraceSegments(state.messages).has(summary.id), false, 'The display-only checklist does not take ownership of execution details')
})

test('unfinished checklists stay at run end and empty replies produce only one summary row', () => {
  for (const status of ['done', 'blocked', 'pending', 'in_progress'] as const) for (const content of ['', 'Answer']) {
    const reply: Message = { ...message(content ? 'reply' : 'checklist-summary/5', 5, 'assistant'), content,
      createdAt: '2026-11-08T04:18:06Z', checklist: { items: [{ text: 'Task', status }], status: 'interrupted',
        updatedAt: '2026-11-08T04:17:48Z', updatedSequence: 2 } }
    const rows = conversationItems([message('user', 1), message('followup', 3), reply], [])
    const settled = ['done', 'blocked'].includes(status)
    assert.deepEqual(rows.map(row => row.id), settled
      ? ['user', 'checklist-summary/5', 'followup', ...(content ? ['reply'] : [])]
      : ['user', 'followup', ...(content ? ['reply'] : []), 'checklist-summary/5'])
    assert.equal(rows.find(row => row.checklist)!.createdAt, settled ? reply.checklist!.updatedAt : reply.createdAt)
    assert.equal(rows.filter(row => row.checklist).length, 1)
  }
})

test('HTTP output snapshot and delayed SSE merge without repeated or missing text', () => {
  let records = mergeMainOutputs([], [{ ...output(1, 2, 5), bytes: 2 }])
  records = reduceMainOutputEvent(records, event(5, 'model_delta', { turn: 1, offset: 1, content: 'B' }))
  records = reduceMainOutputEvent(records, event(7, 'model_delta', { turn: 1, offset: 2, content: 'C' }))
  records[0] = hydrateMainOutput(records[0], { content: 'AB', offset: 0, nextOffset: 2, availableBytes: 2, lastSequence: 5 })
  assert.equal(mainOutputText(records[0]), 'ABC')
  assert.equal(records[0].loaded, true)
  records[0] = hydrateMainOutput(records[0], { content: 'ABCD', offset: 0, nextOffset: 4, availableBytes: 4, lastSequence: 9 })
  records = reduceMainOutputEvent(records, event(9, 'model_delta', { turn: 1, offset: 3, content: 'D' }))
  assert.equal(mainOutputText(records[0]), 'ABCD')
  records = mergeMainOutputs(records, [{ ...output(1, 2, 11), bytes: 5, complete: true }])
  assert.equal(records[0].loaded, false, 'new persisted bytes require hydration')
  records[0] = hydrateMainOutput(records[0], { content: 'ABCDE', offset: 0, nextOffset: 5, availableBytes: 5, lastSequence: 11 })
  records = mergeMainOutputs(records, [{ ...output(1, 2, 5), bytes: 2 }])
  assert.equal(mainOutputText(records[0]), 'ABCDE')
  assert.equal(records[0].complete, true, 'stale index cannot restart streaming')
})

test('terminal-only reasoning cannot move streamed text across a later interjection', () => {
  let records = mergeMainOutputs([], [output(1, 2)])
  records = reduceMainOutputEvent(records, event(4, 'model_reasoning_delta', { turn: 1, content: 'Terminal summary' }))
  const messages = [message('user', 1), message('interjection', 3)]
  assert.deepEqual(conversationItems(messages, records).map(row => row.id), ['user', 'output/run/main/1', 'interjection'])
  const finished = conversationItems([...messages, message('final', 6, 'assistant', 1)], records)
  assert.deepEqual(finished.map(row => row.id), ['user', 'final', 'interjection'])
  assert.equal(finished[1]!.sequence, 6, 'formal reply boundary remains unchanged')
})

test('completion-driven runs stream and keep model state without inventing user messages', () => {
 let state=createConversationState()
 state=reduceConversationEvent(state,event(1,'run_started',{ runtime:true }))
 state=reduceConversationEvent(state,event(2,'model_request',{turn:1, model:{provider:'test',id:'model'}}))
 state=reduceConversationEvent(state,event(3,'model_delta',{turn:1,content:'Background work completed'}))
 state=reduceConversationEvent(state,event(4,'model_result',{turn:1, modelResult:{provider:'test',id:'model'}}))
 state=pruneConversationWindow(state)
 assert.equal(state.messages.length,0)
 assert.equal(state.mainOutputs.length,1)
 assert.equal(state.modelResults.length,1)
 const rows=conversationItems(state.messages,state.mainOutputs)
 assert.equal(rows.length,1)
 assert.equal(rows[0]!.content,'Background work completed')
 state=reduceConversationEvent(state,event(5,'run_finished',{status:'succeeded',message:message('reply',5,'assistant',1)}))
 assert.equal(conversationItems(state.messages,state.mainOutputs).length,1)
 assert.equal(state.messages[0]!.role,'assistant')
})
