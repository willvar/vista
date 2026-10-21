import assert from 'node:assert/strict'
import test from 'node:test'
import { effectScope, nextTick, reactive, ref } from 'vue'
import type { VistaClient } from '../client/vista.ts'
import type { RunOverview, StreamEvent } from '../client/types.ts'
import { useRunHistory, type RunHistoryCache } from './useRunHistory.ts'

const event = (sequence: number, kind = 'model_request', data: any = {}): StreamEvent => ({ sequence, kind, data: { runId: 'run', taskId: 'main', ...data } })
const settle = async () => { await nextTick(); await new Promise(resolve => setImmediate(resolve)) }
function client(request: VistaClient['request']): VistaClient { return { request, image: async () => new Blob(), streamEvents: async () => {} } }

test('collapsed runs fetch only an overview, count requests after its watermark once, and exclude summary calls', async () => {
  const paths: string[] = []
  const overview: RunOverview = { modelCalls: 257, throughSequence: 400, startedAt: '', events: [event(350), event(380)] }
  const options = reactive({ sessionId: 'session', runId: 'run', before: 502, first: 1, events: [event(380), event(401), event(401), event(402, 'model_request', { purpose: 'summary' })] })
  const scope = effectScope()
  const history = scope.run(() => useRunHistory(client(async <T>(path: string) => { paths.push(path); return overview as T }), options, ref(false)))!
  await settle()
  assert.equal(paths.length, 1)
  assert.ok(paths[0]!.endsWith('/runs/run/overview'))
  assert.equal(history.modelCalls.value, 258)
  assert.deepEqual(history.events.value.map(value => value.sequence), [350, 380, 401, 402])
  options.events.push(event(403, 'model_request', { taskId: 'child' }))
  assert.equal(history.modelCalls.value, 259)
  scope.stop()
})

test('reply sections request scoped overviews and reject live or snapshot events outside their boundaries', async () => {
  const paths: string[] = []
  const options = reactive({ sessionId: 'session', runId: 'run', after: 5, through: 10, before: 100, first: 1,
    events: [event(4), event(5), event(6), event(9), event(11), event(12, 'run_finished', { status: 'failed' })] })
  const scope = effectScope()
  const history = scope.run(() => useRunHistory(client(async <T>(path: string) => {
    paths.push(path)
    return { modelCalls: 1, firstSequence: 6, throughSequence: 8, startedAt: '2026-10-07T10:00:00Z', finishedAt: '2026-10-07T10:00:04Z',
      events: [event(1), event(7, 'checklist', { items: [{ text: 'Before followup', status: 'done' }] }), event(11, 'checklist', { items: [{ text: 'Future', status: 'in_progress' }] })] } as T
  }), options, ref(false)))!
  await settle()
  assert.deepEqual(paths, ['/sessions/session/runs/run/overview?after=5&through=10'])
  assert.deepEqual(history.events.value.map(value => value.sequence), [6, 7, 9])
  assert.equal(history.modelCalls.value, 2)
  assert.equal(history.overview.value?.startedAt, '2026-10-07T10:00:00Z')
  assert.equal(history.overview.value?.finishedAt, '2026-10-07T10:00:04Z')
  options.events.push(event(15), event(20, 'run_finished', { status: 'failed' }))
  assert.equal(history.modelCalls.value, 2)
  assert.equal(history.events.value.some(value => value.kind === 'run_finished'), false)
  scope.stop()
})

