import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { effectScope } from 'vue'
import { createVistaClient, createWorkspaceSync, stateTopics } from '@vista/client'
import type { NetworkConfiguration, NetworkSettings } from '@vista/client'
import { useNetworkConfig } from './useNetworkConfig.ts'

const sample: NetworkConfiguration = {
  revision: 'saved-1', version: 'fixture:1',
  config: { globalProxy: '', profiles: [] },
}
const settle = () => new Promise<void>(resolve => setImmediate(resolve))
async function until(condition: () => boolean) {
  const deadline = Date.now() + 1000
  while (!condition()) {
    assert.ok(Date.now() < deadline, 'condition timed out')
    await new Promise(resolve => setTimeout(resolve, 1))
  }
}

function fixture(t: TestContext) {
  const reads: { signal: AbortSignal; resolve: (value: NetworkConfiguration) => void; reject: (cause: Error) => void }[] = []
  const writes: { body: { config: NetworkSettings }; resolve: (value: NetworkConfiguration) => void }[] = []
  const sinks: ReadableStreamDefaultController<Uint8Array>[] = []
  const deliveries: AbortSignal[] = []
  const frame = () => new TextEncoder().encode(`event: state\ndata: ${JSON.stringify({
    epoch: 'fixture', revisions: Object.fromEntries(stateTopics.map(topic => [topic, topic === 'network' ? 1 : 0])),
  })}\n\n`)
  const client = createVistaClient({ fetch: async (path, init) => {
    if (path === '/api/events') {
      assert.ok(init?.signal)
      const signal = init.signal
      return new Response(new ReadableStream<Uint8Array>({ start(sink) {
        sinks.push(sink)
        signal.addEventListener('abort', () => { try { sink.close() } catch {} }, { once: true })
        sink.enqueue(frame())
      } }), { headers: { 'Content-Type': 'text/event-stream' } })
    }
    assert.equal(path, '/api/network-config')
    if (init?.method === 'PUT') return new Promise<Response>(resolve => {
      writes.push({ body: JSON.parse(String(init.body)), resolve: value => resolve(new Response(JSON.stringify(value))) })
    })
    assert.ok(init?.signal)
    const signal = init.signal
    return new Promise<Response>((resolve, reject) => {
      reads.push({ signal, resolve: value => resolve(new Response(JSON.stringify(value))), reject })
      // Keep a cancelled promise pending briefly to expose reuse/finally races.
      signal.addEventListener('abort', () => setTimeout(() => reject(signal.reason), 10), { once: true })
    })
  } })
  const sync = createWorkspaceSync({ client, status() {}, metrics() {}, reconnectDelayMs: 1 })
  const dependencies = { client, stateSync: { ...sync, subscribe(topics, receive) {
    return sync.subscribe(topics, (...args) => { deliveries.push(args[1]); return receive(...args) })
  } } } satisfies Parameters<typeof useNetworkConfig>[0]
  const mount = (active = true) => {
    const scope = effectScope()
    const state = scope.run(() => useNetworkConfig(dependencies, active))!
    t.after(() => scope.stop())
    return { scope, state }
  }
  t.after(() => sync.stop())
  sync.start()
  return { reads, writes, sinks, mount, deliveries, frame }
}

test('closing one subscriber preserves the deduplicated read for another view', async t => {
  const f = fixture(t)
  const first = f.mount()
  await until(() => f.reads.length === 1)
  const second = f.mount()
  await until(() => f.deliveries.length === 2)
  first.scope.stop()
  assert.equal(f.reads[0]!.signal.aborted, false)
  f.reads[0]!.resolve(sample)
  await until(() => !!second.state.value.value)
  assert.deepEqual(second.state.value.value, sample)
  f.sinks.at(-1)!.enqueue(f.frame())
  await settle()
  assert.equal(f.reads.length, 1, 'unchanged revisions must not refetch')
  assert.equal(second.state.error.value, '')
  assert.equal(second.state.loading.value, false)
})

test('the last subscriber cancels its read and reopening cannot reuse that cancelled promise', async t => {
  const f = fixture(t)
  const first = f.mount()
  await until(() => f.reads.length === 1)
  first.scope.stop()
  assert.equal(f.reads[0]!.signal.aborted, true)
  const second = f.mount()
  await until(() => f.reads.length === 2)
  f.reads[1]!.resolve(sample)
  await until(() => !!second.state.value.value)
  await new Promise(resolve => setTimeout(resolve, 20))
  f.sinks.at(-1)!.enqueue(f.frame())
  await settle()
  assert.deepEqual(second.state.value.value, sample)
  assert.equal(f.reads.length, 2)
  assert.equal(second.state.error.value, '')
  assert.equal(second.state.loading.value, false)
})

test('failed HTTP recovery remains unapplied and retries through stream recovery', async t => {
  const f = fixture(t)
  const { state } = f.mount()
  await until(() => f.reads.length === 1)
  f.reads[0]!.reject(new Error('temporarily unavailable'))
  await until(() => f.reads.length === 2)
  f.reads[1]!.resolve(sample)
  await until(() => !!state.value.value)
  assert.equal(f.sinks.length, 2)
  assert.equal(state.error.value, '')
  assert.equal(state.loading.value, false)
})

test('a delayed HTTP result cannot overwrite a newer synchronized snapshot', async t => {
  const f = fixture(t)
  const { state } = f.mount()
  await until(() => f.reads.length === 1)
  const newer = { ...sample, revision: 'saved-2', version: 'fixture:2' }
  state.accept(newer)
  f.reads[0]!.resolve(sample)
  await settle()
  assert.deepEqual(state.value.value, newer)
  assert.equal(state.error.value, '')
  assert.equal(state.loading.value, false)
})

test('save publishes through acceptance without returning a stale snapshot to its caller', async t => {
  const f = fixture(t)
  const { state } = f.mount(false)
  state.accept(sample)
  const profile = { id: 'https-proxy', name: 'HTTPS proxy', protocol: 'https' as const, server: 'proxy.example', port: 443, authentication: 'none' as const, insecureSkipVerify: true }
  const pending = state.save({ ...sample.config, profiles: [profile] }, sample.revision)
  await until(() => f.writes.length === 1)
  assert.equal(f.writes[0]!.body.config.profiles[0]!.insecureSkipVerify, true, 'the save projection must retain the proxy TLS setting')
  const newer = { ...sample, revision: 'saved-3', version: 'fixture:3',
    config: { ...sample.config, profiles: [{ id: 'new', name: 'New proxy', protocol: 'http' as const, server: 'proxy.example', port: 3128, authentication: 'none' as const }] } }
  state.accept(newer)
  f.writes[0]!.resolve({ ...sample, revision: 'saved-2', version: 'fixture:2' })
  assert.equal(await pending, undefined, 'callers must read the latest shared state after saving')
  assert.deepEqual(state.value.value, newer)
  assert.equal(state.error.value, '')
})

test('a manual retry keeps failures visible and can subsequently load the snapshot', async t => {
  const f = fixture(t)
  const { state } = f.mount(false)
  const failed = state.read()
  await until(() => f.reads.length === 1)
  f.reads[0]!.reject(new Error('temporarily unavailable'))
  await failed
  assert.equal(state.error.value, 'temporarily unavailable')
  assert.equal(state.loading.value, false)
  const retry = state.read()
  await until(() => f.reads.length === 2)
  f.reads[1]!.resolve(sample)
  await retry
  assert.deepEqual(state.value.value, sample)
  assert.equal(state.error.value, '')
  assert.equal(state.loading.value, false)
})
