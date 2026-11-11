import assert from 'node:assert/strict'
import test from 'node:test'
import { computed, effectScope, nextTick, ref, watch } from 'vue'
import { useConversationHistory } from './useConversationHistory.ts'
import { conversationItems, hydrateMainOutput, mergeMainOutputs, type ConversationItem } from '../state/mainOutputs.ts'

const item = (sequence: number): ConversationItem => ({ id: String(sequence), displayKey: String(sequence), sequence, displaySequence: sequence, runId: String(sequence), role: 'assistant', content: 'text', createdAt: '' })
const settle = async () => { await nextTick(); await new Promise(resolve => setImmediate(resolve)) }
const deferred = () => { let resolve!: () => void; const promise = new Promise<void>(done => { resolve = done }); return { promise, resolve } }
function defaults(items = ref([item(1)])) {
  return { items, session: () => 'a', revision: ref(0), synchronized: () => true, paused: () => false,
    indexes: async (_runs: string[]) => {}, output: async (_item: ConversationItem) => {}, text: (row: ConversationItem) => row.content,
    traceKeys: () => [] as string[], hasOlder: () => false, older: async () => {} }
}

test('all fragments of one reply prepare from one shared body before releasing it', async () => {
  const scope = effectScope()
  const fragments = Array.from({ length: 6 }, (_, index) => String(index).repeat(10_000))
  const full = fragments.join('')
  const records = ref(mergeMainOutputs([], fragments.map((_, index) => ({ runId: 'run', taskId: 'main', turn: 1,
    sequence: index * 2 + 2, lastSequence: index * 2 + 2, createdAt: '', bytes: full.length, complete: true,
    offset: index * 10_000, endOffset: (index + 1) * 10_000 }))))
  const reply = { ...item(14), runId: 'run', outputTurn: 1, outputBytes: full.length, content: full.slice(0, 8192) }
  const source = computed(() => conversationItems([reply], records.value))
  let reads = 0, releases = 0
  const history = scope.run(() => useConversationHistory({ ...defaults(), items: source,
    output: async () => {
      reads++
      records.value = records.value.map(record => hydrateMainOutput(record, {
        content: full, offset: 0, nextOffset: full.length, availableBytes: full.length, lastSequence: 12,
      }))
    },
    release: () => {
      releases++
      records.value = records.value.map(record => ({ ...record, content: '', contentSequence: 0, deltas: [], loaded: false }))
    },
  }))!
  await settle()
  assert.equal(history.prepared.value, true)
  assert.deepEqual(history.items.value.map(row => row.content), fragments)
  assert.equal(reads, 1)
  assert.equal(releases, 1)
  assert.ok(records.value.every(record => !record.content && !record.loaded), 'The history owns the prepared fragments, without a second full-body cache')
  scope.stop()
})

test('history prepares indexes, full bodies and execution sources before exposing its first batch', async () => {
  const scope = effectScope(), index = deferred(), text = deferred()
  const source = ref(Array.from({ length: 80 }, (_, index) => ({ ...item(index + 1), outputTurn: index + 1, outputBytes: 9000 })))
  const reads: number[] = []
  const history = scope.run(() => useConversationHistory({ ...defaults(source),
    indexes: () => index.promise, output: async row => { reads.push(row.sequence); await text.promise }, traceKeys: () => ['execution'],
  }))!
  await settle()
  assert.equal(history.prepared.value, false)
  assert.equal(history.items.value.length, 0)
  assert.equal(reads.length, 0)
  index.resolve()
  await settle()
  assert.equal(reads.length, 4)
  text.resolve()
  await settle()
  assert.deepEqual(reads.sort((a, b) => a - b), Array.from({ length: 20 }, (_, index) => index + 61))
  assert.equal(history.prepared.value, false)
  history.traceReady('execution', true)
  await settle()
  assert.equal(history.prepared.value, true)
  assert.equal(history.items.value[0]!.sequence, 61)
  scope.stop()
})

test('earlier history extends the continuous list and retains its latest end', async () => {
  const scope = effectScope(), source = ref(Array.from({ length: 73 }, (_, index) => item(index + 1)))
  const history = scope.run(() => useConversationHistory(defaults(source)))!
  await settle()
  assert.equal(history.items.value[0]!.sequence, 54)
  await history.earlier()
  await settle()
  assert.equal(history.items.value[0]!.sequence, 34)
  assert.equal(history.items.value.at(-1)!.sequence, 73)
  assert.equal(history.items.value.length, 40)
  await history.earlier()
  await history.earlier()
  await settle()
  assert.deepEqual(history.items.value.map(row => row.sequence), source.value.map(row => row.sequence))
  assert.equal(history.hasEarlier.value, false)
  scope.stop()
})