test('section pagination, hydrated outputs and caches stay inside the same reply range', async () => {
  const paths: string[] = []
  const rangeEvents = Array.from({ length: 350 }, (_, index) => event(index + 1, 'model_delta', { turn: 1, content: String(index + 1) }))
  const options = { sessionId: 'session', runId: 'run', after: 100, through: 250, before: 1000, first: 1, events: [] }
  const cache: RunHistoryCache = {}
  const request = client(async <T>(path: string) => {
    paths.push(path)
    if (path.includes('/overview')) return { modelCalls: 1, firstSequence: 101, throughSequence: 250, startedAt: '', finishedAt: '2026-10-07T10:00:04Z', events: [] } as T
    if (path.endsWith('/events/120')) return event(120, 'tool', { content: 'Scoped full output' }) as T
    const before = Number(new URL(path, 'http://vista.test').searchParams.get('before'))
    return rangeEvents.filter(value => value.sequence < before).slice(-100) as T
  })
  const scope = effectScope()
  const history = scope.run(() => useRunHistory(request, options, ref(true), cache))!
  await settle()
  assert.deepEqual(paths.filter(path => path.includes('event-page')).map(path => new URL(path, 'http://vista.test').searchParams.get('before')), ['251', '151'])
  assert.deepEqual(history.events.value.map(value => value.sequence), Array.from({ length: 150 }, (_, index) => index + 101))
  assert.equal(history.hasMore.value, false)
  await history.loadNode(100)
  await history.loadNode(251)
  assert.equal(paths.some(path => path.includes('/events/')), false)
  // Simulate a compact tool preview requiring individual hydration.
  history.loadedSequences.value.delete(120)
  await history.loadNode(120)
  assert.equal(history.events.value.find(value => value.sequence === 120)?.data.content, 'Scoped full output')
  assert.equal(cache.rangeAfter, 100)
  assert.equal(cache.rangeThrough, 250)
  assert.ok([...cache.events!.keys()].every(sequence => sequence > 100 && sequence <= 250))
  const requestsBeforeRemount = paths.length
  scope.stop()
  const remount = effectScope()
  const restored = remount.run(() => useRunHistory(request, options, ref(true), cache))!
  await settle()
  assert.equal(paths.length, requestsBeforeRemount)
  assert.equal(restored.events.value.length, 150)
  assert.equal(restored.events.value.find(value => value.sequence === 120)?.data.content, 'Scoped full output')
  remount.stop()
})

test('closing a live section boundary aborts its old snapshot and preserves only the completed section', async () => {
  let finishOld!: (value: RunOverview) => void
  const oldOverview = new Promise<RunOverview>(resolve => { finishOld = resolve })
  const options = reactive({ sessionId: 'session', runId: 'run', after: 0, through: undefined as number | undefined, before: 20, first: 1,
    events: [event(2), event(8), event(13)] })
  const cache: RunHistoryCache = {}
  let oldSignal: AbortSignal | null | undefined
  const scope = effectScope()
  const history = scope.run(() => useRunHistory(client(async <T>(path: string, request?: RequestInit) => {
    if (!path.includes('through=')) { oldSignal = request?.signal; return await oldOverview as T }
    return { modelCalls: 2, firstSequence: 1, throughSequence: 10, startedAt: '', finishedAt: '2026-10-07T10:00:04Z', events: [] } as T
  }), options, ref(false), cache))!
  await settle()
  options.through = 10
  await settle()
  assert.equal(oldSignal?.aborted, true)
  finishOld({ modelCalls: 90, throughSequence: 99, startedAt: '', events: [event(99, 'run_finished', { status: 'failed' })] })
  await settle()
  assert.deepEqual(history.events.value.map(value => value.sequence), [2, 8])
  assert.equal(history.modelCalls.value, 2)
  assert.equal(cache.rangeThrough, 10)
  scope.stop()
})

test('an adjacent tail section cannot reuse the previous reply snapshot or hydrated output', async () => {
  const cache: RunHistoryCache = { sessionId: 'session', runId: 'run', rangeAfter: 0, rangeThrough: 10,
    overview: { modelCalls: 99, throughSequence: 10, startedAt: '', events: [event(3)] },
    events: new Map([[4, event(4)]]), hydrated: [event(5)], loadedSequences: new Set([5]), hasMore: false }
  const paths: string[] = []
  const scope = effectScope()
  const history = scope.run(() => useRunHistory(client(async <T>(path: string) => {
    paths.push(path)
    return { modelCalls: 1, firstSequence: 11, throughSequence: 14, startedAt: '', events: [event(12)] } as T
  }), { sessionId: 'session', runId: 'run', after: 10, before: 20, first: 1, events: [event(14, 'run_finished', { status: 'succeeded' })] }, ref(false), cache))!
  await settle()
  assert.deepEqual(paths, ['/sessions/session/runs/run/overview?after=10'])
  assert.deepEqual(history.events.value.map(value => value.sequence), [12, 14])
  assert.equal(history.modelCalls.value, 1)
  assert.equal(history.loadedSequences.value.has(5), false)
  scope.stop()
})

