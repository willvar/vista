import type { QueuedInput, StreamEvent } from '../types.js'

export type InputQueue = { items: QueuedInput[]; paused: boolean }
export type InputQueueSource = { queuedInputs: QueuedInput[]; inputQueuePaused: boolean; inputConfirmations: StreamEvent[] }
const kinds = new Set(['input_queued', 'input_updated', 'input_status', 'input_delivered', 'input_queue_state'])
export function isInputQueueEvent(event: StreamEvent) { return kinds.has(event.kind) }
export function inputPending(input: QueuedInput) { return ['queued', 'paused', 'delivering'].includes(input.status) }

// HTTP confirmations and durable events share these operations. Only the
// conversation stream owns coverage; projecting a confirmation never moves it.
export function reduceInputQueue(queue: InputQueue, event: StreamEvent): InputQueue {
  const data = event.data
  if (!isInputQueueEvent(event) || !data) return queue
  if (event.kind === 'input_queue_state') {
    return { items: data.paused ? queue.items : queue.items.map(item => item.status === 'paused' ? { ...item, status: 'queued' } : item), paused: !!data.paused }
  }
  if (!data.id) return queue
  let items = queue.items
  if (event.kind === 'input_delivered' || typeof data.status === 'string' && !inputPending(data)) {
    items = items.filter(item => item.id !== data.id)
  } else {
    const existing = items.some(item => item.id === data.id)
    items = existing ? items.map(item => item.id === data.id ? { ...item, ...data } : item)
      : typeof data.content === 'string' && inputPending(data) ? [...items, data as QueuedInput] : items
  }
  return { items, paused: items.some(item => item.status === 'paused') }
}

export function confirmInputEvent(confirmations: StreamEvent[], event: StreamEvent, through: number): StreamEvent[] {
  if (!event || !Number.isSafeInteger(event.sequence) || event.sequence <= 0 || !isInputQueueEvent(event) || !event.data || typeof event.data !== 'object') {
    throw new Error('Invalid input confirmation')
  }
  if (event.sequence <= through || confirmations.some(item => item.sequence === event.sequence)) return confirmations
  return [...confirmations, event].sort((a, b) => a.sequence - b.sequence)
}

export function projectInputQueue(source: InputQueueSource): InputQueue {
  return source.inputConfirmations.reduce(reduceInputQueue, { items: source.queuedInputs, paused: source.inputQueuePaused })
}
