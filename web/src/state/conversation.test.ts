import assert from 'node:assert/strict'
import test from 'node:test'
import { appendMessage, createConversationState, messageResults, runResults, reduceConversationEvent, replyTraceSegments } from './conversation.ts'
import type { Message, Session, StreamEvent } from '../client/types'

const session: Session = { id: 'session-a', title: 'Example', modelKey: '', open: true, running: true, executing: true, createdAt: '' }
const message = (id: string, sequence: number, runId: string, role = 'user'): Message => ({ id, sequence, runId, role, content: id, createdAt: '' })
const event = (sequence: number, kind: string, data: unknown): StreamEvent => ({ sequence, kind, data })

test('hundreds of completed runs release bodies and usage outside the message window', () => {
  let state = createConversationState(session)
  for (let index = 0; index < 320; index++) {
    const runId = `run-${index}`, seq = index * 10
    state = reduceConversationEvent(state, event(seq + 1, 'message', message(`user-${index}`, seq + 1, runId)))
    state = reduceConversationEvent(state, event(seq + 2, 'model_delta', { runId, turn: 1, content: 'x'.repeat(16 * 1024) }))
    state = reduceConversationEvent(state, event(seq + 3, 'model_result', { runId, turn: 1, modelResult: { provider: 'test', id: 'test', finishReason: 'stop' } }))
    state = reduceConversationEvent(state, event(seq + 4, 'run_finished', { runId, status: 'succeeded', message: { ...message(`reply-${index}`, seq + 4, runId, 'assistant'), outputTurn: 1 } }))
    state.modelOutput[`reply-${index}`] = { content: 'x'.repeat(16 * 1024), offset: 0, nextOffset: 16 * 1024, availableBytes: 16 * 1024 }
  }
  assert.equal(state.messages.length, 300)
  assert.equal(state.mainOutputs.length, 150)
  assert.equal(Object.keys(state.modelOutput).length, 150)
  assert.equal(state.modelResults.length, 150)
  const retained = new Set(state.messages.map(message => message.runId))
  assert.ok(state.mainOutputs.every(output => retained.has(output.runId)))
  assert.ok(Object.keys(state.finishedRuns).every(id => retained.has(id)))
})

test('reply trace scopes remain separate across steering, followups and off-page replies', () => {
  const question = message('question', 198, 'run')
  const steering = message('steering', 210, 'run')
  const firstReply = { ...message('reply', 251, 'run', 'assistant'), resultsAfter: 0 }
  const followup = message('price', 252, 'run')
  const lastReply = { ...message('price-reply', 307, 'run', 'assistant'), resultsAfter: 251 }
  const live = replyTraceSegments([question, steering], 'run')
  assert.equal(live.get(question.id)?.anchorId, question.id)
  assert.equal(live.get(steering.id), live.get(question.id))
  const continued = replyTraceSegments([question, steering, firstReply, followup], 'run')
  assert.equal(continued.get(question.id)?.key, live.get(question.id)?.key)
  assert.equal(continued.get(firstReply.id)?.through, 251)
  assert.equal(continued.get(firstReply.id)?.active, false)
  assert.equal(continued.get(followup.id)?.after, 251)
  assert.equal(continued.get(followup.id)?.active, true)
  const ended = replyTraceSegments([question, steering, firstReply, followup, lastReply])
  assert.equal(ended.get(lastReply.id)?.key, continued.get(followup.id)?.key)
  assert.equal(ended.get(lastReply.id)?.anchorId, followup.id)
  assert.equal(ended.get(lastReply.id)?.through, undefined)
  const page = replyTraceSegments([lastReply])
  assert.equal(page.get(lastReply.id)?.after, 251)
  assert.equal(page.get(lastReply.id)?.anchorId, lastReply.id)
  assert.equal(replyTraceSegments([firstReply], '', true).get(firstReply.id)?.through, 251)
})

test('event sequence replay cannot duplicate draft tokens or model usage', () => {
  const started = reduceConversationEvent(createConversationState(session), event(1, 'message', message('user-a', 1, 'run-a')))
  const delta = event(2, 'model_delta', { runId: 'run-a', turn: 1, content: 'Hi' })
  const state = reduceConversationEvent(started, delta)
  assert.equal(reduceConversationEvent(state, delta), state)
  assert.equal(state.modelDrafts[0].content, 'Hi')
  const result = { ...event(3, 'model_result', { runId: 'run-a', turn: 1, modelResult: { provider: 'openai-chat', id: 'example', finishReason: 'stop', durationMs: 125, usage: { inputTokens: 11, outputTokens: 2, cacheReadTokens: 0, reasoningTokens: 0 } } }), createdAt: '2026-10-11T01:00:00Z' }
  const completed = reduceConversationEvent(state, result)
  assert.equal(reduceConversationEvent(completed, result), completed)
  assert.equal(completed.modelResults.length, 1)
  assert.equal(completed.modelResults[0].durationMs, 125)
  assert.equal(completed.modelResults[0].createdAt, '2026-10-11T01:00:00Z')
})

