import assert from 'node:assert/strict'
import test from 'node:test'
import { conversationTimeline } from './conversationTimeline.ts'
import type { ConversationItem } from './mainOutputs.ts'

test('messages and execution steps share event order and stable keys across prepends', () => {
  const message = (sequence: number): ConversationItem => ({ id: `m${sequence}`, displayKey: `m${sequence}`, sequence, displaySequence: sequence, runId: 'run', role: 'assistant', content: 'text', createdAt: '' })
  const traces = { run: [{ key: 'tool', sequence: 3, lastSequence: 5, estimate: 80 }] }
  const before = conversationTimeline([message(2), message(6)], traces, 2)
  const after = conversationTimeline([message(1), message(2), message(6)], traces, 1)
  assert.deepEqual(before.map(row => row.sequence), [2, 3, 6])
  assert.deepEqual(after.slice(1).map(row => row.key), before.map(row => row.key))
  assert.equal(conversationTimeline([message(6)], traces, 6).length, 1)
})
