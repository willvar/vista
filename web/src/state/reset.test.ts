import assert from 'node:assert/strict'
import { test } from 'node:test'
import { clearVistaBrowserData } from './reset.ts'

function storage(entries: Record<string, string>) {
  const values = new Map(Object.entries(entries))
  return {
    values,
    get length() { return values.size },
    key(index: number) { return [...values.keys()][index] ?? null },
    removeItem(key: string) { values.delete(key) },
  }
}

test('reset clears only Vista preferences, drafts, question answers and cookies', t => {
  const local = storage({ 'vista-debug-mode': 'true', 'vista-session-draft:old': 'draft', 'vista-language': 'ja', unrelated: 'keep' })
  const session = storage({ 'vista-question:old:run': 'answers', 'vista-question:old:run:index': '2', unrelated: 'keep' })
  const cookies = new Map([['vista-debug-mode', 'true'], ['vista-last-model', 'old'], ['unrelated', 'keep']])
  const document = {
    get cookie() { return [...cookies].map(([key, value]) => `${key}=${value}`).join('; ') },
    set cookie(value: string) { cookies.delete(value.split('=')[0]!) },
  }
  for (const [name, value] of Object.entries({ localStorage: local, sessionStorage: session, document })) {
    const before = Object.getOwnPropertyDescriptor(globalThis, name)
    Object.defineProperty(globalThis, name, { configurable: true, value })
    t.after(() => { if (before) Object.defineProperty(globalThis, name, before); else Reflect.deleteProperty(globalThis, name) })
  }
  clearVistaBrowserData()
  assert.deepEqual([...local.values], [['unrelated', 'keep']])
  assert.deepEqual([...session.values], [['unrelated', 'keep']])
  assert.deepEqual([...cookies], [['unrelated', 'keep']])
})

test('reset still clears other storage and cookies when local storage is blocked', t => {
  const session = storage({ 'vista-question:old:run': 'answers' })
  const deleted: string[] = []
  for (const name of ['localStorage', 'sessionStorage', 'document']) {
    const before = Object.getOwnPropertyDescriptor(globalThis, name)
    t.after(() => { if (before) Object.defineProperty(globalThis, name, before); else Reflect.deleteProperty(globalThis, name) })
  }
  Object.defineProperty(globalThis, 'localStorage', { configurable: true, get() { throw new Error('disabled') } })
  Object.defineProperty(globalThis, 'sessionStorage', { configurable: true, value: session })
  Object.defineProperty(globalThis, 'document', { configurable: true, value: {
    get cookie() { return 'vista-language=fr; unrelated=keep' },
    set cookie(value: string) { deleted.push(value) },
  } })
  assert.doesNotThrow(clearVistaBrowserData)
  assert.equal(session.length, 0)
  assert.deepEqual(deleted, ['vista-language=; Path=/; Max-Age=0; SameSite=Strict'])
})