test('late completion from an old run cannot finish or change pending questions of the new run', () => {
  let state = createConversationState(session)
  state = reduceConversationEvent(state, event(1, 'message', message('user-a', 1, 'run-a')))
  state = reduceConversationEvent(state, event(2, 'message', message('user-b', 2, 'run-b')))
  state = reduceConversationEvent(state, event(3, 'run_finished', { runId: 'run-a', status: 'succeeded', pendingReply: { runId: 'run-a', kind: 'question', content: 'Old question', questions: [{ question: 'Old question', options: [{ label: 'A' }] }] }, message: message('reply-a', 3, 'run-a', 'assistant') }))
  assert.equal(state.activeRunID, 'run-b')
  assert.deepEqual(state.runProgress, { runId: 'run-b', phase: 'starting' })
  assert.equal(state.selected?.executing, true)
  assert.equal(state.selected?.pendingReply, undefined)
  assert.equal(state.messages.at(-1)?.id, 'reply-a')
  assert.equal(state.finishedRuns['run-a'], true)
})

test('same numbered turns at different depths have independent drafts and completion', () => {
  let state = createConversationState(session)
  state = reduceConversationEvent(state, event(1, 'message', message('user-a', 1, 'run-a')))
  state = reduceConversationEvent(state, event(2, 'model_delta', { runId: 'run-a', turn: 1, depth: 0, content: 'root' }))
  state = reduceConversationEvent(state, event(3, 'model_delta', { runId: 'run-a', turn: 1, depth: 1, content: 'child' }))
  state = reduceConversationEvent(state, event(4, 'model_delta', { runId: 'run-a', turn: 1, depth: 0, content: ' reply' }))
  state = reduceConversationEvent(state, event(5, 'model_result', { runId: 'run-a', turn: 1, depth: 1, modelResult: { provider: 'openai-chat', id: 'child-model', finishReason: 'stop' } }))
  assert.deepEqual(state.modelDrafts.map(draft => [draft.depth, draft.content, draft.finished]), [[0, 'root reply', false], [1, 'child', true]])
  assert.deepEqual(state.runProgress, { runId: 'run-a', phase: 'response' })
  assert.equal(state.modelResults[0].depth, 1)
})

test('final reply replaces its root draft and late deltas do not recreate it', () => {
  let state = createConversationState(session)
  state = reduceConversationEvent(state, event(1, 'message', message('user-a', 1, 'run-a')))
  state = reduceConversationEvent(state, event(2, 'model_delta', { runId: 'run-a', turn: 1, depth: 0, content: 'root' }))
  state = reduceConversationEvent(state, event(3, 'model_delta', { runId: 'run-a', turn: 1, depth: 1, content: 'child' }))
  state = reduceConversationEvent(state, event(4, 'run_finished', { runId: 'run-a', status: 'succeeded', message: { ...message('reply-a', 4, 'run-a', 'assistant'), outputTurn: 1 } }))
  assert.deepEqual(state.modelDrafts.map(draft => [draft.depth, draft.finished]), [[1, true]])
  assert.equal(state.selected?.executing, false)
  assert.deepEqual(state.runProgress, { runId: 'run-a', phase: 'finished' })
  state = reduceConversationEvent(state, event(5, 'model_delta', { runId: 'run-a', turn: 1, content: 'late' }))
  assert.equal(state.modelDrafts.length, 1)
  assert.equal(state.modelDrafts[0].content, 'child')
})

test('stored and live usage join by event sequence only for the message run', () => {
  let state = createConversationState(session)
  const usage = { inputTokens: 100, outputTokens: 20, cacheReadTokens: 10, reasoningTokens: 3 }
  state = reduceConversationEvent(state, event(1, 'model_result', { runId: 'run-a', turn: 1, modelResult: { provider: 'openai-chat', id: 'model-a', finishReason: 'stop', usage, durationMs: 250 } }))
  state = reduceConversationEvent(state, event(2, 'model_result', { runId: 'run-b', turn: 1, modelResult: { provider: 'openai-chat', id: 'model-b', finishReason: 'stop' } }))
  const otherRun = state.modelResults[1]
  state = appendMessage(state, message('reply-a', 3, 'run-a', 'assistant'))
  assert.equal(state.messages[0].modelResults?.length, 1)
  const reply = { ...state.messages[0], modelResults: [...state.messages[0].modelResults!, { ...state.modelResults[0], sequence: 0, turn: 0 }, otherRun] }
  const results = messageResults(reply, state.modelResults)
  assert.deepEqual(results.map(result => result.sequence), [1])
  assert.deepEqual(results[0].usage, usage)
  assert.equal(results[0].durationMs, 250)
  assert.deepEqual(messageResults(message('user-a', 4, 'run-a'), state.modelResults), [])
})

