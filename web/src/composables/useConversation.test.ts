import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { effectScope, nextTick, ref } from 'vue'
import { useConversation, welcomeDraftStorageKey, type ConversationOptions } from './useConversation.ts'
import { useInteractionPreferences } from './useInteractionPreferences.ts'
import { quoteStoragePrefix } from '../state/quotedReply.ts'
import type { VistaClient } from '../client/vista'
import type { EventStreamOptions } from '../client/sse'
import type { Artifact, Job, CompactionStatus, Message, ModelCatalog, OutputView, PendingReply, Session, StreamEvent } from '../client/types'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (failure: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
const session = (id: string): Session => ({ id, title: id, modelKey: 'example', open: true, running: true, executing: false, createdAt: '' })
const message = (id: string, sequence: number, runId: string, role = 'user'): Message => ({ id, sequence, runId, role, content: id, createdAt: '' })
const questionPending = (runId = 'question-run'): PendingReply => ({ runId, kind: 'question', content: 'Pick a choice', questions: [{ question: 'Pick a choice', options: [{ label: 'A' }, { label: 'B' }] }] })

test('returning to an evicted window reloads indexes and ignores earlier in-flight bodies', async t => {
  const old = deferred<OutputView>(), fresh = deferred<OutputView>()
  let indexes = 0, bodies = 0
  const { conversation } = fixture(t, path => {
    if (path.includes('/runs/run-a/main-outputs')) { indexes++; return Promise.resolve([]) }
    if (path.includes('/runs/run-a/model-output?')) return ++bodies === 1 ? old.promise : fresh.promise
  })
  await conversation.selectSession(session('a'))
  const reply: Message = { ...message('reply-a', 2, 'run-a', 'assistant'), outputTurn: 1, outputBytes: 12000 }
  conversation.messages.value = [reply]
  await nextTick()
  const first = conversation.readModelOutput(reply)
  conversation.messages.value = [message('off-window', 3, 'elsewhere')]
  await nextTick()
  conversation.messages.value = [reply]
  await nextTick()
  const second = conversation.readModelOutput(reply)
  old.resolve({ content: 'stale', offset: 0, nextOffset: 5, availableBytes: 5 })
  await first
  assert.equal(conversation.modelOutput.value[reply.id], undefined)
  assert.equal(conversation.modelOutputLoading.value[reply.id], true)
  const text = 'complete body\n'.repeat(2000)
  fresh.resolve({ content: text, offset: 0, nextOffset: text.length, availableBytes: text.length })
  await second
  assert.equal(indexes, 2)
  assert.equal(bodies, 2)
  assert.equal(conversation.modelOutput.value[reply.id]?.content, text)
})

function fixture(t: TestContext, response?: (path: string, options?: RequestInit) => Promise<unknown> | undefined, onSessionStarted?: ConversationOptions['onSessionStarted']) {
  const saved = new Map([['vista-session-draft:a', 'draft A'], ['vista-session-draft:b', 'draft B']])
  const previousStorage = Object.getOwnPropertyDescriptor(globalThis, 'localStorage')
  const storage: Storage = {
    get length() { return saved.size },
    clear: () => saved.clear(),
    getItem: key => saved.get(key) ?? null,
    key: index => [...saved.keys()][index] ?? null,
    removeItem: key => { saved.delete(key) },
    setItem: (key, value) => { saved.set(key, value) },
  }
  Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: storage })
  t.after(() => {
    if (previousStorage) Object.defineProperty(globalThis, 'localStorage', previousStorage)
    else Reflect.deleteProperty(globalThis, 'localStorage')
  })
  const sessions = ref([session('a'), session('b')])
  const modelCatalog = ref<ModelCatalog>({ default: 'example', models: [{ key: 'example', provider: 'openai-chat', id: 'model', reasoningOptions: ['low', 'high'] }] })
  const error = ref('')
  const busy = ref(false)
  let refreshes = 0
  const streams = new Map<string, EventStreamOptions>()
  const client: VistaClient = {
    async request<T>(path: string, options?: RequestInit): Promise<T> {
      const custom = response?.(path, options)
      if (custom) return await custom as T
      if (path === '/sessions') return sessions.value as T
      if (path.endsWith('/snapshot')) {
        const base = path.slice(0, -'/snapshot'.length)
        const [messages, events] = await Promise.all([client.request<Message[]>(`${base}/messages`), client.request<StreamEvent[]>(`${base}/event-page`)])
        return { session: { ...conversation.selected.value, latestReplySequence: Math.max(0, ...messages.filter(message => message.role === 'assistant').map(message => message.sequence)) },
          throughSequence: Math.max(0, ...messages.map(message => message.sequence), ...events.map(event => event.sequence)),
          messages, events, hasOlder: messages.length === 100, hasOlderEvents: events.length === 100 } as T
      }
      if (path.endsWith('/context')) return null as T
      if (path.endsWith('/messages')) return [message(path.split('/')[2] + '-history', 1, 'history', 'assistant')] as T
      return [] as T
    },
    async image() { throw new Error('Images are not requested in this fixture') },
    async streamEvents(id, options) {
      streams.set(id, options)
      await options.beforeConnect?.()
      if (!options.signal.aborted) await options.onCheckpoint?.({ throughSequence: conversation.lastEvent.value, session: conversation.selected.value! })
    },
  }
  const scope = effectScope()
  const preferences = scope.run(() => useInteractionPreferences())!
  const conversation = scope.run(() => useConversation({ client, sessions, modelCatalog, error, busy, refresh: async () => { refreshes++ }, translate: key => key, interactionPreferences: preferences, onSessionStarted }))!
  t.after(() => scope.stop())
  return { conversation, sessions, modelCatalog, error, busy, saved, streams, preferences, client, refreshes: () => refreshes }
}

test('omission from the open-only workspace list preserves the selected conversation and draft', async t => {
  const f = fixture(t)
  await f.conversation.selectSession(session('a'))
  f.conversation.draft.value = 'Keep this draft while another client closes the conversation'
  const messages = [...f.conversation.messages.value]
  f.sessions.value = []
  assert.equal(f.conversation.selected.value?.id, 'a')
  assert.equal(f.conversation.draft.value, 'Keep this draft while another client closes the conversation')
  assert.deepEqual(f.conversation.messages.value, messages)
  // The workspace owner supplies independently verified close/reopen state.
  f.conversation.selected.value = { ...f.conversation.selected.value!, open: false }
  f.sessions.value = [session('a')]
  assert.equal(f.conversation.selected.value?.open, true)
  assert.equal(f.conversation.draft.value, 'Keep this draft while another client closes the conversation')
})

test('successful sends apply acknowledgements without workspace reloads or skipping stream records', async t => {
  const sent = message('new-user', 2, 'new-run')
  const f = fixture(t, (path, options) => path.endsWith('/messages') && options?.method === 'POST'
    ? Promise.resolve({ runId: sent.runId, message: sent }) : undefined)
  await f.conversation.selectSession(session('a'))
  const cursor = f.conversation.lastEvent.value
  f.conversation.draft.value = 'New message'
  await f.conversation.send()
  assert.equal(f.conversation.messages.value.at(-1)?.id, sent.id)
  assert.equal(f.conversation.selected.value?.executing, true)
  assert.equal(f.conversation.lastEvent.value, cursor)
  assert.equal(f.refreshes(), 0)
  await f.streams.get('a')!.onEvent({ sequence: 2, kind: 'message', data: sent })
  await f.streams.get('a')!.onEvent({ sequence: 3, kind: 'run_finished', data: {
    runId: sent.runId, status: 'succeeded', message: message('answer', 3, sent.runId, 'assistant'),
  } })
  assert.deepEqual(f.conversation.messages.value.slice(-2).map(item => item.id), ['new-user', 'answer'])
  assert.equal(f.conversation.selected.value?.executing, false)
  assert.equal(f.refreshes(), 0)
})

test('reopening the selected conversation restores a stopped subscription without clearing its draft', async t => {
  const { conversation, sessions, streams } = fixture(t)
  await conversation.selectSession(session('a'))
  const previous = streams.get('a')!
  const generation = conversation.syncDiagnostics.value?.scope
  previous.onCheckpoint!({ throughSequence: conversation.lastEvent.value, session: { ...conversation.selected.value!, open: false } })
  assert.equal(conversation.synchronization.value.phase, 'stopped')
  assert.equal(previous.signal.aborted, true)
  conversation.draft.value = 'Keep this draft after reopening'
  sessions.value = sessions.value.map(item => item.id === 'a' ? { ...item, open: true } : item)
  await new Promise(resolve => setImmediate(resolve))
  assert.notEqual(streams.get('a'), previous)
  assert.equal(streams.get('a')!.signal.aborted, false)
  assert.equal(conversation.synchronization.value.phase, 'ready')
  assert.equal(conversation.syncDiagnostics.value?.scope, generation)
  assert.equal(conversation.draft.value, 'Keep this draft after reopening')
})

test('quoted selections preserve the draft and become ordinary persisted user content on send', async t => {
  const sent: { content: string; mode: string }[] = []
  const { conversation, saved } = fixture(t, (path, options) => {
    if (options?.method !== 'POST' || !path.endsWith('/messages')) return
    sent.push(JSON.parse(String(options.body)))
    return Promise.resolve({ message: { ...message('quoted-question', 2, 'quote-run'), content: sent.at(-1)!.content } })
  })
  await conversation.selectSession(session('a'))
  conversation.draft.value = 'Explain this part.'
  conversation.quoteSelection({ text: 'First line\n第二行 café 🙂', role: 'assistant' })
  assert.equal(conversation.draft.value, 'Explain this part.')
  assert.deepEqual(JSON.parse(saved.get(quoteStoragePrefix + 'a')!), conversation.quotedText.value)
  await conversation.send()
  assert.deepEqual(sent, [{ content: '> First line\n> 第二行 café 🙂\n\nExplain this part.', mode: 'now',
    modelKey: 'example', reasoningEffort: 'low', selectionMode: 'manual', selectionTier: 'speed', images: [] }])
  assert.equal(conversation.messages.value.at(-1)?.content, sent[0]!.content)
  assert.equal(conversation.quotedText.value, null)
  assert.equal(conversation.draft.value, '')
  assert.equal(saved.has(quoteStoragePrefix + 'a'), false)
})