test('old preparation cannot publish into a newly selected session', async () => {
  const scope = effectScope(), session = ref('a'), index = deferred(), source = ref([item(1)])
  const history = scope.run(() => useConversationHistory({ ...defaults(source), session: () => session.value,
    indexes: () => session.value === 'a' ? index.promise : Promise.resolve(),
  }))!
  await settle()
  session.value = 'b'
  source.value = [item(10)]
  await settle()
  assert.deepEqual(history.items.value.map(row => row.id), ['10'])
  index.resolve()
  await settle()
  assert.deepEqual(history.items.value.map(row => row.id), ['10'])
  scope.stop()
})

test('native drag stages older history, new replies and changed text without discarding them', async () => {
  const scope = effectScope(), paused = ref(false), source = ref(Array.from({ length: 40 }, (_, index) => item(index + 1)))
  const history = scope.run(() => useConversationHistory({ ...defaults(source), paused: () => paused.value }))!
  await settle()
  paused.value = true
  await history.earlier()
  source.value = [...source.value.map(row => row.id === '40' ? { ...row, content: 'complete streamed text' } : row), item(41)]
  await settle()
  assert.equal(history.items.value[0]!.sequence, 21)
  assert.equal(history.items.value.at(-1)!.content, 'text')
  assert.equal(history.prepared.value, true)
  paused.value = false
  await settle()
  assert.equal(history.items.value[0]!.sequence, 1)
  assert.equal(history.items.value.at(-1)!.sequence, 41)
  assert.equal(history.items.value.find(row => row.id === '40')!.content, 'complete streamed text')
  scope.stop()
})

test('scrolling near the top coalesces an outstanding history read into one bounded batch', async () => {
  const scope = effectScope(), gate = deferred(), source = ref([item(100)])
  let requests = 0
  const history = scope.run(() => useConversationHistory({ ...defaults(source), hasOlder: () => true,
    older: async () => { requests++; await gate.promise; source.value = [item(99), ...source.value] },
  }))!
  await settle()
  const first = history.earlier()
  void history.earlier()
  void history.earlier()
  await settle()
  assert.equal(requests, 1)
  assert.equal(history.prepending.value, true)
  gate.resolve()
  await first
  await settle()
  assert.deepEqual(history.items.value.map(row => row.id), ['99', '100'])
  assert.equal(history.prepending.value, false)
  scope.stop()
})

test('transport reconnect keeps displayed history readable while snapshot replacement prepares anew', async () => {
  const scope = effectScope(), synchronized = ref(true), revision = ref(0)
  const history = scope.run(() => useConversationHistory({ ...defaults(), revision, synchronized: () => synchronized.value }))!
  await settle()
  synchronized.value = false
  await settle()
  assert.equal(history.prepared.value, true)
  assert.equal(history.items.value.length, 1)
  synchronized.value = true
  await settle()
  revision.value++
  await settle()
  assert.equal(history.prepared.value, true)
  assert.equal(history.items.value.length, 1)
  scope.stop()
})

test('body eviction preserves all indexes and rehydrates only the newly visible body', async () => {
  const scope = effectScope()
  const source = ref(Array.from({ length: 80 }, (_, index) => ({ ...item(index + 1), outputTurn: index + 1, outputBytes: 300_000 })))
  const loaded = new Map<string, string>(), reads: string[] = []
  const history = scope.run(() => useConversationHistory({ ...defaults(source),
    output: async row => { reads.push(row.id); loaded.set(row.id, row.id + 'x'.repeat(150_000)) },
    text: row => loaded.get(row.id) ?? row.content,
    release: row => { loaded.delete(row.id) },
  }))!
  await settle()
  history.setVisible(['80'])
  await history.earlier()
  await history.earlier()
  await history.earlier()
  await settle()
  assert.equal(history.items.value.length, 80)
  assert.equal(loaded.size, 0, 'The source does not retain copies of full historical bodies')
  assert.ok(history.items.value.filter(row => row.content.length > 8192).length <= 32)
  assert.ok(history.items.value.reduce((sum, row) => sum + row.content.length * 2, 0) < 9 * 1024 * 1024)
  assert.equal(history.missing.value.has('80'), false, 'Visible bodies survive cache pressure')
  const target = history.items.value.find(row => history.missing.value.has(row.displayKey))!
  const before = reads.length
  history.setVisible([target.displayKey])
  await settle()
  assert.equal(reads.length, before + 1)
  assert.equal(reads.at(-1), target.id)
  assert.equal(history.missing.value.has(target.id), false)
  assert.ok(history.items.value.find(row => row.id === target.id)!.content.endsWith('x'.repeat(150_000)))
  scope.stop()
})

