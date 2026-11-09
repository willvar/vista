import assert from 'node:assert/strict'
import test from 'node:test'
import type { MainOutput, Message, StreamEvent } from '../client/types.ts'
import { createConversationState, pruneConversationWindow, reduceConversationEvent, replyTraceSegments } from './conversation.ts'
import { conversationItems, hydrateMainOutput, mainOutputText, mergeMainOutputs, reduceMainOutputEvent } from './mainOutputs.ts'

const event = (sequence: number, kind: string, data: object): StreamEvent => ({ sequence, kind, createdAt: '2026-10-08T00:00:00Z', data: { runId: 'run', taskId: 'main', ...data } })
const message = (id: string, sequence: number, role = 'user', outputTurn?: number): Message => ({ id, runId: 'run', sequence, role, outputTurn, createdAt: '', content: id })
const output = (turn = 1, sequence = 2, lastSequence = sequence): MainOutput => ({ runId: 'run', taskId: 'main', turn, sequence, lastSequence, bytes: 3, createdAt: '', complete: false })

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
  assert.deepEqual(conversationItems(messages, records), before)
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
  let records = mergeMainOutputs([], [output(1, 2, 5)])
  records = reduceMainOutputEvent(records, event(5, 'model_delta', { turn: 1, content: 'B' }))
  records = reduceMainOutputEvent(records, event(7, 'model_delta', { turn: 1, content: 'C' }))
  records[0] = hydrateMainOutput(records[0], { content: 'AB', offset: 0, nextOffset: 2, availableBytes: 2, lastSequence: 5 })
  assert.equal(mainOutputText(records[0]), 'ABC')
  assert.equal(records[0].loaded, true)
  records[0] = hydrateMainOutput(records[0], { content: 'ABCD', offset: 0, nextOffset: 4, availableBytes: 4, lastSequence: 9 })
  records = reduceMainOutputEvent(records, event(9, 'model_delta', { turn: 1, content: 'D' }))
  assert.equal(mainOutputText(records[0]), 'ABCD')
  records = mergeMainOutputs(records, [{ ...output(1, 2, 11), complete: true }])
  assert.equal(records[0].loaded, false, 'new persisted bytes require hydration')
  records[0] = hydrateMainOutput(records[0], { content: 'ABCDE', offset: 0, nextOffset: 5, availableBytes: 5, lastSequence: 11 })
  records = mergeMainOutputs(records, [output(1, 2, 5)])
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
