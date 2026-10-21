import assert from 'node:assert/strict'
import test from 'node:test'
import { effectScope, nextTick, reactive } from 'vue'
import type { VistaClient } from '../client/vista.ts'
import type { OutputView } from '../client/types.ts'
import { useFullOutput } from './useFullOutput.ts'

const settle = async () => { await nextTick(); await new Promise(resolve => setImmediate(resolve)) }
const view = (content: string): OutputView => ({ content, offset: 0, nextOffset: Buffer.byteLength(content), availableBytes: Buffer.byteLength(content) })
function client(request: VistaClient['request']): VistaClient { return { request, image: async () => new Blob(), streamEvents: async () => {} } }

test('full output stays unloaded while collapsed and cached across reopening', async () => {
  const paths: string[] = []
  const full = view('Beginning\n' + 'Long output\n'.repeat(1500) + 'End of output')
  const api = client(async <T>(path: string) => { paths.push(path); return full as T })
  const options = reactive({ path: '/sessions/session/artifacts/output/output?full=1', snapshot: '18000/true/completed', enabled: false })
  const first = effectScope()
  const state = first.run(() => useFullOutput(api, options))!
  await settle()
  assert.deepEqual(paths, [])
  options.enabled = true
  await settle()
  assert.deepEqual(paths, [options.path])
  assert.equal(state.output.value?.content, full.content)
  first.stop()
  const second = effectScope()
  const reopened = second.run(() => useFullOutput(api, options))!
  await settle()
  assert.equal(paths.length, 1)
  assert.equal(reopened.output.value?.content, full.content)
  second.stop()
})

test('two viewers deduplicate one snapshot without aborting a remaining viewer', async () => {
  let finish!: (value: OutputView) => void
  const pending = new Promise<OutputView>(resolve => { finish = resolve })
  let requests = 0
  let signal: AbortSignal | undefined
  const api = client(async <T>(_path: string, request?: RequestInit) => { requests++; signal = request?.signal as AbortSignal; return await pending as T })
  const options = { path: '/output?full=1', snapshot: '100', enabled: true }
  const first = effectScope()
  first.run(() => useFullOutput(api, options))!
  const second = effectScope()
  const surviving = second.run(() => useFullOutput(api, options))!
  assert.equal(requests, 1)
  first.stop()
  assert.equal(signal?.aborted, false)
  finish(view('Complete snapshot'))
  await settle()
  assert.equal(surviving.output.value?.content, 'Complete snapshot')
  second.stop()
})

test('running snapshot updates silently replace output once and completed state refreshes', async () => {
  let requests = 0
  const api = client(async <T>() => view(`Snapshot ${++requests}`) as T)
  const options = reactive({ path: '/jobs/job/output?full=1', snapshot: '4/false/running', enabled: true })
  const scope = effectScope()
  const state = scope.run(() => useFullOutput(api, options))!
  await settle()
  options.snapshot = '8/false/running'
  await settle()
  assert.equal(requests, 2)
  assert.equal(state.output.value?.content, 'Snapshot 2')
  options.snapshot = '8/true/succeeded'
  await settle()
  assert.equal(requests, 3)
  options.enabled = false
  await settle()
  options.snapshot = '12/true/succeeded'
  await settle()
  assert.equal(requests, 3)
  options.enabled = true
  await settle()
  assert.equal(requests, 4)
  scope.stop()
})

test('failed output exposes retry and late output cannot cross an identity change', async () => {
  let requests = 0
  let signal: AbortSignal | undefined
  let finish!: (value: OutputView) => void
  const pending = new Promise<OutputView>(resolve => { finish = resolve })
  const api = client(async <T>(path: string, options?: RequestInit) => {
    requests++
    if (requests === 1) throw new Error('Unavailable')
    if (path.includes('/old/')) { signal = options?.signal as AbortSignal; return await pending as T }
    return view('New session output') as T
  })
  const options = reactive({ path: '/old/output?full=1', snapshot: '1', enabled: true })
  const scope = effectScope()
  const state = scope.run(() => useFullOutput(api, options))!
  await settle()
  assert.ok(state.failure.value)
  const retry = state.retry()
  assert.equal(state.failure.value, '')
  options.path = '/new/output?full=1'
  await settle()
  assert.equal(signal?.aborted, true)
  finish(view('Old session output'))
  await retry
  assert.equal(state.output.value?.content, 'New session output')
  assert.equal(state.failure.value, '')
  scope.stop()
})

test('full output cache evicts old sessions and reloads complete text on return', async () => {
  let requests = 0
  const api = client(async <T>(path: string) => { requests++; return view(path + '\n' + 'body'.repeat(4000) + '\nend') as T })
  for (let index = 0; index < 40; index++) {
    const scope = effectScope()
    scope.run(() => useFullOutput(api, { path: `/sessions/${index}/output`, snapshot: 'done', enabled: true }))
    await settle()
    scope.stop()
  }
  const scope = effectScope()
  const state = scope.run(() => useFullOutput(api, { path: '/sessions/0/output', snapshot: 'done', enabled: true }))!
  await settle()
  assert.equal(requests, 41)
  assert.equal(state.output.value?.content, '/sessions/0/output\n' + 'body'.repeat(4000) + '\nend')
  scope.stop()
})

test('oversized output remains complete while open without being retained in the cache', async () => {
  let requests = 0
  const body = '正文'.repeat(3 * 1024 * 1024)
  const api = client(async <T>() => { requests++; return view(body) as T })
  const options = reactive({ path: '/large/output', snapshot: 'done', enabled: true })
  const scope = effectScope()
  const state = scope.run(() => useFullOutput(api, options))!
  await settle()
  assert.equal((state.output.value as OutputView | null)?.content, body)
  options.enabled = false
  await settle()
  assert.equal(state.output.value, null)
  options.enabled = true
  await settle()
  assert.equal(requests, 2)
  assert.equal((state.output.value as OutputView | null)?.content, body)
  scope.stop()
})