test('stage assistant replies keep only the model results through their own sequence', () => {
  let state = createConversationState(session)
  const first = event(1, 'model_result', { runId: 'run-a', turn: 1, modelResult: { provider: 'openai-chat', id: 'model-a', finishReason: 'stop' } })
  const second = event(4, 'model_result', { runId: 'run-a', turn: 2, modelResult: { provider: 'openai-chat', id: 'model-a', finishReason: 'stop' } })
  state = reduceConversationEvent(state, first)
  state = reduceConversationEvent(state, event(2, 'message', { ...message('stage', 2, 'run-a', 'assistant'), outputTurn: 1, resultsAfter: 0 }))
  state = reduceConversationEvent(state, event(3, 'message', message('followup', 3, 'run-a')))
  state = reduceConversationEvent(state, second)
  state = reduceConversationEvent(state, event(5, 'run_finished', { runId: 'run-a', status: 'succeeded', message: { ...message('final', 5, 'run-a', 'assistant'), outputTurn: 2, resultsAfter: 2 } }))
  assert.deepEqual(state.messages.filter(item => item.role === 'assistant').map(item => item.modelResults?.map(result => result.turn)), [[1], [2]])
  assert.equal(state.modelDrafts.some(draft => draft.turn === 1), false)
})

test('historical user messages cannot replace the current run while loading an event page', () => {
  const initial = createConversationState(session)
  initial.messages = [message('user-new', 200, 'run-new')]
  initial.activeRunID = 'run-new'
  initial.activeRunSequence = 200
  let state = reduceConversationEvent(initial, event(100, 'message', message('user-old', 100, 'run-old')))
  state = reduceConversationEvent(state, event(150, 'run_finished', { runId: 'run-old', pendingReply: { runId: 'run-old', kind: 'question', content: 'Old question', questions: [{ question: 'Old question', options: [{ label: 'A' }] }] } }))
  assert.equal(state.activeRunID, 'run-new')
  assert.equal(state.selected?.pendingReply, undefined)
  assert.equal(state.selected?.executing, true)
})

test('run results include active subagents without adding usage to the user reply footer', () => {
  const user = message('user-a', 1, 'run-a')
  const result = { sequence: 2, runId: 'run-a', turn: 1, depth: 0, provider: 'openai-chat', id: 'model', finishReason: 'tool_calls' }
  const child = { ...result, sequence: 3, turn: 2, depth: 1 }
  const reply = { ...message('reply-a', 4, 'run-a', 'assistant'), modelResults: [result, child] }
  assert.equal(runResults('run-a', [user], [result, child]).length, 2)
  assert.deepEqual(runResults('run-a', [user, reply], [result, child, { ...result, sequence: 5, runId: 'other' }]), [result, child])
  assert.deepEqual(messageResults(user, [result, child]), [])
})

test('execution overview events retain task ownership, timestamps and output references', () => {
  let state = createConversationState(session)
  const data = { runId: 'run-a', taskId: 'child', parentTaskId: 'main', task: { id: 'child', parentId: 'main', title: 'Analyze images', status: 'started' } }
  state = reduceConversationEvent(state, { ...event(1, 'agent_task', data), createdAt: '2026-10-03T10:00:00Z' })
  state = reduceConversationEvent(state, event(2, 'model_request', { runId: 'run-a', taskId: 'child', depth: 1, provider: 'openai-chat', id: 'model' }))
  state = reduceConversationEvent(state, event(3, 'artifact', { id: 'artifact-a', runId: 'run-a', taskId: 'child', callId: 'child/shell-a' }))
  state = reduceConversationEvent(state, event(4, 'checklist', { runId: 'run-a', items: [{ text: 'Analyze images', status: 'in_progress' }] }))
  assert.deepEqual(state.processEvents.map(item => item.kind), ['agent_task', 'model_request', 'artifact', 'checklist'])
  assert.equal(state.processEvents[0].data.taskId, 'child')
  assert.equal(state.processEvents[0].createdAt, '2026-10-03T10:00:00Z')
  assert.equal(state.modelByRun['run-a'], undefined)
})


