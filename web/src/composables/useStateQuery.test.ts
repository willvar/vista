import assert from 'node:assert/strict'
import test from 'node:test'
import { effectScope } from 'vue'
import type { VistaClient } from '../client/vista.ts'
import { createWorkspaceSync, stateTopics } from '../client/workspaceSync.ts'
import { useStateQuery } from './useStateQuery.ts'

type Sync = ReturnType<typeof createWorkspaceSync>
type Receive = Parameters<Sync['subscribe']>[1]
function fixture(request: VistaClient['request']) {
  const client = { request } as VistaClient
  let receive: Receive | undefined
  const sync = { subscribe(_: unknown, callback: Receive) { receive = callback; return () => { receive = undefined } } } as unknown as Sync
  let scope = effectScope()
  const open = (maxAgeMs = Infinity) => { scope = effectScope(); return scope.run(() => useStateQuery<{ version: string; text: string }>(client, sync, '/skills', ['skills'], maxAgeMs))! }
  const point = async (revision = 0, epoch = 'a', signal = new AbortController().signal) => {
    const revisions = Object.fromEntries(stateTopics.map(topic => [topic, topic === 'skills' ? revision : 0])) as Parameters<Receive>[3]
    await receive?.(['skills'], signal, { epoch, revisions }, revisions)
  }
  return { open, point, close: () => scope.stop() }
}

test('matching revisions reuse inventory on reopen and closed views do not read changes', async () => {
  let reads = 0
  const f = fixture(async <T>() => { reads++; return { version: `a:${reads - 1}`, text: `value ${reads}` } as T })
  const view = f.open()
  await f.point(); await f.point(); assert.equal(reads, 1)
  assert.equal(view.data.value?.text, 'value 1')
  f.close(); await f.point(1); assert.equal(reads, 1)
  f.open(); await f.point(); assert.equal(reads, 1)
  await f.point(1); assert.equal(reads, 2)
  f.close()
})

test('new mutation results cancel old reads and late responses cannot replace them', async () => {
  let release!: (value: unknown) => void
  const old = new Promise(resolve => { release = resolve })
  const signals: AbortSignal[] = []
  const f = fixture(async <T>(_: string, init?: RequestInit) => { signals.push(init?.signal as AbortSignal); return await old as T })
  const view = f.open()
  const pending = f.point()
  view.apply({ version: 'a:2', text: 'saved result' })
  assert.equal(signals[0]!.aborted, true)
  release({ version: 'a:0', text: 'old response' }); await pending
  assert.equal(view.data.value?.text, 'saved result')
  await f.point(2); assert.equal(signals.length, 1)
  f.close()
})

test('failed shared reads remain recoverable and unmount cancels pending work', async () => {
  let reads = 0
  const f = fixture(async <T>() => { reads++; throw new Error('temporary read failure') })
  const view = f.open()
  const retry = view.refresh()
  await assert.rejects(f.point(), /temporary read failure/)
  await retry; assert.equal(reads, 1)
  assert.match(view.error.value, /temporary read failure/)
  await assert.rejects(f.point(), /temporary read failure/)
  assert.equal(reads, 2)
  f.close()
})

test('time-sensitive inventories stop reusing old cache after a closed view crosses its age bound', async t => {
  t.mock.timers.enable({ apis: ['Date'], now: Date.now() })
  let reads = 0
  const f = fixture(async <T>() => { reads++; return { version: 'a:0', text: 'rolling window' } as T })
  f.open(60 * 60_000); await f.point(); f.close()
  t.mock.timers.tick(61 * 60_000)
  f.open(60 * 60_000); await f.point()
  assert.equal(reads, 2)
  f.close()
})