test('expansion drains the whole run in compact pages once and live events never rescan old pages', async () => {
  const paths: string[] = []
  const overview: RunOverview = { modelCalls: 2, throughSequence: 351, startedAt: '', events: [event(340)] }
  const runEvents = Array.from({ length: 252 }, (_, index) => event(100 + index, index % 2 ? 'model_delta' : 'model_reasoning_delta', { turn: 1, content: String(index) }))
  const allEvents = [event(99, 'model_delta', { runId: 'older' }), ...runEvents, event(352, 'model_delta', { runId: 'newer' })]
  const options = reactive({ sessionId: 'session', runId: 'run', before: 10001, first: 100, events: [runEvents.at(-1)!] })
  const expanded = ref(false)
  const scope = effectScope()
  const history = scope.run(() => useRunHistory(client(async <T>(path: string) => {
    paths.push(path)
    if (path.endsWith('/overview')) return overview as T
    const query = new URL(path, 'http://vista.test').searchParams
    assert.equal(query.get('compact'), '1')
    return allEvents.filter(value => value.sequence < Number(query.get('before'))).slice(-100) as T
  }), options, expanded))!
  await settle()
  assert.equal(paths.length, 1)
  expanded.value = true
  await settle()
  assert.deepEqual(paths.slice(1).map(path => Number(new URL(path, 'http://vista.test').searchParams.get('before'))), [352, 252, 152])
  assert.equal(history.hasMore.value, false)
  assert.deepEqual(history.events.value.map(value => value.sequence), runEvents.map(value => value.sequence))
  assert.equal(history.events.value[240]?.data.content, '240')
  expanded.value = false
  await settle()
  options.events.push(event(353, 'model_delta', { turn: 2, content: 'new live output' }))
  expanded.value = true
  await settle()
  await history.load()
  assert.equal(paths.length, 4)
  assert.equal(history.events.value.at(-1)?.data.content, 'new live output')
  scope.stop()
})

test('late overview responses cannot leak across sessions', async () => {
  let complete!: (value: RunOverview) => void
  const old = new Promise<RunOverview>(resolve => { complete = resolve })
  const options = reactive({ sessionId: 'old', runId: 'run', before: 1, first: 1, events: [] as StreamEvent[] })
  const scope = effectScope()
  const history = scope.run(() => useRunHistory(client(async <T>(path: string) => (path.includes('/old/') ? await old : { modelCalls: 1, throughSequence: 5, startedAt: '', events: [] }) as T), options, ref(false)))!
  options.sessionId = 'new'
  await settle()
  complete({ modelCalls: 999, throughSequence: 1000, startedAt: '', events: [event(1000)] })
  await settle()
  assert.equal(history.overview.value?.modelCalls, 1)
  assert.equal(history.events.value.length, 0)
  scope.stop()
})

test('the first page of an older run starts at that run watermark instead of the latest session event', async () => {
  const paths: string[] = []
  const overview: RunOverview = { modelCalls: 2, throughSequence: 100, startedAt: '', events: [] }
  const expanded = ref(true)
  const scope = effectScope()
  scope.run(() => useRunHistory(client(async <T>(path: string) => { paths.push(path); return (path.endsWith('/overview') ? overview : []) as T }), { sessionId: 'session', runId: 'run', before: 10001, first: 1, events: [] }, expanded))!
  await settle()
  assert.equal(paths.length, 2)
  assert.ok(paths[1]!.endsWith('before=101'))
  scope.stop()
})

test('opening a compact step retrieves the original event once and overrides stale compact copies', async () => {
  const paths: string[] = []
  const compact = event(7, 'tool', { tool: { name: 'read_file', callId: 'read', truncated: true }, content: '{"content":"part' })
  const original = event(7, 'tool', { tool: { name: 'read_file', callId: 'read' }, content: '{"content":"complete file text"}' })
  let complete!: (value: StreamEvent) => void
  const pending = new Promise<StreamEvent>(resolve => { complete = resolve })
  const scope = effectScope()
  const history = scope.run(() => useRunHistory(client(async <T>(path: string) => {
    paths.push(path)
    return (path.endsWith('/overview') ? { modelCalls: 1, throughSequence: 7, startedAt: '', events: [compact] } : await pending) as T
  }), { sessionId: 'session', runId: 'run', before: 8, first: 1, events: [compact] }, ref(false)))!
  await settle()
  const first = history.loadNode(7)
  const second = history.loadNode(7)
  assert.equal(first, second)
  assert.equal(history.nodeLoading.value[7], true)
  complete(original)
  await first
  assert.equal(history.events.value[0]?.data.content, original.data.content)
  assert.equal(history.events.value[0]?.data.tool.truncated, undefined)
  await history.loadNode(7)
  assert.equal(paths.filter(path => path.endsWith('/events/7')).length, 1)
  assert.equal(paths.filter(path => path.includes('event-page')).length, 0)
  scope.stop()
})

