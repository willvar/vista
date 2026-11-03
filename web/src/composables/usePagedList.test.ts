import assert from 'node:assert/strict'
import test from 'node:test'
import { effectScope, nextTick, ref } from 'vue'
import type { PageInfo } from '../client/types.ts'
import type { VistaClient } from '../client/vista.ts'
import { createWorkspaceSync, stateTopics } from '../client/workspaceSync.ts'
import { usePagedList } from './usePagedList.ts'

const settle = async () => { await nextTick(); await new Promise(resolve => setImmediate(resolve)) }
type Sync = ReturnType<typeof createWorkspaceSync>
type Receive = Parameters<Sync['subscribe']>[1]
function fixture(request: VistaClient['request'], active = true) {
  let receive: Receive | undefined
  const enabled = ref(active)
  const page = ref(1)
  const scope = effectScope()
  const sync = { subscribe(_topics: unknown, callback: Receive) { receive = callback; return () => { receive = undefined } } } as unknown as Sync
  const client = { request } as VistaClient
  const resource = scope.run(() => usePagedList<PageInfo & { items: string[] }>({
    client, sync, page, topic: 'sessions', enabled: () => enabled.value,
    path: current => `/sessions?page=${current}&pageSize=10`,
  }))!
  const point = async (revision = 0, epoch = 'a') => {
    const revisions = Object.fromEntries(stateTopics.map(topic => [topic, topic === 'sessions' ? revision : 0])) as Parameters<Receive>[3]
    await receive?.(['sessions'], new AbortController().signal, { epoch, revisions }, revisions)
  }
  return { enabled, page, scope, resource, point }
}
const result = (page = 1, version = 'a:0') => ({ page, pageSize: 10, total: 30, version, items: [`page-${page}`] })

test('paged views load only while open and reuse local responses on a matching state revision', async () => {
  let reads = 0
  const f = fixture(async <T>() => { reads++; return result() as T }, false)
  await f.point()
  assert.equal(reads, 0)
  f.enabled.value = true
  await f.point()
  assert.equal(reads, 1)
  f.resource.apply(result(1, 'a:2'))
  await f.point(1)
  await f.point(2)
  assert.equal(reads, 1)
  f.resource.apply(result(1, 'a:1'))
  assert.equal(f.resource.data.value?.version, 'a:2')
  f.enabled.value = false
  await f.point(3)
  assert.equal(reads, 1)
  f.scope.stop()
})

test('rapid paging rejects late responses and server clamping does not trigger a duplicate read', async () => {
  let release!: (value: unknown) => void
  const old = new Promise(resolve => { release = resolve })
  const paths: string[] = []
  const signals: AbortSignal[] = []
  const f = fixture(async <T>(path: string, init?: RequestInit) => {
    paths.push(path); signals.push(init?.signal as AbortSignal)
    return (paths.length === 1 ? await old : result(path.includes('page=99') ? 3 : 2)) as T
  })
  const pending = f.point()
  f.page.value = 2
  await settle()
  assert.equal(signals[0]?.aborted, true)
  assert.deepEqual(f.resource.data.value?.items, ['page-2'])
  release(result())
  await pending
  assert.deepEqual(f.resource.data.value?.items, ['page-2'])
  f.page.value = 99
  await settle()
  assert.equal(f.page.value, 3)
  assert.equal(paths.length, 3)
  assert.deepEqual(f.resource.data.value?.items, ['page-3'])
  f.scope.stop()
})

test('revisions and epochs invalidate pages and concurrent reads share errors for stream recovery', async () => {
  let reads = 0
  let fail = false
  const f = fixture(async <T>() => { reads++; if (fail) throw new Error('read failed'); return result(1, reads > 2 ? 'b:0' : `a:${reads - 1}`) as T })
  await f.point()
  await f.point(1)
  assert.equal(reads, 2)
  await f.point(0, 'b')
  assert.equal(reads, 3)
  fail = true
  const manual = f.resource.refresh()
  await assert.rejects(f.point(1, 'b'), /read failed/)
  await manual
  assert.match(f.resource.error.value, /read failed/)
  assert.equal(reads, 4)
  f.scope.stop()
})