test('quotes survive session changes and failed sends, and removing a quote preserves the question', async t => {
  const { conversation, saved, error } = fixture(t, (path, options) =>
    options?.method === 'POST' && path.endsWith('/messages') ? Promise.reject(new Error('Send failed')) : undefined)
  await conversation.selectSession(session('a'))
  conversation.quoteSelection({ text: 'Quote A', role: 'assistant' })
  conversation.draft.value = 'Question A'
  await conversation.selectSession(session('b'))
  assert.equal(JSON.stringify(conversation.quotedText.value), 'null')
  conversation.quoteSelection({ text: 'Quote B', role: 'user' })
  await conversation.selectSession(session('a'))
  assert.deepEqual(conversation.quotedText.value, { text: 'Quote A', role: 'assistant' })
  await conversation.send()
  assert.match(error.value, /Send failed/)
  assert.equal(conversation.quotedText.value?.text, 'Quote A')
  assert.equal(conversation.draft.value, 'Question A')
  conversation.quoteSelection({ text: 'Replacement', role: 'user' })
  assert.equal(conversation.quotedText.value?.text, 'Replacement')
  conversation.removeQuote()
  assert.equal(conversation.draft.value, 'Question A')
  assert.equal(saved.has(quoteStoragePrefix + 'a'), false)
  assert.equal(JSON.parse(saved.get(quoteStoragePrefix + 'b')!).text, 'Quote B')
})

for (const failed of [false, true]) {
  test(`a late quoted send ${failed ? 'failure' : 'success'} cannot clear another session's quote`, async t => {
    const pending = deferred<unknown>(), started = deferred<void>()
    const { conversation, saved } = fixture(t, (path, options) => {
      if (path === '/sessions/a/messages' && options?.method === 'POST') { started.resolve(); return pending.promise }
    })
    await conversation.selectSession(session('a'))
    conversation.quoteSelection({ text: 'Quote A', role: 'assistant' })
    const sent = conversation.send()
    await started.promise
    await conversation.selectSession(session('b'))
    conversation.quoteSelection({ text: 'Quote B', role: 'user' })
    if (failed) pending.reject(new Error('Old send failed'))
    else pending.resolve({ message: message('old-a', 2, 'quote-a') })
    await sent
    assert.equal(conversation.selected.value?.id, 'b')
    assert.equal(conversation.quotedText.value?.text, 'Quote B')
    assert.equal(JSON.parse(saved.get(quoteStoragePrefix + 'b')!).text, 'Quote B')
  })
}

test('attachments use server capabilities for manual and automatic selection without a protocol whitelist', t => {
  const { conversation, modelCatalog } = fixture(t)
  modelCatalog.value.models.push({ key: 'new-protocol', provider: 'future-protocol', id: 'vision', inputModalities: ['text', 'image'] })
  conversation.chooseModel('new-protocol')
  assert.equal(conversation.canAttachImage.value, true)
  conversation.chooseModel('example')
  assert.equal(conversation.canAttachImage.value, false)
  conversation.selectionMode.value = 'auto'
  assert.equal(conversation.canAttachImage.value, true)
  modelCatalog.value.models[1]!.inputModalities = ['text']
  assert.equal(conversation.canAttachImage.value, false)
})

for (const fail of [false, true]) {
  test(`late session load ${fail ? 'failure' : 'success'} cannot change the selected session`, async t => {
    const load = deferred<unknown>()
    const loading = deferred<void>()
    let signal: AbortSignal | null | undefined
    const { conversation, busy, error } = fixture(t, (path, options) => {
      if (path !== '/sessions/a/inputs') return
      signal = options?.signal
      loading.resolve()
      return load.promise
    })
    const oldSelection = conversation.selectSession(session('a'))
    await loading.promise
    await conversation.selectSession(session('b'))
    conversation.draft.value = 'unsent B'
    error.value = 'current B error'
    busy.value = true
    assert.equal(signal?.aborted, true)
    if (fail) load.reject(new Error('old A failure'))
    else load.resolve([{ id: 'old-a-input', mode: 'now', status: 'queued', content: 'old' }])
    await oldSelection
    assert.equal(conversation.selected.value?.id, 'b')
    assert.deepEqual(conversation.messages.value.map(item => item.id), ['b-history'])
    assert.equal(conversation.draft.value, 'unsent B')
    assert.deepEqual(conversation.queuedInputs.value, [])
    assert.equal(busy.value, true)
    assert.equal(error.value, 'current B error')
  })
}

for (const fail of [false, true]) {
  test(`late send ${fail ? 'failure' : 'success'} and finally cannot clear the next session draft or busy state`, async t => {
    const sentA = deferred<unknown>()
    const sentB = deferred<unknown>()
    const startedA = deferred<void>()
    const startedB = deferred<void>()
    let signalA: AbortSignal | null | undefined
    const { conversation, busy, error, streams, saved } = fixture(t, (path, options) => {
      if (options?.method !== 'POST') return
      if (path === '/sessions/a/messages') { signalA = options.signal; startedA.resolve(); return sentA.promise }
      if (path === '/sessions/b/messages') { startedB.resolve(); return sentB.promise }
    })
    await conversation.selectSession(session('a'))
    conversation.draft.value = 'send A'
    const oldSend = conversation.send()
    await startedA.promise
    await conversation.selectSession(session('b'))
    assert.equal(signalA?.aborted, true)
    conversation.draft.value = 'send B'
    const currentSend = conversation.send()
    await startedB.promise
    assert.equal(busy.value, true)
    error.value = 'current B error'
    // Also exercise a stale callback from a client that delivers after cancellation.
    streams.get('a')?.onEvent({ sequence: 100, kind: 'message', data: message('late-a-event', 100, 'run-a') })
    if (fail) sentA.reject(new Error('old A send failure'))
    else sentA.resolve({ runId: 'run-a', message: message('late-a-response', 2, 'run-a') })
    await oldSend
    assert.equal(conversation.selected.value?.id, 'b')
    assert.equal(conversation.draft.value, 'send B')
    assert.equal(saved.get('vista-session-draft:b'), 'send B')
    assert.deepEqual(conversation.messages.value.map(item => item.id), ['b-history'])
    assert.equal(conversation.activeRunID.value, 'history')
    assert.equal(busy.value, true)
    assert.equal(error.value, 'current B error')
    sentB.resolve({ runId: 'run-b', message: message('b-user', 2, 'run-b') })
    await currentSend
    assert.equal(busy.value, false)
    assert.equal(conversation.draft.value, '')
    assert.equal(conversation.activeRunID.value, 'run-b')
  })
}

test('run_finished before the POST response does not revive an already finished run', async t => {
  const posted = deferred<unknown>()
  const started = deferred<void>()
  const { conversation, busy, streams } = fixture(t, (path, options) => {
    if (path === '/sessions/a/messages' && options?.method === 'POST') { started.resolve(); return posted.promise }
  })
  await conversation.selectSession(session('a'))
  conversation.draft.value = 'Hi'
  const sending = conversation.send()
  await started.promise
  const emit = (event: StreamEvent) => streams.get('a')!.onEvent(event)
  const user = message('user-a', 2, 'run-a')
  emit({ sequence: 2, kind: 'message', data: user })
  emit({ sequence: 3, kind: 'model_delta', data: { runId: 'run-a', turn: 1, content: 'Hello' } })
  emit({ sequence: 4, kind: 'run_finished', data: { runId: 'run-a', status: 'succeeded', message: { ...message('reply-a', 4, 'run-a', 'assistant'), outputTurn: 1 } } })
  posted.resolve({ runId: 'run-a', message: user })
  await sending
  assert.deepEqual(conversation.runProgress.value, { runId: 'run-a', phase: 'finished' })
  assert.equal(conversation.selected.value?.executing, false)
  assert.equal(conversation.agentWorking.value, false)
  assert.deepEqual(conversation.modelDrafts.value, [])
  assert.deepEqual(conversation.messages.value.map(item => item.id), ['a-history', 'user-a', 'reply-a'])
  assert.equal(busy.value, false)
})


test('selecting the same session again still rejects the previous generation', async t => {
  const oldInputs = deferred<unknown>()
  const loading = deferred<void>()
  let requests = 0
  let oldSignal: AbortSignal | null | undefined
  const { conversation, busy, error } = fixture(t, (path, options) => {
    if (path !== '/sessions/a/inputs' || ++requests !== 1) return
    oldSignal = options?.signal
    loading.resolve()
    return oldInputs.promise
  })
  const firstLoad = conversation.selectSession(session('a'))
  await loading.promise
  await conversation.selectSession(session('a'))
  conversation.draft.value = 'new generation draft'
  busy.value = true
  error.value = 'current generation error'
  oldInputs.resolve([{ id: 'old-generation-input', mode: 'now', status: 'queued', content: 'old' }])
  await firstLoad
  assert.equal(oldSignal?.aborted, true)
  assert.equal(conversation.draft.value, 'new generation draft')
  assert.deepEqual(conversation.queuedInputs.value, [])
  assert.equal(busy.value, true)
  assert.equal(error.value, 'current generation error')
})

test('initial load derives the active run from the latest user rather than a late older reply', async t => {
  const { conversation } = fixture(t, path => {
    if (path === '/sessions/a/messages') return Promise.resolve([
      message('old-user', 10, 'old-run'), message('new-user', 20, 'new-run'), message('late-old-reply', 30, 'old-run', 'assistant'),
    ])
  })
  await conversation.selectSession(session('a'))
  assert.equal(conversation.activeRunID.value, 'new-run')
  assert.equal(conversation.activeRunSequence.value, 20)
})

test('snapshot restores root execution while preserving recent child tokens and its live boundary', async t => {
  const rootModel: StreamEvent = { sequence: 2, kind: 'model_request', data: { runId: 'run-a', taskId: 'main', provider: 'openai-chat', key: 'example', id: 'model', reasoningEffort: 'high' } }
  const rootTool: StreamEvent = { sequence: 3, kind: 'tool', data: { runId: 'run-a', taskId: 'main', status: 'started', tool: { callId: 'child', name: 'delegate', summary: 'Inspect source' } } }
  const future: StreamEvent = { sequence: 110, kind: 'model_request', data: { ...rootModel.data, id: 'future-model' } }
  const child: StreamEvent = { sequence: 100, kind: 'model_delta', data: { runId: 'run-a', taskId: 'child', parentTaskId: 'main', depth: 1, turn: 2, content: 'Preserve recent tokens' } }
  const { conversation, sessions, streams } = fixture(t, path => {
    if (path === '/sessions/a/snapshot') return Promise.resolve({ session: { ...session('a'), executing: true }, throughSequence: 100,
      messages: [message('user-a', 1, 'run-a')], events: [rootModel, rootTool, child], hasOlder: false, hasOlderEvents: true })
    if (path.endsWith('/runs/run-a/overview')) return Promise.resolve({ modelCalls: 50, throughSequence: 110, startedAt: '', events: [rootModel, rootTool, future] })
  })
  sessions.value[0]!.executing = true
  await conversation.selectSession(sessions.value[0]!)
  assert.deepEqual(conversation.runProgress.value, { runId: 'run-a', phase: 'tool', tool: 'delegate' })
  assert.equal(conversation.modelByRun.value['run-a']?.id, 'model')
  assert.equal(conversation.lastEvent.value, 100)
  assert.equal(conversation.modelDrafts.value[0]?.content, 'Preserve recent tokens')
  streams.get('a')?.onEvent({ sequence: 101, kind: 'model_delta', data: { ...child.data, content: ' and live tokens' } })
  assert.equal(conversation.modelDrafts.value[0]?.content, 'Preserve recent tokens and live tokens')
})