test('step loading can retry a failure and discards an old session response', async () => {
  let attempts = 0
  let complete!: (value: StreamEvent) => void
  const pending = new Promise<StreamEvent>(resolve => { complete = resolve })
  const options = reactive({ sessionId: 'old', runId: 'run', before: 8, first: 1, events: [] as StreamEvent[] })
  const scope = effectScope()
  const history = scope.run(() => useRunHistory(client(async <T>(path: string) => {
    if (path.endsWith('/overview')) return { modelCalls: 1, throughSequence: 7, startedAt: '', events: [] } as T
    if (++attempts === 1) throw new Error('Temporarily unavailable')
    return await pending as T
  }), options, ref(false)))!
  await settle()
  await history.loadNode(7)
  assert.ok(history.nodeFailure.value[7])
  const retry = history.loadNode(7)
  assert.equal(history.nodeFailure.value[7], '')
  options.sessionId = 'new'
  await settle()
  complete(event(7, 'tool', { content: 'old session output' }))
  await retry
  assert.deepEqual(history.events.value, [])
  assert.equal(history.loadedSequences.value.has(7), false)
  scope.stop()
})

test('history failure keeps completed pages and retry resumes the failed cursor', async () => {
  const runEvents = Array.from({ length: 220 }, (_, index) => event(10 + index, 'model_delta', { turn: 1, content: String(index) }))
  const cursors: number[] = []
  let fail = true
  const scope = effectScope()
  const history = scope.run(() => useRunHistory(client(async <T>(path: string) => {
    if (path.endsWith('/overview')) return { modelCalls: 1, throughSequence: 229, startedAt: '', events: [] } as T
    const cursor = Number(new URL(path, 'http://vista.test').searchParams.get('before'))
    cursors.push(cursor)
    if (cursor === 130 && fail) { fail = false; throw new Error('Temporary failure') }
    return runEvents.filter(value => value.sequence < cursor).slice(-100) as T
  }), { sessionId: 'session', runId: 'run', before: 230, first: 10, events: [] }, ref(true)))!
  await settle()
  assert.ok(history.failure.value.includes('Temporary failure'))
  assert.equal(history.events.value.length, 100)
  assert.equal(history.hasMore.value, true)
  const first = history.load()
  assert.equal(history.load(), first)
  await first
  assert.deepEqual(cursors, [230, 130, 130, 30])
  assert.equal(history.failure.value, '')
  assert.equal(history.events.value.length, 220)
  assert.equal(history.hasMore.value, false)
  scope.stop()
})

test('the overview start includes earlier steps even when the loaded message window begins later', async () => {
  const runEvents = Array.from({ length: 421 }, (_, index) => event(100 + index, 'model_reasoning_delta', { turn: 1, content: String(index) }))
  const paths: string[] = []
  const scope = effectScope()
  const history = scope.run(() => useRunHistory(client(async <T>(path: string) => {
    paths.push(path)
    if (path.endsWith('/overview')) return { modelCalls: 1, firstSequence: 100, throughSequence: 520, startedAt: '', events: [] } as T
    const query = new URL(path, 'http://vista.test').searchParams
    return runEvents.filter(value => value.sequence < Number(query.get('before') || Infinity)).slice(-100) as T
  }), { sessionId: 'session', runId: 'run', before: 521, first: 500, events: [] }, ref(true)))!
  await settle()
  assert.equal(history.events.value.length, 421)
  assert.equal(history.events.value[0]?.sequence, 100)
  assert.equal(paths.filter(path => path.includes('event-page')).length, 5)
  scope.stop()
})

