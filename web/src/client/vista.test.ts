import assert from 'node:assert/strict'
import test from 'node:test'
import { createVistaClient, VistaAPIError } from './vista.ts'

test('API and image requests work without a local access token', async () => {
  const calls: string[] = []
  const client = createVistaClient(async (path, options) => {
    calls.push(String(path))
    const headers = new Headers(options?.headers)
    assert.equal(headers.get('Authorization'), null)
    if (path === '/api/image') return new Response(new Uint8Array([1]), { headers: { 'Content-Type': 'image/png' } })
    assert.equal(headers.get('Content-Type'), 'application/json')
    return new Response('{"ok":true}')
  })
  assert.deepEqual(await client.request('/sessions', { method: 'POST', body: '{}' }), { ok: true })
  assert.equal((await client.image('/image')).type, 'image/png')
  assert.deepEqual(calls, ['/api/sessions', '/api/image'])
})

test('API errors preserve stable recovery code, status and details independently of message text', async () => {
  const client = createVistaClient(async () => new Response(JSON.stringify({
    code: 'model_ids_required', message: '请填写模型 ID', details: { models: [] },
  }), { status: 400, headers: { 'Content-Type': 'application/json' } }))
  await assert.rejects(() => client.request('/provider-config', { method: 'POST' }), (error: unknown) => {
    assert.ok(error instanceof VistaAPIError)
    assert.equal(error.message, '请填写模型 ID')
    assert.equal(error.status, 400)
    assert.equal(error.code, 'model_ids_required')
    assert.deepEqual(error.details, { models: [] })
    return true
  })
})

test('plain text, malformed JSON, JSON primitives and arrays remain ordinary API failures', async () => {
  for (const body of ['upstream unavailable', '{broken', 'null', 'false', '42', '"a JSON string"', '[]']) {
    const client = createVistaClient(async () => new Response(body, { status: 502 }))
    await assert.rejects(() => client.request('/provider-config'), (error: unknown) => {
      assert.ok(error instanceof VistaAPIError, body)
      assert.equal(error.message, body)
      assert.equal(error.status, 502)
      assert.equal(error.code, '')
      assert.equal(error.details, undefined)
      return true
    })
  }
})

test('invalid error fields cannot become recovery codes or details', async () => {
  for (const details of [null, [], 'retry', 1, true]) {
    const body = JSON.stringify({ code: 123, message: { value: 'wrong type' }, details })
    const client = createVistaClient(async () => new Response(body, { status: 400 }))
    await assert.rejects(() => client.request('/provider-config'), (error: unknown) => {
      assert.ok(error instanceof VistaAPIError)
      assert.equal(error.message, body)
      assert.equal(error.code, '')
      assert.equal(error.details, undefined)
      return true
    })
  }
})

test('empty failures retain HTTP status and image requests use the same error boundary', async () => {
  const emptyClient = createVistaClient(async () => new Response(null, { status: 503, statusText: 'Service Unavailable' }))
  await assert.rejects(() => emptyClient.request('/models'), (error: unknown) => {
    assert.ok(error instanceof VistaAPIError)
    assert.equal(error.message, 'Service Unavailable')
    assert.equal(error.status, 503)
    return true
  })
  const client = createVistaClient(async () => new Response(JSON.stringify({ code: 'not_found', message: 'Image is gone' }), { status: 404 }))
  await assert.rejects(() => client.image('/image'), (error: unknown) => {
    assert.ok(error instanceof VistaAPIError)
    assert.equal(error.code, 'not_found')
    assert.equal(error.status, 404)
    return true
  })
})

test('requests carry the current resolved UI language without changing message content', async t => {
  const element = { lang: 'zh-HK' }
  const previous = Object.getOwnPropertyDescriptor(globalThis, 'document')
  Object.defineProperty(globalThis, 'document', { configurable: true, value: { documentElement: element } })
  t.after(() => {
    if (previous) Object.defineProperty(globalThis, 'document', previous)
    else Reflect.deleteProperty(globalThis, 'document')
  })
  const received: string[] = []
  const client = createVistaClient(async (_path, options) => {
    received.push(new Headers(options?.headers).get('X-Vista-Language')!)
    assert.equal(options?.body, '{"content":"Original message"}')
    return new Response('{}')
  })
  await client.request('/sessions/a/messages', { method: 'POST', body: '{"content":"Original message"}' })
  element.lang = 'ja'
  await client.request('/sessions/b/messages', { method: 'POST', body: '{"content":"Original message"}' })
  assert.deepEqual(received, ['zh-HK', 'ja'])
})