test('steering and a final reply keep execution details on the original user message', async t => {
  const { conversation, streams } = fixture(t)
  await conversation.selectSession(session('a'))
  const stream = streams.get('a')!
  const first = message('first-user', 2, 'run-a')
  const steering = message('steering-user', 5, 'run-a')
  const answer = { ...message('final-answer', 6, 'run-a', 'assistant'), outputTurn: 1 }
  stream.onEvent({ sequence: 2, kind: 'message', data: first })
  stream.onEvent({ sequence: 3, kind: 'tool', data: { runId: 'run-a', status: 'started', tool: { callId: 'call', name: 'read_file' } } })
  stream.onEvent({ sequence: 4, kind: 'model_delta', data: { runId: 'run-a', turn: 1, content: 'Current response' } })
  stream.onEvent({ sequence: 5, kind: 'message', data: steering })
  assert.equal(conversation.runActivityAnchor(first), true)
  assert.equal(conversation.runActivityAnchor(steering), false)
  // A completed stage response remains anchored to the message that started
  // it; a later steering message must not make that draft jump underneath it.
  assert.equal(conversation.liveDrafts(first)[0]?.content, 'Current response')
  assert.equal(conversation.liveDrafts(steering).length, 0)
  assert.deepEqual(conversation.runReplyTurns('run-a'), [])
  stream.onEvent({ sequence: 6, kind: 'run_finished', data: { runId: 'run-a', status: 'succeeded', message: answer } })
  assert.equal(conversation.runActivityAnchor(first), true)
  assert.equal(conversation.runActivityAnchor(answer), false)
  assert.deepEqual(conversation.runReplyTurns('run-a'), [1])
  assert.deepEqual(conversation.runReplyTurns('other-run'), [])
})

test('a reply before a followup stays in the timeline while the same run continues and completes', async t => {
  const { conversation, streams } = fixture(t)
  await conversation.selectSession(session('a'))
  const stream = streams.get('a')!
  const question = message('question', 2, 'run-a')
  const firstAnswer = { ...message('first-answer', 5, 'run-a', 'assistant'), outputTurn: 1 }
  const followup = message('followup', 6, 'run-a')
  const finalAnswer = { ...message('final-answer', 11, 'run-a', 'assistant'), outputTurn: 3, resultsAfter: 5 }
  stream.onEvent({ sequence: 2, kind: 'message', data: question })
  stream.onEvent({ sequence: 3, kind: 'model_delta', data: { runId: 'run-a', turn: 1, content: 'First answer' } })
  stream.onEvent({ sequence: 4, kind: 'model_result', data: { runId: 'run-a', turn: 1, modelResult: { provider: 'openai-chat', id: 'model', finishReason: 'stop' } } })
  stream.onEvent({ sequence: 5, kind: 'message', data: firstAnswer })
  assert.equal(conversation.agentWorking.value, true)
  assert.deepEqual(conversation.runReplyTurns('run-a'), [1])
  assert.equal(conversation.modelDrafts.value.length, 0)
  assert.equal(conversation.traceSegment(question).through, 5)
  assert.equal(conversation.traceSegment(question).active, false)
  stream.onEvent({ sequence: 6, kind: 'message', data: followup })
  stream.onEvent({ sequence: 7, kind: 'model_result', data: { runId: 'run-a', turn: 2, modelResult: { provider: 'openai-chat', id: 'model', finishReason: 'tool_calls' } } })
  stream.onEvent({ sequence: 8, kind: 'model_delta', data: { runId: 'run-a', turn: 3, content: 'Followup answer' } })
  assert.deepEqual(conversation.liveDrafts(question), [])
  assert.deepEqual(conversation.liveDrafts(firstAnswer), [])
  assert.equal(conversation.liveDrafts(followup)[0]?.content, 'Followup answer')
  assert.deepEqual(conversation.resultsForMessage(firstAnswer).map(result => result.turn), [1])
  assert.equal(conversation.runActivityAnchor(followup), true)
  assert.equal(conversation.traceSegment(followup).active, true)
  assert.equal(conversation.traceSegment(followup).after, 5)
  assert.deepEqual(conversation.resultsForTrace(question).map(result => result.turn), [1])
  assert.deepEqual(conversation.resultsForTrace(followup).map(result => result.turn), [2])
  stream.onEvent({ sequence: 10, kind: 'model_result', data: { runId: 'run-a', turn: 3, modelResult: { provider: 'openai-chat', id: 'model', finishReason: 'stop' } } })
  stream.onEvent({ sequence: 11, kind: 'run_finished', data: { runId: 'run-a', status: 'succeeded', message: finalAnswer } })
  assert.deepEqual(conversation.messages.value.filter(item => item.runId === 'run-a').map(item => item.id), ['question', 'first-answer', 'followup', 'final-answer'])
  assert.deepEqual(conversation.runReplyTurns('run-a'), [1, 3])
  assert.deepEqual(conversation.resultsForMessage(firstAnswer).map(result => result.turn), [1])
  assert.deepEqual(conversation.resultsForMessage(finalAnswer).map(result => result.turn), [2, 3])
  assert.deepEqual(conversation.resultsForRun('run-a').map(result => result.turn), [1, 2, 3])
  assert.equal(conversation.runActivityAnchor(question), true)
  assert.equal(conversation.runActivityAnchor(followup), true)
  assert.equal(conversation.runActivityAnchor(finalAnswer), false)
  assert.deepEqual(conversation.resultsForTrace(followup).map(result => result.turn), [2, 3])
  assert.equal(conversation.traceSegment(question).through, 5)
  assert.equal(conversation.traceSegment(followup).through, undefined)
  assert.equal(conversation.agentWorking.value, false)
})


test('title synchronization preserves the execution and pending question just updated by SSE', async t => {
  const { conversation, sessions, streams } = fixture(t)
  const staleSession = { ...session('a'), pendingReply: questionPending('old-run') }
  sessions.value = [staleSession, session('b')]
  await conversation.selectSession(staleSession)
  const emit = (event: StreamEvent) => streams.get('a')!.onEvent(event)
  emit({ sequence: 2, kind: 'message', data: message('user-a', 2, 'run-a') })
  assert.equal(conversation.selected.value?.executing, true)
  assert.equal(conversation.selected.value?.pendingReply, undefined)
  // Sidebar reminders follow SSE without waiting for a status poll.
  assert.equal(sessions.value[0].executing, true)
  assert.equal(sessions.value[0].pendingReply, undefined)
  emit({ sequence: 3, kind: 'session_title', data: { title: 'New task title' } })
  assert.equal(conversation.selected.value?.title, 'New task title')
  assert.equal(conversation.selected.value?.executing, true)
  assert.equal(conversation.selected.value?.pendingReply, undefined)
  assert.equal(conversation.agentWorking.value, true)
  assert.equal(sessions.value[0].title, 'New task title')
  assert.equal(sessions.value[0].executing, true)
  assert.equal(sessions.value[0].pendingReply, undefined)
})

test('read receipts preserve newer replies and remain monotonic when responses arrive out of order', async t => {
  const first = deferred<{ readSequence: number }>(), second = deferred<{ readSequence: number }>()
  const reads: number[] = []
  const { conversation, sessions, streams } = fixture(t, (path, options) => {
    if (!path.endsWith('/read')) return
    reads.push(JSON.parse(String(options?.body)).sequence)
    return reads.length === 1 ? first.promise : second.promise
  })
  await conversation.selectSession(session('a'))
  const emit = (event: StreamEvent) => streams.get('a')!.onEvent(event)
  emit({ sequence: 2, kind: 'message', data: message('reply-1', 2, 'run-a', 'assistant') })
  const earlier = conversation.markRead(2)
  await conversation.markRead(2)
  emit({ sequence: 3, kind: 'message', data: message('reply-2', 3, 'run-a', 'assistant') })
  assert.equal(sessions.value[0].latestReplySequence, 3)
  assert.equal(sessions.value[0].readSequence || 0, 0)
  const later = conversation.markRead(3)
  second.resolve({ readSequence: 3 })
  await later
  first.resolve({ readSequence: 2 })
  await earlier
  assert.deepEqual(reads, [2, 3])
  assert.equal(sessions.value[0].readSequence, 3)
  assert.equal(conversation.selected.value?.readSequence, 3)
  // Duplicate SSE replay cannot resurrect unread or older metadata.
  emit({ sequence: 2, kind: 'message', data: message('reply-1', 2, 'run-a', 'assistant') })
  assert.equal(sessions.value[0].latestReplySequence, 3)
})

test('a late read receipt cannot mark the next selected conversation read', async t => {
  const receipt = deferred<{ readSequence: number }>()
  let signal: AbortSignal | null | undefined
  const { conversation, sessions } = fixture(t, (path, options) => {
    if (!path.endsWith('/read')) return
    signal = options?.signal
    return receipt.promise
  })
  await conversation.selectSession(session('a'))
  const oldRead = conversation.markRead(10)
  await conversation.selectSession(session('b'))
  assert.equal(signal?.aborted, true)
  receipt.resolve({ readSequence: 10 })
  await oldRead
  assert.equal(conversation.selected.value?.id, 'b')
  assert.equal(conversation.selected.value?.readSequence || 0, 0)
  assert.equal(sessions.value.find(item => item.id === 'b')?.readSequence || 0, 0)
})