test('a missing watermark never traverses unrelated session runs and overview retry can resume', async () => {
  let attempts = 0
  let pages = 0
  const scope = effectScope()
  const history = scope.run(() => useRunHistory(client(async <T>(path: string) => {
    if (path.endsWith('/overview')) {
      if (++attempts === 1) throw new Error('Overview unavailable')
      return { modelCalls: 1, throughSequence: 9, startedAt: '', events: [] } as T
    }
    pages++
    assert.equal(new URL(path, 'http://vista.test').searchParams.get('before'), '10')
    return [event(9)] as T
  }), { sessionId: 'session', runId: 'run', before: 10001, first: 1, events: [] }, ref(true)))!
  await settle()
  assert.equal(pages, 0)
  assert.match(history.failure.value, /overview unavailable/)
  await history.load()
  assert.equal(attempts, 2)
  assert.equal(pages, 1)
  assert.equal(history.events.value.length, 1)
  assert.equal(history.failure.value, '')
  scope.stop()
})

test('nonadvancing run pages stop with a retryable error instead of looping', async () => {
  const page = Array.from({ length: 100 }, (_, index) => event(101 + index, 'model_delta', { turn: 1, content: 'reasoning' }))
  const scope = effectScope()
  let runPages = 0
  const history = scope.run(() => useRunHistory(client(async <T>(path: string) => {
    if (path.endsWith('/overview')) return { modelCalls: 1, throughSequence: 200, startedAt: '', events: [] } as T
    runPages++
    return page as T
  }), { sessionId: 'session', runId: 'run', before: 201, first: 1, events: [] }, ref(true)))!
  await settle()
  assert.equal(runPages, 2)
  assert.match(history.failure.value, /did not advance/)
  assert.equal(history.loading.value, false)
  assert.equal(history.events.value.length, 100)
  scope.stop()
})

test('collapsing pauses the next history page and reopening resumes the cursor', async () => {
  const page = Array.from({ length: 100 }, (_, index) => event(101 + index, 'model_delta', { turn: 1 }))
  let complete!: (value: StreamEvent[]) => void
  const pending = new Promise<StreamEvent[]>(resolve => { complete = resolve })
  const cursors: string[] = []
  const expanded = ref(true)
  const scope = effectScope()
  const history = scope.run(() => useRunHistory(client(async <T>(path: string) => {
    if (path.endsWith('/overview')) return { modelCalls: 1, throughSequence: 200, startedAt: '', events: [] } as T
    cursors.push(new URL(path, 'http://vista.test').searchParams.get('before')!)
    return (cursors.length === 1 ? await pending : [event(100)]) as T
  }), { sessionId: 'session', runId: 'run', before: 201, first: 100, events: [] }, expanded))!
  await settle()
  expanded.value = false
  await nextTick()
  complete(page)
  await settle()
  assert.deepEqual(cursors, ['201'])
  assert.equal(history.hasMore.value, true)
  expanded.value = true
  await settle()
  assert.deepEqual(cursors, ['201', '101'])
  assert.equal(history.hasMore.value, false)
  scope.stop()
})

test('compact pages keep tool originals lazy and never shorten a live original', async () => {
  const compact = event(7, 'tool', { tool: { name: 'read_file', callId: 'read', truncated: true }, content: 'preview' })
  const original = event(7, 'tool', { tool: { name: 'read_file', callId: 'read' }, content: 'complete file' })
  const live = event(8, 'tool', { tool: { name: 'read_file', callId: 'live' }, content: 'complete live file' })
  const paths: string[] = []
  const scope = effectScope()
  const history = scope.run(() => useRunHistory(client(async <T>(path: string) => {
    paths.push(path)
    if (path.endsWith('/overview')) return { modelCalls: 1, throughSequence: 8, startedAt: '', events: [compact] } as T
    if (path.includes('event-page')) return [compact, { ...live, data: { ...live.data, content: 'live preview', tool: { ...live.data.tool, truncated: true } } }] as T
    return original as T
  }), { sessionId: 'session', runId: 'run', before: 9, first: 1, events: [live] }, ref(true)))!
  await settle()
  assert.equal(history.loadedSequences.value.has(7), false)
  assert.equal(history.events.value.find(value => value.sequence === 8)?.data.content, 'complete live file')
  assert.equal(paths.filter(path => path.endsWith('/events/7')).length, 0)
  await history.loadNode(7)
  assert.equal(history.events.value.find(value => value.sequence === 7)?.data.content, 'complete file')
  assert.equal(history.loadedSequences.value.has(7), true)
  scope.stop()
})

