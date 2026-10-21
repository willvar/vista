import type { MainOutput, Message, ModelInfo, OutputView, StreamEvent } from '../client/types.ts'

export type MainOutputRecord = MainOutput & {
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
export function mergeMainOutputs(records: readonly MainOutputRecord[], page: readonly MainOutput[]): MainOutputRecord[] {
  const merged = new Map(records.map(output => [mainOutputKey(output), output]))
  for (const output of page) {
    const key = mainOutputKey(output)
    const previous = merged.get(key)
    merged.set(key, previous ? {
      ...previous, ...output,
      sequence: Math.min(previous.sequence, output.sequence),
      lastSequence: Math.max(previous.lastSequence, output.lastSequence),
      bytes: Math.max(previous.bytes, output.bytes),
      complete: previous.complete || output.complete,
      loaded: previous.loaded && output.lastSequence <= Math.max(previous.contentSequence, previous.deltas.at(-1)?.sequence || 0),
    } : { ...output, content: '', contentSequence: 0, loaded: false, deltas: [] })
  }
  return [...merged.values()].sort((a, b) => a.sequence - b.sequence)
}

export function reduceMainOutputEvent(records: readonly MainOutputRecord[], event: StreamEvent, model?: ModelInfo): MainOutputRecord[] {
  const data = event.data
  if (!data?.runId) return records as MainOutputRecord[]
  if (event.kind === 'run_finished') return records.map(output => output.runId === data.runId ? { ...output, complete: true } : output)
  if (data.depth || data.taskId && data.taskId !== 'main') return records as MainOutputRecord[]
  if (event.kind === 'model_result') return records.map(output => output.runId === data.runId && output.taskId === (data.taskId || 'main') && output.turn === data.turn ? {
    ...output, complete: true, provider: data.modelResult?.provider || output.provider, modelId: data.modelResult?.id || output.modelId,
  } : output)
  if (event.kind !== 'model_delta' || data.purpose || !(data.turn > 0) || typeof data.content !== 'string' || !data.content) return records as MainOutputRecord[]
  const key = mainOutputKey({ runId: data.runId, taskId: data.taskId || 'main', turn: data.turn })
  const previous = records.find(output => mainOutputKey(output) === key)
  if (previous && (event.sequence <= previous.contentSequence || previous.deltas.some(delta => delta.sequence === event.sequence))) return records as MainOutputRecord[]
  const output: MainOutputRecord = previous || {
    runId: data.runId, taskId: data.taskId || 'main', turn: data.turn, sequence: event.sequence, lastSequence: 0,
    createdAt: event.createdAt || '', provider: model?.provider, modelId: model?.id, modelKey: model?.key,
    bytes: 0, complete: false, content: '', contentSequence: 0, loaded: false, deltas: [],
  }
  const updated = { ...output, lastSequence: Math.max(output.lastSequence, event.sequence),
    bytes: output.bytes + (event.sequence > output.lastSequence ? new TextEncoder().encode(data.content).length : 0),
    deltas: [...output.deltas, { sequence: event.sequence, content: data.content }],
  }
  return previous ? records.map(item => item === previous ? updated : item) : [...records, updated]
}

// The HTTP snapshot can include tokens whose SSE events have not arrived yet,
// or be older than tokens received in flight. Reconcile both by event sequence.
export function hydrateMainOutput(record: MainOutputRecord, output: OutputView): MainOutputRecord {
  const through = output.lastSequence || 0
  if (through < record.contentSequence) return record
  const deltas = record.deltas.filter(delta => delta.sequence > through)
  return { ...record, content: output.content, contentSequence: through, deltas,
    bytes: output.availableBytes + deltas.reduce((total, delta) => total + new TextEncoder().encode(delta.content).length, 0),
    loaded: Math.max(through, deltas.at(-1)?.sequence || 0) >= record.lastSequence,
  }
}

export function conversationItems(messages: readonly Message[], outputs: readonly MainOutputRecord[], hasOlder = false, hasNewer = false): ConversationItem[] {
  const replies = new Set(messages.filter(message => message.role === 'assistant' && message.outputTurn).map(message => `${message.runId}/${message.outputTurn}`))
  const byTurn = new Map(outputs.map(output => [`${output.runId}/${output.turn}`, output]))
  const runs = new Set(messages.map(message => message.runId))
  const items: ConversationItem[] = messages.map(message => {
    const output = message.role === 'assistant' && message.outputTurn ? byTurn.get(`${message.runId}/${message.outputTurn}`) : undefined
    return { ...message, content: output?.loaded && message.outputBytes && output.bytes === message.outputBytes ? mainOutputText(output) : message.content,
      displayKey: output ? mainOutputKey(output) : message.id, displaySequence: output?.sequence ?? message.sequence }
  })
  for (const output of outputs) {
    if (!runs.has(output.runId) || replies.has(`${output.runId}/${output.turn}`)
      || hasOlder && output.sequence < (messages[0]?.sequence || 0)
      || hasNewer && output.sequence > (messages.at(-1)?.sequence || 0)) continue
    const key = mainOutputKey(output)
    items.push({ id: key, displayKey: key, displaySequence: output.sequence, runId: output.runId, role: 'assistant',
      sequence: output.sequence, createdAt: output.createdAt, provider: output.provider, modelId: output.modelId, modelKey: output.modelKey,
      content: mainOutputText(output), outputTurn: output.turn, outputBytes: output.bytes, mainOutput: output, progress: true })
  }
  return items.sort((a, b) => a.displaySequence - b.displaySequence)
}