test('automatic mode keeps manual model and reasoning memory independent', async t => {
  const { conversation, modelCatalog, saved, streams } = fixture(t)
  modelCatalog.value.models.push({ key: 'second', provider: 'openai-chat', id: 'model-second', reasoningOptions: ['low', 'high'] })
  await conversation.selectSession(session('a'))
  conversation.chooseModel('second')
  conversation.selectedReasoning.value = 'high'
  const manual = saved.get('vista-last-model')
  conversation.chooseSelectionMode('auto')
  streams.get('a')!.onEvent({ sequence: 2, kind: 'message', data: message('auto-user', 2, 'auto-run') })
  streams.get('a')!.onEvent({ sequence: 3, kind: 'model_request', data: { runId: 'auto-run', turn: 1, key: 'example', id: 'model', provider: 'openai-chat' } })
  assert.equal(conversation.selectedModel.value, 'example')
  assert.equal(saved.get('vista-last-model'), manual)
  conversation.chooseSelectionMode('manual')
  assert.equal(conversation.selectedModel.value, 'second')
  assert.equal(conversation.selectedReasoning.value, 'high')
})

test('queued inputs are not clipped and withdrawing appends to the existing draft', async t => {
  const { conversation } = fixture(t, (path, options) => {
    if (path.endsWith('/inputs')) return Promise.resolve(Array.from({ length: 30 }, (_, index) => ({ id: 'pending-' + index, mode: 'later', status: 'queued', content: 'Pending ' + index })))
    if (path.endsWith('/withdraw') && options?.method === 'POST') return Promise.resolve({ input: { id: 'pending-0', mode: 'later', status: 'withdrawn', content: 'Restored note' }, images: [] })
  })
  await conversation.selectSession(session('a'))
  assert.equal(conversation.pendingInputs.value.length, 30)
  conversation.draft.value = 'Unsent note'
  assert.equal(await conversation.mutateInput(conversation.pendingInputs.value[0], 'withdraw'), true)
  assert.equal(conversation.draft.value, 'Unsent note\n\nRestored note')
  assert.equal(conversation.pendingInputs.value.length, 29)
})

test('withdrawing a multi-image input retains all images alongside an existing attachment draft', async t => {
  const input = { id: 'pictures', mode: 'later' as const, status: 'queued', content: '', imageCount: 4 }
  const { conversation, error, client } = fixture(t, (path, options) => {
    if (path.endsWith('/inputs')) return Promise.resolve([input])
    if (path.endsWith('/withdraw') && options?.method === 'POST') return Promise.resolve({
      input: { ...input, status: 'withdrawn' },
      images: Array.from({ length: 4 }, (_, index) => ({ mediaType: 'image/png', hash: String(index).repeat(64), name: `original-${index}.png` })),
    })
  })
  client.image = async path => new Blob(['image-' + path.split('/').at(-1)![0]], { type: 'image/png' })
  await conversation.selectSession(session('a'))
  const draftFiles = Array.from({ length: 3 }, (_, index) => new File(['draft-' + index], `draft-${index}.png`, { type: 'image/png' }))
  conversation.addImages(draftFiles)
  assert.equal(error.value, '')
  assert.equal(conversation.attachedImages.value.length, 3)
  assert.equal(await conversation.mutateInput(input, 'withdraw'), true)
  assert.equal(conversation.attachedImages.value.length, 7)
  assert.deepEqual(conversation.attachedImages.value.slice(0, 3), draftFiles)
  assert.deepEqual(await Promise.all(conversation.attachedImages.value.slice(3).map(file => file.text())), ['image-0', 'image-1', 'image-2', 'image-3'])
  assert.deepEqual(conversation.attachedImages.value.slice(3).map(file => file.name), ['original-0.png', 'original-1.png', 'original-2.png', 'original-3.png'])
  assert.equal(conversation.pendingInputs.value.length, 0)
  conversation.addImages([new File([new Uint8Array(1048577)], 'oversized.png', { type: 'image/png' })])
  assert.equal(error.value, 'imageLimit')
  assert.equal(conversation.attachedImages.value.length, 7)
})

for (const fail of [false, true]) {
  test(`a late withdrawn resource ${fail ? 'failure' : 'success'} cannot affect the next session`, async t => {
    const image = deferred<Blob>(), started = deferred<void>()
    const input = { id: 'pictures', mode: 'later' as const, status: 'queued', content: 'Image note', imageCount: 1 }
    const { conversation, client, error } = fixture(t, (path, options) => {
      if (path.endsWith('/inputs')) return Promise.resolve([input])
      if (path.endsWith('/withdraw') && options?.method === 'POST') return Promise.resolve({
        input: { ...input, status: 'withdrawn' }, images: [{ mediaType: 'image/png', hash: 'a'.repeat(64), name: 'original.png' }],
      })
    })
    let signal: AbortSignal | undefined
    client.image = async (_path, abort) => { signal = abort; started.resolve(); return image.promise }
    await conversation.selectSession(session('a'))
    const withdrawing = conversation.mutateInput(input, 'withdraw')
    await started.promise
    await conversation.selectSession(session('b'))
    error.value = 'Current error'
    assert.equal(signal?.aborted, true)
    if (fail) image.reject(new Error('Old image failure'))
    else image.resolve(new Blob(['image'], { type: 'image/png' }))
    assert.equal(await withdrawing, false)
    assert.equal(conversation.draft.value, 'draft B')
    assert.equal(error.value, 'Current error')
    assert.equal(conversation.attachedImages.value.length, 0)
  })
}

test('feedback belongs to the reply and can be changed and removed without changing the selected model', async t => {
  const votes: unknown[] = []
  const { conversation } = fixture(t, (path, options) => {
    if (path.endsWith('/messages/reply/feedback') && options?.method === 'PUT') { votes.push(JSON.parse(String(options.body))); return Promise.resolve({}) }
  })
  await conversation.selectSession(session('a'))
  const reply = { ...message('reply', 2, 'run', 'assistant'), modelId: 'previous-model' }
  await conversation.rateMessage(reply, 'up')
  assert.equal(conversation.feedback.value.reply, 'up')
  await conversation.rateMessage(reply, 'down')
  assert.equal(conversation.feedback.value.reply, 'down')
  await conversation.rateMessage(reply, 'down')
  assert.equal(conversation.feedback.value.reply, undefined)
  assert.deepEqual(votes, [{ value: 'up' }, { value: 'down' }, { value: null }])
})


test('late input snapshot cannot resurrect a pending message already delivered by SSE', async t => {
  const delayed = deferred<unknown>()
  let reads = 0
  const queued = { id: 'pending', mode: 'now', status: 'queued', content: 'Deliver once' }
  const { conversation, streams } = fixture(t, path => {
    if (path.endsWith('/inputs')) return ++reads === 1 ? Promise.resolve([queued]) : delayed.promise
  })
  await conversation.selectSession(session('a'))
  const refresh = conversation.refreshConversation()
  streams.get('a')!.onEvent({ sequence: 2, kind: 'input_delivered', data: { id: 'pending', runId: 'run' } })
  delayed.resolve([queued])
  await refresh
  assert.equal(conversation.pendingInputs.value.length, 0)
})

const compactStatus = (phase: CompactionStatus['phase'], sequence: number, manual = true): CompactionStatus => ({ runId: 'compact-a', phase, sequence, manual })

test('context totals follow runtime and compaction, reject stale snapshots, and reset on session change', async t => {
  const first = deferred<unknown>()
  const { conversation, streams } = fixture(t, path => {
    if (path === '/sessions/a/context-usage') return first.promise
    if (path === '/sessions/b/context-usage') return Promise.resolve({ tokens: 600, sequence: 1 })
  })
  await conversation.selectSession(session('a'))
  const old = streams.get('a')!
  old.onEvent({ sequence: 2, kind: 'context_usage', data: { tokens: 14000 } })
  old.onEvent({ sequence: 3, kind: 'context_usage', data: { tokens: 21000 } })
  old.onEvent({ sequence: 4, kind: 'context_notice', data: { depth: 0, contextUsage: { tokens: 8000 } } })
  old.onEvent({ sequence: 4, kind: 'context_usage', data: { tokens: 21000 } })
  first.resolve({ tokens: 12000, sequence: 1 })
  await new Promise(resolve => setImmediate(resolve))
  assert.equal(conversation.contextUsage.value?.tokens, 8000)
  old.onEvent({ sequence: 5, kind: 'context_notice', data: { depth: 1, contextUsage: { tokens: 1 } } })
  assert.equal(conversation.contextUsage.value?.tokens, 8000)
  await conversation.selectSession(session('b'))
  await new Promise(resolve => setImmediate(resolve))
  old.onEvent({ sequence: 6, kind: 'context_usage', data: { tokens: 50000 } })
  assert.equal(conversation.contextUsage.value?.tokens, 600)
})

test('manual compaction is idle-only, survives early SSE, and Continue only fills a draft', async t => {
  const post = deferred<unknown>()
  let posts = 0
  const { conversation, streams } = fixture(t, (path, options) => {
    if (path.endsWith('/compact')) { posts++; return post.promise }
    if (path.endsWith('/messages') && options?.method !== 'POST') return Promise.resolve([message('u', 1, 'old'), message('a', 2, 'old', 'assistant')])
  })
  await conversation.selectSession(session('a'))
  conversation.draft.value = ''
  const pending = conversation.compactSession()
  await conversation.compactSession()
  assert.equal(posts, 1)
  const stream = streams.get('a')!
  stream.onEvent({ sequence: 3, kind: 'context_status', data: compactStatus('compacting', 3) })
  assert.equal(conversation.manualCompacting.value, true)
  assert.equal(conversation.canCompact.value, false)
  stream.onEvent({ sequence: 4, kind: 'context_status', data: compactStatus('compacted', 4) })
  stream.onEvent({ sequence: 4, kind: 'context_status', data: compactStatus('compacted', 4) })
  post.resolve({ runId: 'compact-a', event: { sequence: 3, kind: 'context_status', data: compactStatus('compacting', 3) } })
  await pending
  assert.equal(conversation.compaction.value?.phase, 'compacted')
  assert.equal(conversation.selected.value?.executing, false)
  assert.equal(conversation.continueAfterCompaction.value, true)
  assert.equal(conversation.fillContinueDraft(), true)
  assert.equal(conversation.draft.value, 'continueConversation')
  assert.equal(conversation.continueAfterCompaction.value, false)
  assert.equal(posts, 1)
  assert.equal(conversation.messages.value.length, 2)
})

test('manual success restores after refresh and protects existing text and attachments', async t => {
  const { conversation } = fixture(t, path => {
    if (path.endsWith('/context')) return Promise.resolve(compactStatus('compacted', 9))
  })
  await conversation.selectSession(session('a'))
  assert.equal(conversation.fillContinueDraft(), false)
  assert.equal(conversation.draft.value, 'draft A')
  conversation.draft.value = ''
  conversation.attachedImages.value = [new File(['image'], 'image.png', { type: 'image/png' })]
  assert.equal(conversation.fillContinueDraft(), false)
  conversation.attachedImages.value = []
  assert.equal(conversation.continueAfterCompaction.value, true)
  await conversation.selectSession(session('b'))
  assert.equal(conversation.draft.value, 'draft B')
})

