import type { MainOutput, Message, ModelInfo, OutputView, StreamEvent } from '../client/types.ts'
import { checklistSummaryPosition } from './checklist.ts'
import { isTimelineEvent } from './conversationTimeline.ts'

export type MainOutputSpan = { sequence: number; lastSequence: number; createdAt: string; offset: number; endOffset: number; closed?: boolean }
export type MainOutputRecord = MainOutput & {
  spans: MainOutputSpan[]
  content: string
  contentSequence: number
  loaded: boolean
  deltas: { sequence: number; content: string }[]
}
export type ConversationItem = Message & { displayKey: string; displaySequence: number; mainOutput?: MainOutputRecord; progress?: boolean }

export function mainOutputKey(output: Pick<MainOutput, 'runId' | 'taskId' | 'turn'>) {
  return `output/${output.runId}/${output.taskId || 'main'}/${output.turn}`
}
export function mainOutputText(output: MainOutputRecord) {
  return output.content + output.deltas.map(delta => delta.content).join('')
}
export function displayOutputKey(message: ConversationItem) {
  return message.mainOutput ? mainOutputKey(message.mainOutput) : message.id
}
const encoder = new TextEncoder()
const decoder = new TextDecoder()

export function mergeMainOutputs(records: readonly MainOutputRecord[], page: readonly MainOutput[]): MainOutputRecord[] {
  const merged = new Map(records.map(output => [mainOutputKey(output), output]))
  const turns = new Map<string, MainOutput[]>()
  for (const output of page) {
    const key = mainOutputKey(output), spans = turns.get(key) || []
    spans.push(output)
    turns.set(key, spans)
  }
  for (const [key, parts] of turns) {
    parts.sort((a, b) => a.sequence - b.sequence)
    const output = parts[0]!
    const previous = merged.get(key)
    const indexed = parts.flatMap((part): MainOutputSpan[] => {
      // Accept an already reduced record when restoring an in-memory snapshot.
      if ('spans' in part) return (part as MainOutputRecord).spans.map(span => ({ ...span }))
      return [{ sequence: part.sequence, lastSequence: part.lastSequence, createdAt: part.createdAt,
        offset: part.offset ?? 0, endOffset: part.endOffset ?? part.bytes, closed: part.complete }]
    })
    const first = indexed[0]!, tail = indexed.at(-1)!
    const before = previous?.spans.filter(span => span.lastSequence < first.sequence) || []
    const after = previous?.spans.filter(span => span.sequence > tail.lastSequence) || []
    for (const old of previous?.spans || []) {
      if (old.lastSequence < first.sequence || old.sequence > tail.lastSequence) continue
      // The index owns its covered range; an in-flight stream can extend its
      // last span beyond the HTTP snapshot.
      if (old.lastSequence >= tail.lastSequence) {
        if (old.lastSequence > tail.lastSequence) {
          tail.lastSequence = old.lastSequence
          tail.endOffset = old.endOffset
        }
        tail.closed ||= old.closed
      }
    }
    const spans = [...before, ...indexed, ...after]
    const complete = !!previous?.complete || parts.some(part => part.complete)
    for (let index = 0; index < spans.length; index++) spans[index] = { ...spans[index]!,
      closed: complete || index < spans.length - 1 || spans[index]!.closed }
    const bytes = parts.reduce((bytes, part) => Math.max(bytes, part.bytes), Math.max(previous?.bytes || 0, spans.at(-1)!.endOffset))
    const record = { content: '', contentSequence: 0, deltas: [], loaded: false, ...previous, ...output,
      sequence: spans[0]!.sequence, lastSequence: spans.at(-1)!.lastSequence, bytes, complete, spans }
    record.loaded = !!previous?.loaded && Math.max(previous.contentSequence, previous.deltas.at(-1)?.sequence || 0) >= record.lastSequence
      && encoder.encode(mainOutputText(previous)).length === bytes
    merged.set(key, record)
  }
  return [...merged.values()].sort((a, b) => a.sequence - b.sequence)
}

