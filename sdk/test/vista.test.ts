import assert from 'node:assert/strict'
import test from 'node:test'
import { createVistaClient, VistaAPIError } from '../dist/vista.js'

test('API and image requests work without a local access token', async () => {
  const calls: string[] = []
  const client = createVistaClient({ fetch: async (path, options) => {
    calls.push(String(path))
    const headers = new Headers(options?.headers)
    assert.equal(headers.get('Authorization'), null)
    if (path === '/api/image') return new Response(new Uint8Array([1]), { headers: { 'Content-Type': 'image/png' } })
    assert.equal(headers.get('Content-Type'), 'application/json')
    return new Response('{"ok":true}')
  } })
  assert.deepEqual(await client.request('/sessions', { method: 'POST', body: '{}' }), { ok: true })
  assert.equal((await client.image('/image')).type, 'image/png')
  assert.deepEqual(calls, ['/api/sessions', '/api/image'])
})

test('API errors preserve stable recovery code, status and details independently of message text', async () => {
  const client = createVistaClient({ fetch: async () => new Response(JSON.stringify({
    code: 'model_ids_required', message: '请填写模型 ID', details: { models: [] },
  }), { status: 400, headers: { 'Content-Type': 'application/json' } }) })
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
    const client = createVistaClient({ fetch: async () => new Response(body, { status: 502 }) })
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
    const client = createVistaClient({ fetch: async () => new Response(body, { status: 400 }) })
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
  const emptyClient = createVistaClient({ fetch: async () => new Response(null, { status: 503, statusText: 'Service Unavailable' }) })
  await assert.rejects(() => emptyClient.request('/models'), (error: unknown) => {
    assert.ok(error instanceof VistaAPIError)
    assert.equal(error.message, 'Service Unavailable')
    assert.equal(error.status, 503)
    return true
  })
  const client = createVistaClient({ fetch: async () => new Response(JSON.stringify({ code: 'not_found', message: 'Image is gone' }), { status: 404 }) })
  await assert.rejects(() => client.image('/image'), (error: unknown) => {
    assert.ok(error instanceof VistaAPIError)
    assert.equal(error.code, 'not_found')
    assert.equal(error.status, 404)
    return true
  })
})

test('an application supplies the current language without changing message content', async () => {
  let language = 'zh-HK'
  const received: string[] = []
  const client = createVistaClient({ language: () => language, fetch: async (_path, options) => {
    received.push(new Headers(options?.headers).get('X-Vista-Language')!)
    assert.equal(options?.body, '{"content":"Original message"}')
    return new Response('{}')
  } })
  await client.request('/sessions/a/messages', { method: 'POST', body: '{"content":"Original message"}' })
  language = 'ja'
  await client.request('/sessions/b/messages', { method: 'POST', body: '{"content":"Original message"}' })
  assert.deepEqual(received, ['zh-HK', 'ja'])
})

test('HTTP, streamed requests, both SSE channels and image reads share the configured connection', { timeout: 2000 }, async () => {
  const signal = new AbortController()
  const session = new AbortController()
  const baseURL = 'https://vista.example.test/tenant/'
  const headers = new Headers({ 'X-Test-Client': 'one', 'X-Default': 'config' })
  const calls: { path: string; accept: string | null; override: string | null }[] = []
  const client = createVistaClient({ baseURL, headers, language: 'zh-CN', credentials: 'include', fetch: async (path, init) => {
    const options = new Headers(init?.headers)
    assert.equal(options.get('X-Test-Client'), 'one')
    assert.equal(options.get('X-Vista-Language'), 'zh-CN')
    assert.equal(init?.credentials, 'include')
    assert.ok(init.signal && !init.signal.aborted)
    const endpoint = new URL(String(path))
    assert.equal(endpoint.origin, 'https://vista.example.test')
    calls.push({ path: endpoint.pathname + endpoint.search, accept: options.get('Accept'), override: options.get('X-Default') })
    if (endpoint.pathname.endsWith('/image')) return new Response(Uint8Array.of(1), { headers: { 'Content-Type': 'image/png' } })
    if (options.get('Accept') === 'text/event-stream') return new Response('data: {"sequence":2,"kind":"message"}\n\n', { headers: { 'Content-Type': 'text/event-stream' } })
    return new Response('{"ok":true}')
  } })
  headers.set('X-Test-Client', 'mutated after construction')
  const other = createVistaClient({ baseURL: 'https://other.example.test', fetch: async (path, init) => {
    assert.equal(path, 'https://other.example.test/api/status')
    assert.equal(new Headers(init?.headers).get('X-Test-Client'), null)
    return new Response('{}')
  } })
  assert.equal(client.url('/image'), 'https://vista.example.test/tenant/api/image')
  await other.request('/status')
  await client.request('/status', { signal: signal.signal, headers: { 'x-default': 'request' } })
  const frames: { sequence: number }[] = []
  for await (const frame of client.requestStream<{ sequence: number }>('/diagnostic', { signal: signal.signal })) frames.push(frame)
  assert.deepEqual(frames.map(frame => frame.sequence), [2])
  await (await client.openWorkspaceEvents(true, signal.signal)).body?.cancel()
  await client.streamEvents('session/id', { signal: session.signal, after: () => 1, onEvent(event) {
    assert.equal(event.sequence, 2)
    session.abort()
  }, onError(error) { throw error } })
  assert.equal((await client.image('/image', signal.signal)).type, 'image/png')
  assert.deepEqual(calls, [
    { path: '/tenant/api/status', accept: null, override: 'request' },
    { path: '/tenant/api/diagnostic', accept: 'text/event-stream', override: 'config' },
    { path: '/tenant/api/events?metrics=1', accept: 'text/event-stream', override: 'config' },
    { path: '/tenant/api/sessions/session%2Fid/events?after=1', accept: 'text/event-stream', override: 'config' },
    { path: '/tenant/api/image', accept: null, override: 'config' },
  ])
})

test('base URL is a service address, not a credential-bearing or relative URL', () => {
  for (const baseURL of ['/api', 'ftp://example.test', 'https://user:password@example.test/', 'https://example.test/?query', 'https://example.test/#fragment']) {
    assert.throws(() => createVistaClient({ baseURL }), TypeError)
  }
  assert.throws(() => createVistaClient().url('//another.example.test'), TypeError)
})