test('automatic compaction failure remains visible and never offers manual Continue', async t => {
  const { conversation, streams } = fixture(t)
  await conversation.selectSession(session('a'))
  conversation.draft.value = ''
  const stream = streams.get('a')!
  stream.onEvent({ sequence: 2, kind: 'message', data: message('u', 2, 'run') })
  stream.onEvent({ sequence: 3, kind: 'context_status', data: { ...compactStatus('compacting', 3, false), runId: 'run' } })
  assert.equal(conversation.agentStatus.value, 'contextCompacting')
  stream.onEvent({ sequence: 4, kind: 'run_finished', data: { runId: 'run', status: 'failed', message: { ...message('f', 4, 'run', 'assistant'), content: 'overflow recovery failed', failure: true } } })
  assert.equal(conversation.compaction.value?.phase, 'failed')
  assert.equal(conversation.compaction.value?.error, 'overflow recovery failed')
  assert.equal(conversation.agentWorking.value, false)
  assert.equal(conversation.continueAfterCompaction.value, false)
})

test('a late compaction POST or SSE cannot modify a different session', async t => {
  const post = deferred<unknown>()
  const { conversation, streams } = fixture(t, path => {
    if (path.endsWith('/compact')) return post.promise
    if (path.endsWith('/messages')) return Promise.resolve([message('u', 1, 'old'), message('a', 2, 'old', 'assistant')])
  })
  await conversation.selectSession(session('a'))
  const pending = conversation.compactSession()
  const old = streams.get('a')!
  await conversation.selectSession(session('b'))
  old.onEvent({ sequence: 4, kind: 'context_status', data: compactStatus('compacted', 4) })
  post.resolve({ runId: 'compact-a', event: { sequence: 3, kind: 'context_status', data: compactStatus('compacting', 3) } })
  await pending
  assert.equal(conversation.compaction.value, null)
  assert.equal(conversation.compactionRequestPending.value, false)
  assert.equal(conversation.draft.value, 'draft B')
})

test('successful send dismisses Continue, while failed send preserves the draft and suggestion', async t => {
  let fail = true
  const { conversation } = fixture(t, (path, options) => {
    if (path.endsWith('/context')) return Promise.resolve(compactStatus('compacted', 9))
    if (path.endsWith('/messages') && options?.method === 'POST') return fail ? Promise.reject(new Error('offline')) : Promise.resolve({ runId: 'new', message: message('u', 10, 'new') })
  })
  await conversation.selectSession(session('a'))
  conversation.draft.value = 'continue'
  await conversation.send()
  assert.equal(conversation.draft.value, 'continue')
  conversation.draft.value = ''
  assert.equal(conversation.continueAfterCompaction.value, true)
  conversation.fillContinueDraft()
  fail = false
  await conversation.send()
  assert.equal(conversation.continueAfterCompaction.value, false)
  assert.equal(conversation.draft.value, '')
})

test('welcome view restores the last model, reasoning, mode and tier after preferences and catalog become available', t => {
  const { conversation, preferences, modelCatalog } = fixture(t)
  preferences.ready.value = false
  modelCatalog.value = { default: '', models: [] }
  preferences.rememberedModel.value = { key: 'other', id: 'other-id', reasoningEffort: 'high' }
  preferences.reasoningByModelId.value = { 'other-id': 'high' }
  preferences.lastTier.value = 'quality'
  preferences.lastMode.value = 'auto'
  modelCatalog.value = { default: 'example', models: [
    { key: 'example', id: 'model', provider: 'openai-chat' },
    { key: 'other', id: 'other-id', provider: 'openai-chat', reasoningOptions: ['low', 'high'] },
  ] }
  preferences.ready.value = true
  assert.equal(conversation.selected.value, null)
  assert.equal(conversation.selectedModel.value, 'other')
  assert.equal(conversation.selectionMode.value, 'auto')
  assert.equal(conversation.selectionTier.value, 'quality')
  conversation.chooseSelectionMode('manual')
  assert.equal(conversation.selectedModel.value, 'other')
  assert.equal(conversation.selectedReasoning.value, 'high')
})

test('explicit budget and toggle choices survive remembering, session selection and sending', async t => {
  let requestBody: Record<string, unknown> = {}
  const { conversation, modelCatalog, preferences, saved } = fixture(t, (path, options) => {
    if (path.endsWith('/messages') && options?.method === 'POST') {
      requestBody = JSON.parse(String(options.body))
      return Promise.resolve({ message: message('reply', 2, 'run') })
    }
  })
  modelCatalog.value.models[0]!.reasoningOptions = ['off', 'on', 'budget:1024', 'budget:8192']
  preferences.reasoningByModelId.value = { model: 'high' }
  assert.equal(conversation.selectedReasoning.value, 'on')
  assert.deepEqual(conversation.reasoningChoices.value.map(option => option.label), [
    'reasoningOff', 'reasoningOn', 'reasoningBudget', 'reasoningBudget',
  ])
  conversation.selectedReasoning.value = 'budget:8192'
  assert.equal(preferences.reasoningByModelId.value.model, 'budget:8192')
  assert.equal(JSON.parse(saved.get('vista-reasoning-by-model')!).model, 'budget:8192')
  await conversation.selectSession(session('a'))
  assert.equal(conversation.selectedReasoning.value, 'budget:8192')
  conversation.draft.value = 'Use this budget'
  await conversation.send()
  assert.equal(requestBody.reasoningEffort, 'budget:8192')
  conversation.selectedReasoning.value = 'off'
  assert.equal(preferences.reasoningByModelId.value.model, 'off')
  conversation.draft.value = 'Disable thinking'
  await conversation.send()
  assert.equal(requestBody.reasoningEffort, 'off')
})

test('new conversation preparation creates no record and preserves the welcome text and attachments across sessions', async t => {
  let posts = 0
  const { conversation, saved } = fixture(t, (_, options) => {
    if (options?.method === 'POST') posts++
    return undefined
  })
  conversation.draft.value = 'Unsent welcome question'
  const image = new File(['png'], 'welcome.png', { type: 'image/png' })
  conversation.addImages([image])
  await conversation.selectSession(session('a'))
  conversation.draft.value = 'Session A draft'
  conversation.clearSession()
  assert.equal(posts, 0)
  assert.equal(conversation.selected.value, null)
  assert.equal(conversation.draft.value, 'Unsent welcome question')
  assert.equal(saved.get(welcomeDraftStorageKey), 'Unsent welcome question')
  assert.equal(saved.get('vista-session-draft:a'), 'Session A draft')
  assert.equal(conversation.attachedImages.value[0], image)
  // Reopening home or its settings does not clear the prepared draft.
  conversation.clearSession()
  assert.equal(conversation.draft.value, 'Unsent welcome question')
  assert.equal(conversation.attachedImages.value[0], image)
})

test('the first welcome send creates one session and sends the exact chosen model, effort, text and attachment once', async t => {
  const creating = deferred<unknown>()
  const requested = deferred<void>()
  const posts: Array<{ path: string; body: unknown }> = []
  const destinations: string[] = []
  const created = { ...session('first'), modelKey: 'second', manualModelKey: 'second' }
  const { conversation, modelCatalog, saved, busy } = fixture(t, (path, options) => {
    if (options?.method !== 'POST') return
    posts.push({ path, body: JSON.parse(String(options.body)) })
    if (path === '/sessions') { requested.resolve(); return creating.promise }
    if (path === '/sessions/first/messages') return Promise.resolve({ runId: 'first-run', message: message('first-user', 1, 'first-run') })
  }, async item => { destinations.push(item.id) })
  modelCatalog.value.models.push({ key: 'second', id: 'model-second', provider: 'openai-chat', reasoningOptions: ['low', 'high'], inputModalities: ['text', 'image'] })
  conversation.chooseModel('second')
  conversation.selectedReasoning.value = 'high'
  conversation.draft.value = 'Exact first message'
  conversation.addImages([new File(['png'], 'welcome.png', { type: 'image/png' })])
  const sending = conversation.send()
  await requested.promise
  assert.equal(busy.value, true)
  await conversation.send()
  assert.equal(posts.length, 1)
  creating.resolve(created)
  await sending
  assert.deepEqual(posts.map(item => item.path), ['/sessions', '/sessions/first/messages'])
  assert.deepEqual(posts[1].body, {
    content: 'Exact first message', images: [{ mediaType: 'image/png', name: 'welcome.png', data: btoa('png') }], mode: 'now',
    modelKey: 'second', reasoningEffort: 'high', selectionMode: 'manual', selectionTier: 'speed',
  })
  assert.deepEqual(destinations, ['first'])
  assert.equal(conversation.selected.value?.id, 'first')
  assert.equal(conversation.activeRunID.value, 'first-run')
  assert.equal(conversation.draft.value, '')
  assert.equal(conversation.attachedImages.value.length, 0)
  assert.equal(saved.has(welcomeDraftStorageKey), false)
  assert.equal(busy.value, false)
})

test('a failed first session creation preserves the welcome draft and attachment for retry', async t => {
  let creates = 0
  const { conversation, error, busy } = fixture(t, (path, options) => {
    if (path === '/sessions' && options?.method === 'POST') { creates++; return Promise.reject(new Error('creation failed')) }
  })
  conversation.draft.value = 'Keep this question'
  const image = new File(['png'], 'keep.png', { type: 'image/png' })
  conversation.addImages([image])
  await conversation.send()
  assert.equal(creates, 1)
  assert.equal(conversation.selected.value, null)
  assert.equal(conversation.draft.value, 'Keep this question')
  assert.equal(conversation.attachedImages.value[0], image)
  assert.equal(busy.value, false)
  assert.match(error.value, /creation failed/)
})

test('a failed first message keeps the created session and retry sends without creating another record', async t => {
  let creates = 0
  let sends = 0
  const { conversation, error, saved } = fixture(t, (path, options) => {
    if (options?.method !== 'POST') return
    if (path === '/sessions') { creates++; return Promise.resolve(session('first')) }
    if (path === '/sessions/first/messages') {
      if (++sends === 1) return Promise.reject(new Error('message rejected'))
      return Promise.resolve({ runId: 'first-run', message: message('first-user', 1, 'first-run') })
    }
  })
  conversation.draft.value = 'Retry this exact text'
  const image = new File(['png'], 'keep.png', { type: 'image/png' })
  conversation.addImages([image])
  await conversation.send()
  assert.equal(conversation.selected.value?.id, 'first')
  assert.equal(conversation.draft.value, 'Retry this exact text')
  assert.equal(saved.get('vista-session-draft:first'), 'Retry this exact text')
  assert.equal(conversation.attachedImages.value[0], image)
  assert.match(error.value, /message rejected/)
  await conversation.send()
  assert.equal(creates, 1)
  assert.equal(sends, 2)
  assert.equal(conversation.draft.value, '')
})