test('late history pages are aborted and discarded on a run change', async () => {
  let finishPage!: (value: StreamEvent[]) => void
  const page = new Promise<StreamEvent[]>(resolve => { finishPage = resolve })
  const signals: AbortSignal[] = []
  const options = reactive({ sessionId: 'session', runId: 'run', before: 10, first: 1, events: [] as StreamEvent[] })
  const expanded = ref(true)
  const scope = effectScope()
  const history = scope.run(() => useRunHistory(client(async <T>(path: string, init?: RequestInit) => {
    if (path.endsWith('/overview')) return { modelCalls: 1, throughSequence: path.includes('/runs/new/') ? 0 : 9, startedAt: '', events: [] } as T
    signals.push(init!.signal!)
    return await page as T
  }), options, expanded))!
  await settle()
  options.runId = 'new'
  await settle()
  assert.equal(signals.length, 1)
  assert.ok(signals.every(signal => signal.aborted))
  finishPage([event(7)])
  await settle()
  assert.deepEqual(history.events.value, [])
  assert.equal(history.loading.value, false)
  assert.equal(history.loadedSequences.value.size, 0)
  scope.stop()
})

test('remount restores completed pages and hydrated tools without reloading, while unopened tools stay lazy', async () => {
  const cache: RunHistoryCache = {}
  const paths: string[] = []
  const compact = (sequence: number) => event(sequence, 'tool', { turn: 1, tool: { name: 'read_file', callId: `read-${sequence}`, truncated: true }, content: 'preview' })
  const originals = [event(7, 'tool', { turn: 1, tool: { name: 'read_file', callId: 'read-7' }, content: 'complete first output' }), event(8, 'tool', { turn: 1, tool: { name: 'read_file', callId: 'read-8' }, content: 'complete second output' })]
  const mock = client(async <T>(path: string) => {
    paths.push(path)
    if (path.endsWith('/overview')) return { modelCalls: 1, firstSequence: 1, throughSequence: 8, startedAt: '', finishedAt: '2026-01-01T12:00:00Z', events: [compact(7), compact(8)] } as T
    if (path.includes('event-page')) return [compact(7), compact(8)] as T
    return originals.find(value => path.endsWith(`/events/${value.sequence}`)) as T
  })
  const options = { sessionId: 'session', runId: 'run', before: 9, first: 1, events: [] }
  const firstScope = effectScope()
  const first = firstScope.run(() => useRunHistory(mock, options, ref(true), cache))!
  await settle()
  await first.load()
  await first.loadNode(7)
  firstScope.stop()
  const beforeRemount = paths.length
  const secondScope = effectScope()
  const second = secondScope.run(() => useRunHistory(mock, options, ref(true), cache))!
  await settle()
  await second.load()
  await second.loadNode(7)
  assert.equal(paths.length, beforeRemount)
  assert.equal(second.events.value.find(value => value.sequence === 7)?.data.content, 'complete first output')
  assert.equal(second.loadedSequences.value.has(8), false)
  await second.loadNode(8)
  assert.equal(paths.length, beforeRemount + 1)
  assert.ok(paths.at(-1)?.endsWith('/events/8'))
  assert.equal(second.events.value.find(value => value.sequence === 8)?.data.content, 'complete second output')
  secondScope.stop()
})

test('remount resumes an unfinished cursor and ignores the disposed instance late response', async () => {
  const cache: RunHistoryCache = {}
  const page = Array.from({ length: 100 }, (_, index) => event(101 + index, 'model_delta', { turn: 1, content: 'original' }))
  let finishOld!: (value: StreamEvent[]) => void
  const oldPage = new Promise<StreamEvent[]>(resolve => { finishOld = resolve })
  const cursors: number[] = []
  const signals: AbortSignal[] = []
  const mock = client(async <T>(path: string, init?: RequestInit) => {
    if (path.endsWith('/overview')) return { modelCalls: 1, firstSequence: 100, throughSequence: 200, startedAt: '', events: [] } as T
    const cursor = Number(new URL(path, 'http://vista.test').searchParams.get('before'))
    cursors.push(cursor)
    signals.push(init!.signal!)
    return (cursor === 201 ? page : cursors.length === 2 ? await oldPage : [event(100, 'model_delta', { turn: 1, content: 'fresh' })]) as T
  })
  const options = { sessionId: 'session', runId: 'run', before: 201, first: 100, events: [] }
  const firstScope = effectScope()
  const first = firstScope.run(() => useRunHistory(mock, options, ref(true), cache))!
  await settle()
  assert.deepEqual(cursors, [201, 101])
  assert.equal(first.events.value.length, 100)
  firstScope.stop()
  assert.equal(signals[1]?.aborted, true)
  const secondScope = effectScope()
  const second = secondScope.run(() => useRunHistory(mock, options, ref(true), cache))!
  await settle()
  assert.deepEqual(cursors, [201, 101, 101])
  assert.equal(second.events.value.length, 101)
  assert.equal(second.loading.value, false)
  finishOld([event(100, 'model_delta', { turn: 1, content: 'stale' })])
  await settle()
  assert.equal(second.events.value[0]?.data.content, 'fresh')
  assert.equal(cache.events?.get(100)?.data.content, 'fresh')
  assert.equal(cache.hasMore, false)
  secondScope.stop()
})

