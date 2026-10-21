import assert from 'node:assert/strict'
import test from 'node:test'
import type { MainOutput, Message, StreamEvent } from '../client/types.ts'
import { createConversationState, reduceConversationEvent } from './conversation.ts'
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