test('authoritative recovery prepares a replacement without discarding the browsed prefix', async () => {
  const scope = effectScope(), source = ref(Array.from({ length: 60 }, (_, index) => item(index + 1))), revision = ref(0)
  let gate = Promise.resolve()
  const history = scope.run(() => useConversationHistory({ ...defaults(source), revision, indexes: () => gate }))!
  await settle()
  await history.earlier()
  await settle()
  const retained = history.items.value
  const read = deferred()
  gate = read.promise
  source.value = [...source.value, item(61)]
  revision.value++
  await settle()
  assert.equal(history.prepared.value, true)
  assert.equal(history.items.value, retained)
  read.resolve()
  await settle()
  assert.equal(history.items.value[0]!.sequence, 21)
  assert.equal(history.items.value.at(-1)!.sequence, 61)
  scope.stop()
})

test('a final message reuses its already prepared complete streaming body', async () => {
  const scope = effectScope(), text = 'Complete streamed text. '.repeat(500)
  const record = { runId: 'run', taskId: 'main', turn: 1, sequence: 2, lastSequence: 5, createdAt: '', bytes: text.length,
    complete: true, content: text, contentSequence: 5, loaded: true, deltas: [],
    spans: [{ sequence: 2, lastSequence: 5, createdAt: '', offset: 0, endOffset: text.length, closed: true }] }
  const source = ref<ConversationItem[]>([{ ...item(2), id: 'output/run/main/1', displayKey: 'output/run/main/1', runId: 'run',
    outputTurn: 1, outputBytes: text.length, mainOutput: record, progress: true, content: text }])
  let reads = 0
  const history = scope.run(() => useConversationHistory({ ...defaults(source), output: async () => { reads++ } }))!
  await settle()
  source.value = [{ ...item(6), id: 'formal-reply', displayKey: 'output/run/main/1', displaySequence: 2, runId: 'run',
    outputTurn: 1, outputBytes: text.length, content: 'Short persisted preview.' }]
  await settle()
  assert.equal(reads, 1)
  assert.equal(history.items.value[0]!.content, text)
  assert.equal(history.items.value[0]!.progress, undefined)
  scope.stop()
})

test('visible keys and equivalent source projections preserve prepared rows without publishing again', async () => {
  const scope = effectScope(), source = ref(Array.from({ length: 1000 }, (_, index) => item(index + 1)))
  let texts = 0, reads = 0, publications = 0
  const history = scope.run(() => useConversationHistory({ ...defaults(source),
    output: async () => { reads++ }, text: row => { texts++; return row.content },
  }))!
  await settle()
  while (history.hasEarlier.value) { await history.earlier(); await settle() }
  const previous = history.items.value
  scope.run(() => watch(history.items, () => { publications++ }, { flush: 'sync' }))
  texts = 0
  history.setVisible(['998', '999', '1000'])
  await settle()
  assert.equal(history.items.value, previous)
  assert.equal(publications, 0)
  assert.equal(texts, 0)
  source.value = source.value.map(row => ({ ...row }))
  await settle()
  assert.equal(history.items.value, previous, 'Regenerated indexes with unchanged fields do not replace rows')
  source.value = [...source.value, item(1001)]
  await settle()
  assert.equal(publications, 1)
  assert.equal(texts, 1)
  assert.equal(reads, 0, 'Short messages never enter the output hydration queue')
  assert.equal(history.items.value.length, 1001)
  assert.ok(previous.every((row, index) => history.items.value[index] === row))
  scope.stop()
})

test('changed metadata and text still replace the affected row once', async () => {
  const scope = effectScope(), source = ref([item(1), item(2)])
  const history = scope.run(() => useConversationHistory(defaults(source)))!
  await settle()
  const first = history.items.value[0]
  source.value = [source.value[0]!, { ...source.value[1]!, content: 'corrected text', provider: 'identified-provider' }]
  await settle()
  assert.equal(history.items.value[0], first)
  assert.equal(history.items.value[1]!.content, 'corrected text')
  assert.equal(history.items.value[1]!.provider, 'identified-provider')
  scope.stop()
})
