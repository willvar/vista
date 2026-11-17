import assert from 'node:assert/strict'
import test from 'node:test'
import { confirmInputEvent, projectInputQueue, reduceInputQueue } from './inputQueue.ts'
import { createConversationState, reduceConversationEvent } from './conversation.ts'
import type { StreamEvent } from '../client/types.ts'

function* permutations<T>(values: T[]): Generator<T[]> {
  if (!values.length) { yield []; return }
  for (let i = 0; i < values.length; i++) for (const rest of permutations(values.filter((_, index) => index !== i))) yield [values[i]!, ...rest]
}

test('all 120 ordered-stream and unordered-confirmation interleavings retain the latest known input state', () => {
  const events: Record<number, StreamEvent> = {
    2: { sequence: 2, kind: 'input_queued', data: { id: 'input', content: 'Accepted', status: 'queued' } },
    3: { sequence: 3, kind: 'input_updated', data: { id: 'input', content: 'Edited', status: 'queued' } },
    4: { sequence: 4, kind: 'input_status', data: { id: 'input', status: 'deleted' } },
  }
  let checked = 0
  for (const order of permutations(['s2', 's3', 's4', 'a2', 'a3', 'a4'])) {
    if (order.indexOf('s2') > order.indexOf('s3') || order.indexOf('s3') > order.indexOf('s4')) continue
    let state = { ...createConversationState(), lastEvent: 1 }, latest = 1, cursor = 1
    for (const action of order) {
      const sequence = Number(action[1]); latest = Math.max(latest, sequence)
      if (action[0] === 's') { state = reduceConversationEvent(state, events[sequence]!); cursor = sequence }
      else state.inputConfirmations = confirmInputEvent(state.inputConfirmations, events[sequence]!, state.lastEvent)
      assert.deepEqual(projectInputQueue(state).items.map(item => item.content), latest === 4 ? [] : [latest === 2 ? 'Accepted' : 'Edited'], order.join(','))
      assert.equal(state.lastEvent, cursor, 'HTTP must not advance conversation coverage')
    }
    assert.deepEqual(state.inputConfirmations, [], 'Retire confirmations as soon as stream coverage includes them')
    checked++
  }
  assert.equal(checked, 120)
})

test('a queue-wide resume confirmation remains effective through older pause records', () => {
  let state = { ...createConversationState(), lastEvent: 2, queuedInputs: [{ id: 'input', content: 'Keep', status: 'paused' }], inputQueuePaused: true }
  const resume: StreamEvent = { sequence: 4, kind: 'input_queue_state', data: { paused: false } }
  state.inputConfirmations = confirmInputEvent(state.inputConfirmations, resume, state.lastEvent)
  state = reduceConversationEvent(state, { sequence: 3, kind: 'input_status', data: { id: 'input', status: 'paused' } })
  assert.deepEqual(projectInputQueue(state), { items: [{ id: 'input', content: 'Keep', status: 'queued' }], paused: false })
  assert.equal(state.inputQueuePaused, true, 'Checkpoint proof uses canonical state, not a future confirmation')
  state = reduceConversationEvent(state, resume)
  assert.deepEqual(state.inputConfirmations, [])
  assert.equal(state.inputQueuePaused, false)
})

test('partial statuses preserve content and order while terminal inputs never occupy the projection', () => {
  const queue = { items: [{ id: 'a', content: 'First', status: 'queued', imageCount: 2 }, { id: 'b', content: 'Second', status: 'queued' }], paused: false }
  const paused = reduceInputQueue(queue, { sequence: 1, kind: 'input_status', data: { id: 'a', status: 'paused' } })
  assert.equal(paused.items[0]!.content, 'First'); assert.equal(paused.items[0]!.imageCount, 2)
  assert.equal(paused.paused, true)
  for (const status of ['deleted', 'withdrawn', 'delivered']) {
    const removed = reduceInputQueue(paused, { sequence: 2, kind: 'input_status', data: { id: 'a', status } })
    assert.deepEqual(removed.items.map(item => item.id), ['b']); assert.equal(removed.paused, false)
  }
  assert.deepEqual(reduceInputQueue(queue, { sequence: 2, kind: 'input_status', data: { id: 'unknown', status: 'paused' } }).items, queue.items)
})

test('confirmations deduplicate, reject malformed records and ignore already-covered operations', () => {
  const event: StreamEvent = { sequence: 3, kind: 'input_queued', data: { id: 'input', content: 'Once', status: 'queued' } }
  const pending = confirmInputEvent([], event, 2)
  assert.equal(confirmInputEvent(pending, event, 2), pending)
  assert.deepEqual(confirmInputEvent([], event, 3), [])
  assert.throws(() => confirmInputEvent([], { ...event, kind: 'message' }, 0), /Invalid input confirmation/)
  assert.throws(() => confirmInputEvent([], { ...event, sequence: NaN }, 0), /Invalid input confirmation/)
})