test('the initial automatic send carries mode and tier while keeping remembered manual selection independent', async t => {
  let submitted: unknown
  const { conversation, saved } = fixture(t, (path, options) => {
    if (options?.method !== 'POST') return
    if (path === '/sessions') return Promise.resolve(session('first'))
    if (path === '/sessions/first/messages') {
      submitted = JSON.parse(String(options.body))
      return Promise.resolve({ runId: 'auto-run', message: message('first-user', 1, 'auto-run') })
    }
  })
  conversation.selectedReasoning.value = 'high'
  const remembered = saved.get('vista-last-model')
  conversation.chooseSelectionMode('auto')
  conversation.chooseSelectionTier('quality')
  conversation.draft.value = 'Automatically select'
  await conversation.send()
  assert.deepEqual(submitted, { content: 'Automatically select', images: [], mode: 'now', selectionMode: 'auto', selectionTier: 'quality' })
  assert.equal(saved.get('vista-last-model'), remembered)
})

test('leaving home during first creation rejects its late result and preserves the new session draft', async t => {
  const creation = deferred<unknown>()
  const requested = deferred<void>()
  let sent = false
  const { conversation } = fixture(t, (path, options) => {
    if (options?.method !== 'POST') return
    if (path === '/sessions') { requested.resolve(); return creation.promise }
    sent = true
    return Promise.resolve({})
  })
  conversation.draft.value = 'Home question'
  const sending = conversation.send()
  await requested.promise
  await conversation.selectSession(session('b'))
  conversation.draft.value = 'Keep B'
  creation.resolve(session('first'))
  await sending
  assert.equal(conversation.selected.value?.id, 'b')
  assert.equal(conversation.draft.value, 'Keep B')
  assert.equal(sent, false)
})

test('full reply loads once and copying reuses the complete content', async t => {
  const loading = deferred<OutputView>()
  const paths: string[] = []
  const full = '# Report\n\n' + '完整正文🙂\n'.repeat(1200) + '\nEnd of report'
  const reply: Message = { ...message('long-reply', 2, 'run-a', 'assistant'), content: full.slice(0, 100), outputTurn: 7, outputBytes: Buffer.byteLength(full) }
  const { conversation } = fixture(t, path => {
    if (path.includes('/model-output?')) { paths.push(path); return loading.promise }
  })
  await conversation.selectSession(session('a'))
  conversation.messages.value = [reply]
  const read = conversation.readModelOutput(reply)
  const copy = conversation.fullMessageText(reply)
  assert.deepEqual(paths, ['/sessions/a/runs/run-a/model-output?turn=7&full=1'])
  assert.equal(conversation.modelOutputLoading.value[reply.id], true)
  loading.resolve({ content: full, offset: 0, nextOffset: reply.outputBytes!, availableBytes: reply.outputBytes! })
  await read
  assert.equal(await copy, full)
  assert.equal(conversation.modelOutput.value[reply.id].content, full)
  assert.equal(conversation.modelOutputLoading.value[reply.id], false)
  await conversation.readModelOutput(reply)
  assert.equal(await conversation.fullMessageText(reply), full)
  assert.equal(paths.length, 1)
})

test('a late full reply cannot replace output or errors in a new session', async t => {
  const loading = deferred<OutputView>()
  let signal: AbortSignal | null | undefined
  const { conversation } = fixture(t, (path, options) => {
    if (path.includes('/model-output?')) { signal = options?.signal; return loading.promise }
  })
  const reply: Message = { ...message('long-reply', 2, 'run-a', 'assistant'), outputTurn: 1, outputBytes: 12000 }
  await conversation.selectSession(session('a'))
  const read = conversation.readModelOutput(reply)
  await conversation.selectSession(session('b'))
  assert.equal(signal?.aborted, true)
  loading.resolve({ content: 'old session text', offset: 0, nextOffset: 12000, availableBytes: 12000 })
  await read
  assert.deepEqual(conversation.modelOutput.value, {})
  assert.deepEqual(conversation.modelOutputLoading.value, {})
  assert.deepEqual(conversation.modelOutputFailure.value, {})
})

test('a failed full reply keeps its preview and retries only when requested', async t => {
  let attempts = 0
  const { conversation } = fixture(t, path => {
    if (!path.includes('/model-output?')) return
    if (++attempts === 1) return Promise.reject(new Error('temporarily unavailable'))
    return Promise.resolve({ content: 'recovered full report', offset: 0, nextOffset: 12000, availableBytes: 12000 })
  })
  const reply: Message = { ...message('long-reply', 2, 'run-a', 'assistant'), content: 'saved preview', outputTurn: 1, outputBytes: 12000 }
  await conversation.selectSession(session('a'))
  conversation.messages.value = [reply]
  await conversation.readModelOutput(reply)
  assert.equal(conversation.modelOutput.value[reply.id], undefined)
  assert.match(conversation.modelOutputFailure.value[reply.id], /temporarily unavailable/)
  await conversation.readModelOutput(reply)
  assert.equal(attempts, 1)
  await conversation.readModelOutput(reply, true)
  assert.equal(attempts, 2)
  assert.equal(conversation.modelOutputFailure.value[reply.id], '')
  assert.equal(conversation.modelOutput.value[reply.id].content, 'recovered full report')
})

test('recalling from an older history window finds the latest user behind a full assistant page without replacing history', async t => {
  const paths: string[] = []
  let latestRequests = 0
  const olderWindow = [message('older question', 10, 'old-run'), message('older reply', 20, 'old-run', 'assistant')]
  const { conversation, saved } = fixture(t, path => {
    if (path === '/sessions/a/messages') {
      paths.push(path)
      return Promise.resolve(++latestRequests === 1 ? olderWindow
        : Array.from({ length: 100 }, (_, index) => message(`latest reply ${index}`, 201 + index, 'latest-run', 'assistant')))
    }
    if (path === '/sessions/a/messages?before=201') {
      paths.push(path)
      return Promise.resolve([message('latest question', 150, 'latest-run'), message('first reply', 200, 'latest-run', 'assistant')])
    }
  })
  await conversation.selectSession(session('a'))
  conversation.hasNewer.value = true
  conversation.draft.value = ''
  const displayedHistory = conversation.messages.value

  assert.equal(await conversation.recallLastUserMessage(), true)
  assert.equal(conversation.draft.value, 'latest question')
  assert.equal(saved.get('vista-session-draft:a'), 'latest question')
  assert.equal(conversation.messages.value, displayedHistory)
  assert.deepEqual(conversation.messages.value, olderWindow)
  assert.equal(conversation.hasNewer.value, true)
  assert.deepEqual(paths, ['/sessions/a/messages', '/sessions/a/messages', '/sessions/a/messages?before=201'])
})

test('a pending recall cannot restore text after the user edits and clears the draft', async t => {
  const latest = deferred<Message[]>()
  let recalling = false
  const { conversation, saved } = fixture(t, path => {
    if (recalling && path === '/sessions/a/messages') return latest.promise
  })
  await conversation.selectSession(session('a'))
  conversation.hasNewer.value = true
  conversation.draft.value = ''
  recalling = true
  const recall = conversation.recallLastUserMessage()
  conversation.draft.value = 'new draft entered while loading'
  conversation.draft.value = ''
  latest.resolve([message('previous question', 100, 'latest-run')])

  assert.equal(await recall, false)
  assert.equal(conversation.draft.value, '')
  assert.equal(saved.has('vista-session-draft:a'), false)
  assert.deepEqual(conversation.messages.value.map(item => item.id), ['a-history'])
})

test('a pending recall cannot overwrite the next session draft', async t => {
  const latest = deferred<Message[]>()
  let recalling = false
  let signal: AbortSignal | null | undefined
  const { conversation, saved } = fixture(t, (path, options) => {
    if (recalling && path === '/sessions/a/messages') { signal = options?.signal; return latest.promise }
  })
  await conversation.selectSession(session('a'))
  conversation.hasNewer.value = true
  conversation.draft.value = ''
  recalling = true
  const recall = conversation.recallLastUserMessage()
  await conversation.selectSession(session('b'))
  assert.equal(signal?.aborted, true)
  latest.resolve([message('previous A question', 100, 'run-a')])

  assert.equal(await recall, false)
  assert.equal(conversation.selected.value?.id, 'b')
  assert.equal(conversation.draft.value, 'draft B')
  assert.equal(saved.get('vista-session-draft:b'), 'draft B')
  assert.deepEqual(conversation.messages.value.map(item => item.id), ['b-history'])
})

test('structured question submission uses the pending run and selected model without consuming composer draft', async t => {
  const sent = deferred<unknown>()
  let requestBody: Record<string, any> | undefined
  let posts = 0
  const { conversation, sessions, busy } = fixture(t, (path, options) => {
    if (path === '/sessions/a/messages' && options?.method === 'POST') {
      posts++
      requestBody = JSON.parse(String(options.body))
      return sent.promise
    }
  })
  sessions.value[0] = { ...session('a'), pendingReply: questionPending() }
  await conversation.selectSession(sessions.value[0])
  conversation.selectedReasoning.value = 'high'
  conversation.draft.value = 'unsent separate message'
  const submission = conversation.submitQuestion('question-run', [{ options: ['B'] }])
  assert.equal(busy.value, true)
  assert.equal(await conversation.submitQuestion('question-run', [{ options: ['A'] }]), false)
  assert.equal(posts, 1)
  assert.deepEqual(requestBody, { questionReply: { runId: 'question-run', answers: [{ options: ['B'] }] }, selectionMode: 'manual', selectionTier: 'speed', modelKey: 'example', reasoningEffort: 'high' })
  assert.equal(conversation.draft.value, 'unsent separate message')
  sent.resolve({ runId: 'continuation', message: message('answer B', 5, 'continuation') })
  assert.equal(await submission, true)
  assert.equal(busy.value, false)
  assert.equal(conversation.draft.value, 'unsent separate message')
  assert.equal(conversation.selected.value?.pendingReply, undefined)
  assert.equal(conversation.selected.value?.executing, true)
  assert.equal(conversation.activeRunID.value, 'continuation')
  assert.equal(conversation.messages.value.at(-1)?.content, 'answer B')
  assert.equal(await conversation.submitQuestion('question-run', [{ options: ['B'] }]), false)
  assert.equal(posts, 1)
})