test('a new user message updates execution state before the next status poll', () => {
  const initial = createConversationState({ ...session, executing: false })
  const state = reduceConversationEvent(initial, event(1, 'message', message('user-a', 1, 'run-a')))
  assert.equal(state.selected?.executing, true)
  assert.deepEqual(state.runProgress, { runId: 'run-a', phase: 'starting' })
})

test('execution compaction preserves its summary without requiring a historical sequence', () => {
  let state = createConversationState(session)
  state = reduceConversationEvent(state, event(1, 'context_notice', { runId: 'run-a', message: 'Completed execution history was compacted', summary: 'Completed changes and unresolved work.', turn: 0, depth: 0 }))
  assert.equal(state.contextNotices[0].summary, 'Completed changes and unresolved work.')
  assert.equal(state.contextNotices[0].runId, 'run-a')
})

test('long live runs preserve cumulative usage through completion and reload', () => {
  let state = createConversationState(session)
  for (let turn = 1; turn <= 117; turn++) state = reduceConversationEvent(state, event(turn, 'model_result', {
    runId: 'long-run', turn, taskId: turn % 2 ? 'main' : 'child', depth: turn % 2 ? 0 : 1,
    modelResult: { provider: 'openai-chat', id: 'model', finishReason: 'tool_calls', durationMs: 1000,
      usage: { inputTokens: turn, outputTokens: 10, cacheReadTokens: 0, reasoningTokens: 0 } },
  }))
  const live = runResults('long-run', [], state.modelResults)
  assert.equal(live.length, 117)
  assert.equal(live.reduce((sum, result) => sum + result.usage!.inputTokens, 0), 6903)
  state = reduceConversationEvent(state, event(118, 'run_finished', {
    runId: 'long-run', status: 'succeeded', message: message('long-reply', 118, 'long-run', 'assistant'),
  }))
  assert.deepEqual(messageResults(state.messages[0], state.modelResults), live)
  assert.deepEqual(messageResults(state.messages[0], []), live)
})


test('partial input status events preserve content and ordering, and queue resume clears pause', () => {
  let state = createConversationState(session)
  state.queuedInputs = [{ id: 'first', mode: 'later', status: 'queued', content: 'Preserve me', imageCount: 1 }, { id: 'second', mode: 'now', status: 'queued', content: 'Next' }]
  state = reduceConversationEvent(state, { sequence: 1, kind: 'input_status', data: { id: 'first', status: 'paused' } })
  assert.deepEqual(state.queuedInputs[0], { id: 'first', mode: 'later', status: 'paused', content: 'Preserve me', imageCount: 1 })
  state = reduceConversationEvent(state, { sequence: 2, kind: 'input_queue_state', data: { paused: true } })
  assert.equal(state.selected?.inputQueuePaused, true)
  assert.equal(state.queuedInputs[1].status, 'queued')
  state = reduceConversationEvent(state, { sequence: 3, kind: 'input_queue_state', data: { paused: false } })
  assert.equal(state.queuedInputs[0].status, 'queued')
  assert.equal(state.selected?.inputQueuePaused, false)
  state = reduceConversationEvent(state, { sequence: 4, kind: 'input_delivered', data: { id: 'first', runId: 'run' } })
  assert.deepEqual(state.queuedInputs.map(item => item.id), ['second'])
})

test('actual reasoning survives request, live result and persisted reply without summary requests overwriting it', () => {
  let state = createConversationState(session)
  state = reduceConversationEvent(state, event(1, 'model_request', { runId: 'run-a', key: 'model-key', provider: 'openai-chat', id: 'model', reasoningEffort: 'high' }))
  state = reduceConversationEvent(state, event(2, 'model_request', { runId: 'run-a', key: 'model-key', provider: 'openai-chat', id: 'model', reasoningEffort: 'none', purpose: 'summary' }))
  assert.equal(state.modelByRun['run-a'].reasoningEffort, 'high')
  state = reduceConversationEvent(state, event(3, 'model_result', { runId: 'run-a', turn: 2, depth: 0, modelResult: { provider: 'openai-chat', id: 'model', finishReason: 'stop', reasoningEffort: 'high' } }))
  state = appendMessage(state, { ...message('reply-a', 4, 'run-a', 'assistant'), outputTurn: 2 })
  assert.equal(state.modelResults[0].reasoningEffort, 'high')
  assert.equal(state.messages[0].modelResults?.[0].reasoningEffort, 'high')
  const reloaded = { ...state.messages[0], modelResults: [{ ...state.modelResults[0], reasoningEffort: 'max' }] }
  assert.equal(messageResults(reloaded, [])[0].reasoningEffort, 'max')
})