test('a cache getter selects the matching run snapshot without leaking the prior run', async () => {
  const caches: Record<string, RunHistoryCache> = { run: {}, next: {} }
  const options = reactive({ sessionId: 'session', runId: 'run', before: 10, first: 1, events: [] as StreamEvent[] })
  const paths: string[] = []
  const mock = client(async <T>(path: string) => {
    paths.push(path)
    if (path.endsWith('/overview')) return { modelCalls: 1, firstSequence: 1, throughSequence: 9, startedAt: '', finishedAt: '2026-01-01T12:00:00Z', events: [] } as T
    return [event(9, 'model_delta', { runId: options.runId, content: options.runId })] as T
  })
  const scope = effectScope()
  const history = scope.run(() => useRunHistory(mock, options, ref(true), () => caches[options.runId]!))!
  await settle()
  options.runId = 'next'
  await settle()
  assert.equal(history.events.value[0]?.data.content, 'next')
  assert.equal(caches.run!.events?.get(9)?.data.content, 'run')
  assert.equal(caches.next!.events?.get(9)?.data.content, 'next')
  const beforeReturn = paths.length
  options.runId = 'run'
  await settle()
  assert.equal(paths.length, beforeReturn)
  assert.equal(history.events.value[0]?.data.content, 'run')
  scope.stop()
})

test('remount refreshes an unfinished run and fills only its unseen gap after newer runs displaced live events', async () => {
  const cache: RunHistoryCache = {}
  const runEvents = Array.from({ length: 258 }, (_, index) => event(index + 1, 'model_delta', { turn: 1, content: String(index + 1) }))
  runEvents[257] = event(258, 'run_finished', { status: 'succeeded' })
  const cursors: number[] = []
  let overviewRequests = 0
  let beforeRequests = 0
  const mock = client(async <T>(path: string) => {
    if (path.endsWith('/overview')) {
      overviewRequests++
      return { modelCalls: 1, firstSequence: 1, throughSequence: overviewRequests === 1 ? 8 : 258, startedAt: '', finishedAt: overviewRequests === 1 ? undefined : '2026-01-01T12:00:00Z', events: [] } as T
    }
    const query = new URL(path, 'http://vista.test').searchParams
    assert.equal(query.get('compact'), '1')
    if (query.has('before')) { beforeRequests++; return runEvents.slice(0, 8) as T }
    const after = Number(query.get('after'))
    cursors.push(after)
    return runEvents.filter(value => value.sequence > after).slice(0, 100) as T
  })
  const options = reactive({ sessionId: 'session', runId: 'run', before: 9, first: 1, events: [] as StreamEvent[] })
  const firstScope = effectScope()
  firstScope.run(() => useRunHistory(mock, options, ref(true), cache))!
  await settle()
  firstScope.stop()
  // The external recent-event window no longer contains this run at all.
  options.before = 4001
  options.events = [event(4000, 'model_request', { runId: 'newer-run' })]
  const secondScope = effectScope()
  const second = secondScope.run(() => useRunHistory(mock, options, ref(true), cache))!
  await settle()
  assert.equal(overviewRequests, 2)
  assert.equal(beforeRequests, 1)
  assert.deepEqual(cursors, [8, 108, 208])
  assert.equal(second.events.value.length, 258)
  assert.equal(second.events.value.at(-1)?.kind, 'run_finished')
  assert.equal(second.failure.value, '')
  assert.equal(cache.throughSequence, 258)
  secondScope.stop()
  const thirdScope = effectScope()
  thirdScope.run(() => useRunHistory(mock, options, ref(true), cache))!
  await settle()
  assert.equal(overviewRequests, 2)
  assert.deepEqual(cursors, [8, 108, 208])
  thirdScope.stop()
})