test('question answers for an outdated run or an already answered question are not posted', async t => {
  let posts = 0
  const { conversation, sessions } = fixture(t, (_path, options) => {
    if (options?.method === 'POST') { posts++; return Promise.resolve({}) }
  })
  sessions.value[0] = { ...session('a'), pendingReply: questionPending() }
  await conversation.selectSession(sessions.value[0])
  assert.equal(await conversation.submitQuestion('old-question', [{ options: ['A'] }]), false)
  assert.equal(await conversation.submitQuestion('question-run', []), false)
  conversation.selected.value = { ...conversation.selected.value!, pendingReply: undefined }
  assert.equal(await conversation.submitQuestion('question-run', [{ options: ['A'] }]), false)
  assert.equal(posts, 0)
})

for (const failure of [false, true]) {
  test(`late question ${failure ? 'failure' : 'success'} cannot change the next session`, async t => {
    const sent = deferred<unknown>()
    let signal: AbortSignal | null | undefined
    const { conversation, sessions, busy, error } = fixture(t, (path, options) => {
      if (path === '/sessions/a/messages' && options?.method === 'POST') { signal = options.signal; return sent.promise }
    })
    sessions.value[0] = { ...session('a'), pendingReply: questionPending() }
    await conversation.selectSession(sessions.value[0])
    const submission = conversation.submitQuestion('question-run', [{ options: ['A'] }])
    await conversation.selectSession(session('b'))
    assert.equal(signal?.aborted, true)
    conversation.draft.value = 'keep B draft'
    busy.value = true
    error.value = 'B error'
    if (failure) sent.reject(new Error('old question failed'))
    else sent.resolve({ runId: 'continuation-a', message: message('answer A', 5, 'continuation-a') })
    assert.equal(await submission, false)
    assert.equal(conversation.selected.value?.id, 'b')
    assert.equal(conversation.draft.value, 'keep B draft')
    assert.equal(error.value, 'B error')
    assert.equal(busy.value, true)
    assert.deepEqual(conversation.messages.value.map(item => item.id), ['b-history'])
  })
}

test('failed question submission leaves the pending question and draft available for retry', async t => {
  let posts = 0
  const { conversation, sessions, busy, error } = fixture(t, (path, options) => {
    if (path === '/sessions/a/messages' && options?.method === 'POST') {
      if (++posts === 1) return Promise.reject(new Error('temporarily unavailable'))
      return Promise.resolve({ runId: 'continued', message: message('accepted', 5, 'continued') })
    }
  })
  sessions.value[0] = { ...session('a'), pendingReply: questionPending() }
  await conversation.selectSession(sessions.value[0])
  const answers = [{ options: [], text: 'my own answer' }]
  assert.equal(await conversation.submitQuestion('question-run', answers), false)
  assert.equal(conversation.selected.value?.pendingReply?.runId, 'question-run')
  assert.equal(conversation.draft.value, 'draft A')
  assert.equal(busy.value, false)
  assert.match(error.value, /temporarily unavailable/)
  assert.equal(await conversation.submitQuestion('question-run', answers), true)
  assert.equal(posts, 2)
  assert.equal(error.value, '')
})

for (const fail of [false, true]) {
  test(`main output index auto-pages and ignores late ${fail ? 'failure' : 'success'} after switching sessions`, async t => {
    const loading = deferred<unknown>()
    let signal: AbortSignal | null | undefined
    const paths: string[] = []
    const index = Array.from({ length: 100 }, (_, i) => ({ kind: 'text', runId: 'history', taskId: 'main', turn: i + 5, sequence: i + 5, lastSequence: i + 5, createdAt: '', bytes: 20, complete: true }))
    const { conversation, error } = fixture(t, (path, options) => {
      if (path.endsWith('/main-outputs') && path.includes('/sessions/a/')) { paths.push(path); return Promise.resolve(index) }
      if (path.includes('/main-outputs?before=5')) { signal = options?.signal; paths.push(path); return loading.promise }
    })
    await conversation.selectSession(session('a'))
    await new Promise(resolve => setImmediate(resolve))
    assert.equal(paths.length, 2)
    assert.equal(conversation.mainOutputs.value.length, 100)
    await conversation.selectSession(session('b'))
    error.value = 'B error'
    assert.equal(signal?.aborted, true)
    if (fail) loading.reject(new Error('A index failed'))
    else loading.resolve([{ ...index[0], turn: 1, sequence: 1 }])
    await new Promise(resolve => setImmediate(resolve))
    assert.equal(conversation.mainOutputs.value.length, 0)
    assert.equal(error.value, 'B error')
  })
}

test('visible main output hydration keeps SSE received during a read and cancels on session switch', async t => {
  const loading = deferred<OutputView>()
  let signal: AbortSignal | null | undefined
  const { conversation, streams } = fixture(t, (path, options) => {
    if (path.includes('/model-output?')) { signal = options?.signal; return loading.promise }
  })
  await conversation.selectSession(session('a'))
  const emit = (sequence: number, content: string) => streams.get('a')!.onEvent({ sequence, kind: 'model_delta', data: { runId: 'history', turn: 1, content } })
  emit(2, 'prefix')
  const row = conversation.displayMessages.value.find(row => row.progress)!
  const pending = conversation.readDisplayOutput(row)
  emit(3, 'tail')
  loading.resolve({ content: 'prefix', offset: 0, nextOffset: 6, availableBytes: 6, lastSequence: 2 })
  await pending
  assert.equal(conversation.displayMessages.value.find(row => row.progress)?.content, 'prefixtail')
  await conversation.selectSession(session('b'))
  assert.equal(signal?.aborted, true)
  assert.equal(conversation.mainOutputs.value.length, 0)
})

test('reasoning remains in the process through final reply and resets on session switch', async t => {
  const { conversation, streams } = fixture(t)
  await conversation.selectSession(session('a'))
  const reasoning = { sequence: 2, kind: 'model_reasoning_delta', data: { runId: 'history', turn: 1, content: 'Checking sources' } }
  streams.get('a')!.onEvent(reasoning)
  streams.get('a')!.onEvent(reasoning)
  assert.equal(conversation.mainOutputs.value.length, 0)
  assert.equal(conversation.processEvents.value.filter(event => event.kind === 'model_reasoning_delta').length, 1)
  streams.get('a')!.onEvent({ sequence: 3, kind: 'model_result', data: { runId: 'history', turn: 1, modelResult: { provider: 'test', id: 'model' } } })
  streams.get('a')!.onEvent({ sequence: 4, kind: 'message', data: { ...message('final', 4, 'history', 'assistant'), outputTurn: 1 } })
  assert.equal(conversation.processEvents.value.find(event => event.kind === 'model_reasoning_delta')?.data.content, 'Checking sources')
  assert.equal(conversation.displayMessages.value.filter(row => row.progress).length, 0)
  await conversation.selectSession(session('b'))
  assert.equal(conversation.processEvents.value.some(event => event.kind === 'model_reasoning_delta'), false)
})

for (const fail of [false, true]) {
  test(`late main body ${fail ? 'failure' : 'success'} cannot alter another session`, async t => {
    const loading = deferred<OutputView>()
    let signal: AbortSignal | null | undefined
    const { conversation, streams, error } = fixture(t, (path, options) => {
      if (path.includes('/model-output?')) { signal = options?.signal; return loading.promise }
    })
    await conversation.selectSession(session('a'))
    streams.get('a')!.onEvent({ sequence: 2, kind: 'model_delta', data: { runId: 'history', turn: 1, content: 'Old body' } })
    const pending = conversation.readDisplayOutput(conversation.displayMessages.value.find(row => row.progress)!)
    await conversation.selectSession(session('b'))
    error.value = 'current session error'
    assert.equal(signal?.aborted, true)
    if (fail) loading.reject(new Error('late body error'))
    else loading.resolve({ content: 'Old body', offset: 0, nextOffset: 8, availableBytes: 8, lastSequence: 2 })
    await pending
    assert.equal(conversation.mainOutputs.value.length, 0)
    assert.deepEqual(conversation.modelOutputFailure.value, {})
    assert.deepEqual(conversation.modelOutputLoading.value, {})
    assert.equal(error.value, 'current session error')
  })
}

test('entering edit only prepares a draft and cancel restores the previous draft', async t => {
  const original = { ...message('original', 2, 'run'), content: 'Original question' }
  const methods: string[] = []
  const { conversation } = fixture(t, (path, options) => {
    if (path.endsWith('/original/draft')) { methods.push(options?.method || 'GET'); return Promise.resolve({ message: original, images: [] }) }
  })
  await conversation.selectSession(session('a'))
  conversation.draft.value = 'Unsent draft'
  conversation.quoteSelection({ text: 'Saved quote', role: 'assistant' })
  const before = [...conversation.messages.value]
  assert.equal(await conversation.beginEditMessage(original), true)
  assert.deepEqual(methods, ['GET'])
  assert.deepEqual(conversation.messages.value, before)
  assert.equal(conversation.draft.value, original.content)
  assert.equal(conversation.editingMessage.value?.id, original.id)
  conversation.cancelEditMessage()
  assert.equal(conversation.draft.value, 'Unsent draft')
  assert.equal(conversation.quotedText.value?.text, 'Saved quote')
  assert.equal(conversation.editingMessage.value, null)
})

test('editing submits one replacement request and reloads authoritative history', async t => {
  const original = { ...message('original', 2, 'run'), content: 'Original question' }
  const replacement = { ...message('replacement', 20, 'new-run'), content: 'Edited question' }
  const sent: Record<string, unknown>[] = []
  let replaced = false
  const { conversation } = fixture(t, (path, options) => {
    if (path.endsWith('/original/draft')) return Promise.resolve({ message: original, images: [] })
    if (path.endsWith('/messages') && options?.method === 'POST') {
      sent.push(JSON.parse(String(options.body))); replaced = true
      return Promise.resolve({ runId: replacement.runId, message: replacement })
    }
    if (path.endsWith('/messages')) return Promise.resolve(replaced ? [replacement] : [original])
  })
  await conversation.selectSession(session('a'))
  await conversation.beginEditMessage(original)
  conversation.draft.value = 'Edited question'
  await conversation.send('later')
  assert.equal(sent.length, 1)
  assert.equal(sent[0]!.replacesMessageId, original.id)
  assert.equal(sent[0]!.mode, 'now')
  assert.equal(conversation.editingMessage.value, null)
  assert.equal(conversation.messages.value[0]?.id, replacement.id)
  assert.equal(conversation.draft.value, '')
})