export function reduceMainOutputEvent(records: readonly MainOutputRecord[], event: StreamEvent, model?: ModelInfo): MainOutputRecord[] {
  const data = event.data
  if (!data?.runId) return records as MainOutputRecord[]
  const main = !data.depth && (!data.taskId || data.taskId === 'main')
  if (isTimelineEvent(event)) records = records.map(output => {
    const span = output.spans.at(-1)
    if (!span || span.closed || span.lastSequence >= event.sequence
      || event.kind === 'model_delta' && main && output.runId === data.runId && output.turn === data.turn) return output
    return { ...output, spans: output.spans.map(part => part === span ? { ...part, closed: true } : part) }
  })
  if (event.kind === 'run_finished') return records.map(output => output.runId === data.runId ? { ...output, complete: true, spans: output.spans.map(span => ({ ...span, closed: true })) } : output)
  if (data.depth || data.taskId && data.taskId !== 'main') return records as MainOutputRecord[]
  if (event.kind === 'model_result') return records.map(output => output.runId === data.runId && output.taskId === (data.taskId || 'main') && output.turn === data.turn ? {
    ...output, complete: true, spans: output.spans.map(span => ({ ...span, closed: true })), provider: data.modelResult?.provider || output.provider, modelId: data.modelResult?.id || output.modelId,
    // A fully received live turn is already its full text. Keep it when the
    // durable final-message preview replaces the streaming row.
    loaded: output.loaded || output.spans[0]?.offset === 0 && encoder.encode(mainOutputText(output)).length === output.bytes,
  } : output)
  if (event.kind !== 'model_delta' || data.purpose || !(data.turn > 0) || typeof data.content !== 'string' || !data.content) return records as MainOutputRecord[]
  const key = mainOutputKey({ runId: data.runId, taskId: data.taskId || 'main', turn: data.turn })
  const previous = records.find(output => mainOutputKey(output) === key)
  if (previous && event.sequence <= previous.lastSequence && (event.sequence <= previous.contentSequence || previous.deltas.some(delta => delta.sequence === event.sequence))) return records as MainOutputRecord[]
  const output: MainOutputRecord = previous || {
    runId: data.runId, taskId: data.taskId || 'main', turn: data.turn, sequence: event.sequence, lastSequence: 0,
    createdAt: event.createdAt || '', provider: model?.provider, modelId: model?.id, modelKey: model?.key,
    bytes: 0, complete: false, spans: [], content: '', contentSequence: 0, loaded: false, deltas: [],
  }
  const bytes = encoder.encode(data.content).length
  const spans = output.spans.slice()
  const span = spans.at(-1)
  const offset = typeof data.offset === 'number' ? data.offset : span?.endOffset || 0
  if (event.sequence > output.lastSequence) {
    if (!span || span.closed) spans.push({ sequence: event.sequence, lastSequence: event.sequence,
      createdAt: event.createdAt || '', offset, endOffset: offset + bytes })
    else spans[spans.length - 1] = { ...span, lastSequence: event.sequence, endOffset: offset + bytes }
  }
  const updated = { ...output, lastSequence: Math.max(output.lastSequence, event.sequence),
    bytes: event.sequence > output.lastSequence ? Math.max(output.bytes, offset + bytes) : output.bytes, spans,
    deltas: event.sequence <= output.contentSequence ? output.deltas : [...output.deltas, { sequence: event.sequence, content: data.content }],
  }
  return previous ? records.map(item => item === previous ? updated : item) : [...records, updated]
}

// The HTTP snapshot can include tokens whose SSE events have not arrived yet,
// or be older than tokens received in flight. Reconcile both by event sequence.
export function hydrateMainOutput(record: MainOutputRecord, output: OutputView): MainOutputRecord {
  const through = output.lastSequence || 0
  if (through < record.contentSequence) return record
  const deltas = record.deltas.filter(delta => delta.sequence > through)
  const bytes = output.availableBytes + deltas.reduce((total, delta) => total + encoder.encode(delta.content).length, 0)
  return { ...record, content: output.content, contentSequence: through, deltas,
    bytes: Math.max(record.bytes, bytes),
    loaded: bytes >= record.bytes && Math.max(through, deltas.at(-1)?.sequence || 0) >= record.lastSequence,
  }
}

export function conversationItems(messages: readonly Message[], outputs: readonly MainOutputRecord[], hasOlder = false, hasNewer = false): ConversationItem[] {
  const replies = new Set(messages.filter(message => message.role === 'assistant' && message.outputTurn).map(message => `${message.runId}/${message.outputTurn}`))
  const byTurn = new Map(outputs.map(output => [`${output.runId}/${output.turn}`, output]))
  function outputRows(output: MainOutputRecord, reply?: Message): ConversationItem[] {
    const full = output.loaded || !reply ? mainOutputText(output) : reply.content
    const bytes = encoder.encode(full)
    return output.spans.map((span, index) => {
      const terminal = !!reply && index === output.spans.length - 1
      const key = mainOutputKey(output) + (span.offset ? `/part/${span.sequence}` : '')
      const content = output.spans.length === 1 ? full : decoder.decode(bytes.subarray(span.offset, span.endOffset))
      return { ...(terminal ? reply : { id: key, runId: output.runId, role: 'assistant', sequence: span.sequence,
        createdAt: span.createdAt, provider: output.provider, modelId: output.modelId, modelKey: output.modelKey, outputTurn: output.turn }),
        displayKey: key, displaySequence: span.sequence, content,
        outputBytes: terminal ? reply!.outputBytes : span.endOffset - span.offset,
        mainOutput: terminal && output.spans.length === 1 ? undefined : { ...output, complete: output.complete || !!span.closed, lastSequence: span.lastSequence },
        progress: !terminal }
    })
  }
  const items: ConversationItem[] = messages.flatMap(message => {
    const output = message.role === 'assistant' && message.outputTurn ? byTurn.get(`${message.runId}/${message.outputTurn}`) : undefined
    const { checklist, ...reply } = message
    const rows: ConversationItem[] = checklist && !message.content ? [] : output ? outputRows(output, reply) : [{
      ...reply, content: message.content, displayKey: message.id, displaySequence: message.sequence,
    }]
    if (checklist) {
      // The page cursor stays with the terminal reply. Only the presentation
      // row moves to the last checklist update, without changing model history.
      const position = checklistSummaryPosition(message)
      const id = `checklist-summary/${message.sequence}`
      rows.push({ id, displayKey: id, displaySequence: position.sequence, runId: message.runId,
        sequence: message.sequence, role: 'assistant', content: '', createdAt: position.createdAt, checklist })
    }
    return rows
  })
  for (const output of outputs) {
    if (replies.has(`${output.runId}/${output.turn}`)
      || hasOlder && output.sequence < (messages[0]?.sequence || 0)
      || hasNewer && output.sequence > (messages.at(-1)?.sequence || 0)) continue
    items.push(...outputRows(output))
  }
  return items.sort((a, b) => a.displaySequence - b.displaySequence)
}