test('withdrawal returns the original attachments and leaves an idle draft', async t => {
  const original = { ...message('original', 2, 'run'), content: 'Original question' }
  let withdrawn = false
  const { conversation } = fixture(t, (path, options) => {
    if (path.endsWith('/original/revert') && options?.method === 'POST') {
      withdrawn = true
      return Promise.resolve({ message: original, images: [{ name: 'source.png', mediaType: 'image/png', data: btoa('original image bytes') }] })
    }
    if (path.endsWith('/messages')) return Promise.resolve(withdrawn ? [] : [original])
  })
  await conversation.selectSession(session('a'))
  assert.equal(await conversation.withdrawMessage(original), true)
  assert.equal(conversation.messages.value.length, 0)
  assert.equal(conversation.draft.value, original.content)
  assert.equal(conversation.attachedImages.value[0]?.name, 'source.png')
  assert.equal(await conversation.attachedImages.value[0]?.text(), 'original image bytes')
  assert.equal(conversation.agentWorking.value, false)
})

test('late edit loading cannot replace another session draft', async t => {
  const original = { ...message('original', 2, 'run') }
  const delayed = deferred<unknown>()
  const { conversation } = fixture(t, path => path.endsWith('/original/draft') ? delayed.promise : undefined)
  await conversation.selectSession(session('a'))
  const pending = conversation.beginEditMessage(original)
  await conversation.selectSession(session('b'))
  delayed.resolve({ message: original, images: [] })
  assert.equal(await pending, false)
  assert.equal(conversation.draft.value, 'draft B')
  assert.equal(conversation.editingMessage.value, null)
})

test('a remote rollback reloads history while preserving the current draft', async t => {
  let reverted = false
  const original = { ...message('original', 2, 'run') }
  const { conversation, streams } = fixture(t, path => path.endsWith('/snapshot') ? Promise.resolve({ session: session('a'),
    throughSequence: reverted ? 30 : 2, revertedSequence: reverted ? 30 : 0, messages: reverted ? [] : [original], events: [], hasOlder: false, hasOlderEvents: false }) : undefined)
  await conversation.selectSession(session('a'))
  conversation.draft.value = 'My draft'
  const oldStream = streams.get('a')!
  reverted = true
  oldStream.onEvent({ sequence: 30, kind: 'conversation_reverted', data: { beforeSequence: 2 } })
  for (let index = 0; index < 20 && conversation.draft.value !== 'My draft'; index++) await new Promise(resolve => setTimeout(resolve, 0))
  await new Promise(resolve => setTimeout(resolve, 0))
  assert.equal(conversation.messages.value.length, 0)
  assert.equal(conversation.draft.value, 'My draft')
  oldStream.onEvent({ sequence: 31, kind: 'message', data: original })
  assert.equal(conversation.messages.value.length, 0)
})

test('snapshot recovery preserves composer state and a send acknowledgement newer than its boundary', async t => {
  const rebuilding = deferred<unknown>(), started = deferred<void>()
  const original = [message('u', 1, 'old'), message('a', 2, 'old', 'assistant')]
  let reads = 0, indexes = 0
  const { conversation, streams } = fixture(t, (path, options) => {
    if (path.endsWith('/snapshot')) {
      if (++reads === 2) { started.resolve(); return rebuilding.promise }
      return Promise.resolve({ session: session('a'), throughSequence: 2, messages: original, events: [], hasOlder: false, hasOlderEvents: false })
    }
    if (path.endsWith('/messages') && options?.method === 'POST') return Promise.resolve({ runId: 'new', message: message('new-user', 10, 'new') })
    if (path.includes('/runs/old/main-outputs')) { indexes++; return Promise.resolve([]) }
  })
  await conversation.selectSession(session('a'))
  await nextTick()
  const previousStream = streams.get('a')!
  conversation.resynchronize()
  await started.promise
  conversation.draft.value = 'Send during repair'
  await conversation.send()
  conversation.draft.value = 'Keep this draft'
  conversation.quoteSelection({ text: 'Keep this quote', role: 'assistant' })
  const image = new File(['image'], 'draft.png', { type: 'image/png' })
  conversation.attachedImages.value = [image]
  conversation.chooseModel('example')
  conversation.selectedReasoning.value = 'high'
  rebuilding.resolve({ session: session('a'), throughSequence: 2, messages: original, events: [], hasOlder: false, hasOlderEvents: false })
  await streams.get('a')!.beforeConnect!()
  await nextTick()
  assert.deepEqual(conversation.messages.value.map(item => item.id), ['u', 'a', 'new-user'])
  assert.equal(conversation.activeRunID.value, 'new')
  assert.equal(conversation.selected.value?.executing, true)
  assert.equal(conversation.draft.value, 'Keep this draft')
  assert.equal(conversation.quotedText.value?.text, 'Keep this quote')
  assert.equal(conversation.attachedImages.value[0], image)
  assert.equal(conversation.selectedReasoning.value, 'high')
  assert.equal(indexes, 2)
  assert.equal(previousStream.signal.aborted, true)
  assert.equal(conversation.lastEvent.value, 2, 'A POST acknowledgement cannot skip missing records')
})

test('a late recovery snapshot cannot modify another conversation or the same conversation in a new scope', async t => {
  for (const nextID of ['b', 'a']) {
    const pending = deferred<unknown>(), started = deferred<void>()
    let reads = 0
    const { conversation, streams } = fixture(t, path => {
      if (path === '/sessions/a/snapshot' && ++reads === 2) { started.resolve(); return pending.promise }
    })
    await conversation.selectSession(session('a'))
    conversation.resynchronize()
    await started.promise
    const old = streams.get('a')!
    const repairing = old.beforeConnect!()
    await conversation.selectSession(session(nextID))
    conversation.draft.value = 'Current draft'
    pending.resolve({ session: session('a'), throughSequence: 99, messages: [message('stale', 99, 'stale')], events: [], hasOlder: false, hasOlderEvents: false })
    await repairing
    assert.equal(conversation.selected.value?.id, nextID)
    assert.equal(conversation.draft.value, 'Current draft')
    assert.equal(conversation.messages.value.some(item => item.id === 'stale'), false)
  }
})

test('recovery keeps an intentional older message window and does not acknowledge unseen replies', async t => {
  let reads = 0, receipts = 0
  const { conversation, streams } = fixture(t, (path, options) => {
    if (path.endsWith('/snapshot')) return Promise.resolve({ session: { ...session('a'), latestReplySequence: reads++ ? 500 : 400 }, throughSequence: reads > 1 ? 500 : 400,
      messages: Array.from({ length: 100 }, (_, i) => message(`latest-${i}`, (reads > 1 ? 401 : 301) + i, 'r', 'assistant')), events: [], hasOlder: true, hasOlderEvents: false })
    if (path.includes('/messages?before=')) {
      const before = Number(path.split('before=')[1])
      return Promise.resolve(Array.from({ length: 100 }, (_, i) => message(`older-${before}-${i}`, before - 100 + i, 'r', 'assistant')))
    }
    if (path.endsWith('/read') && options?.method === 'POST') { receipts++; return Promise.resolve({ readSequence: 500 }) }
  })
  await conversation.selectSession(session('a'))
  await conversation.loadOlder(); await conversation.loadOlder(); await conversation.loadOlder()
  assert.equal(conversation.hasNewer.value, true)
  const window = conversation.messages.value.map(item => item.id)
  conversation.resynchronize()
  await streams.get('a')!.beforeConnect!()
  assert.deepEqual(conversation.messages.value.map(item => item.id), window)
  assert.equal(conversation.hasNewer.value, true)
  assert.equal(conversation.latestMessageSequence.value, 500)
  assert.equal(receipts, 0)
})

test('failed recovery survives a successful send and an application error cannot advance the cursor', async t => {
  let failSnapshot = false
  const { conversation, streams } = fixture(t, (path, options) => {
    if (path.endsWith('/snapshot') && failSnapshot) return Promise.reject(new Error('snapshot unavailable'))
    if (path.endsWith('/messages') && options?.method === 'POST') return Promise.resolve({ runId: 'r', message: message('accepted', 10, 'r') })
  })
  await conversation.selectSession(session('a'))
  const stream = streams.get('a')!
  assert.throws(() => stream.onEvent({ sequence: 2, kind: 'message', data: null }), /Invalid conversation event/)
  assert.equal(conversation.lastEvent.value, 1)
  failSnapshot = true
  stream.onError!(new Error('apply failed'))
  await assert.rejects(stream.beforeConnect!(), /snapshot unavailable/)
  stream.onError!(new Error('snapshot unavailable'))
  conversation.draft.value = 'Send accepted'
  await conversation.send()
  assert.equal(conversation.synchronization.value.phase, 'error')
  assert.equal(conversation.lastEvent.value, 1)
})

test('late auxiliary snapshots cannot replace newer process and artifact events', async t => {
  let delay = false
  const jobs = deferred<Job[]>(), artifacts = deferred<Artifact[]>()
  const { conversation, streams } = fixture(t, path => {
    if (delay && path.endsWith('/jobs')) return jobs.promise
    if (delay && path.endsWith('/artifacts')) return artifacts.promise
  })
  await conversation.selectSession(session('a'))
  await nextTick()
  conversation.jobs.value = [{ id: 'unchanged', status: 'running', bytes: 0 } as Job]
  delay = true
  const pending = conversation.refreshConversation()
  const stream = streams.get('a')!
  await stream.onEvent({ sequence: 2, kind: 'job', data: { id: 'job', status: 'succeeded', bytes: 100, command: 'done' } })
  await stream.onEvent({ sequence: 3, kind: 'artifact', data: { id: 'artifact', status: 'succeeded', bytes: 100 } })
  jobs.resolve([{ id: 'job', status: 'running', bytes: 0 }, { id: 'unchanged', status: 'succeeded', bytes: 10 },
    { id: 'historical-job', status: 'succeeded', bytes: 20 }] as Job[])
  artifacts.resolve([{ id: 'artifact', status: 'started', bytes: 0 }, { id: 'historical-artifact', status: 'succeeded', bytes: 20 }] as Artifact[])
  await pending
  assert.equal(conversation.jobs.value[0].status, 'succeeded')
  assert.equal(conversation.artifacts.value[0].status, 'succeeded')
  assert.equal(conversation.jobs.value.find(job => job.id === 'unchanged')?.status, 'succeeded')
  assert.ok(conversation.jobs.value.some(job => job.id === 'historical-job'))
  assert.ok(conversation.artifacts.value.some(artifact => artifact.id === 'historical-artifact'))
})
