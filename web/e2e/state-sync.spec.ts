import type { Page } from '@playwright/test'
import { test, expect } from './fixtures'

test('workspace initializes each snapshot once and keeps one stream without periodic requests', async ({ page, vista }, info) => {
  test.skip(info.project.name !== 'desktop', 'Transport behavior is independent of viewport')
  test.setTimeout(45_000)
  await page.goto('about:blank')
  const reads: string[] = []
  page.on('request', request => {
    const path = new URL(request.url()).pathname
    if (request.method() === 'GET' && ['/api/models', '/api/sessions', '/api/status'].includes(path)) reads.push(path)
  })
  await page.goto(vista.origin + '/')
  await expect(page.locator('.sender-card textarea')).toBeVisible()
  await expect.poll(async () => (await vista.api('/status')).eventStreams).toBe(1)
  await page.waitForTimeout(700)
  expect([...reads].sort()).toEqual(['/api/models', '/api/sessions', '/api/status'])
  const settled = reads.length
  // Covers three former five-second polls and the SSE checkpoint heartbeat.
  await page.waitForTimeout(16_000)
  expect(reads.slice(settled)).toEqual([])
  await expect(page.locator('.state-sync-error')).toHaveCount(0)
})

test('another client changes the sidebar without refreshing the model inventory', async ({ page, vista }) => {
  const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
  await expect(page.locator(`.session-card[data-session-id="${session.id}"]`)).toBeAttached()
  await page.waitForTimeout(300)
  let modelReads = 0
  page.on('request', request => { if (new URL(request.url()).pathname === '/api/models') modelReads++ })
  await vista.api(`/sessions/${session.id}`, { method: 'PATCH', body: JSON.stringify({ title: 'Changed in another client' }) })
  await expect(page.locator(`.session-card[data-session-id="${session.id}"]`)).toContainText('Changed in another client')
  expect(modelReads).toBe(0)
})

test('reading a reply in another client clears unread without querying workspace metadata', async ({ page, vista }, info) => {
  test.skip(info.project.name !== 'desktop', 'Request counts are independent of viewport')
  const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
  await vista.api(`/sessions/${session.id}`, { method: 'PATCH', body: JSON.stringify({ title: 'Unread synchronization' }) })
  await vista.api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'Reply in another client' }) })
  const row = page.locator(`.session-card[data-session-id="${session.id}"]`)
  await expect(row.locator('.unread.session-indicator')).toBeAttached()
  await page.waitForTimeout(350)
  const reads: string[] = []
  page.on('request', request => {
    const path = new URL(request.url()).pathname
    if (request.method() === 'GET' && ['/api/models', '/api/sessions', '/api/status'].includes(path)) reads.push(path)
  })
  const reply = (await vista.api(`/sessions/${session.id}/messages`)).find((message: { role: string }) => message.role === 'assistant')
  await vista.api(`/sessions/${session.id}/read`, { method: 'POST', body: JSON.stringify({ sequence: reply.sequence }) })
  await expect(row.locator('.unread.session-indicator')).toHaveCount(0)
  await page.waitForTimeout(350)
  expect(reads).toEqual([])
})

test.describe('message lifecycle request counts', () => {
  test.use({ progressCalls: 3 })

  test('tool progress keeps replies live without refreshing workspace state for each step', async ({ page, vista }, info) => {
    test.skip(info.project.name !== 'desktop', 'Request counts are independent of viewport')
    await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true, manualModelKey: 'test' }) })
    const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
    await vista.api(`/sessions/${session.id}`, { method: 'PATCH', body: JSON.stringify({ title: 'Message request counts' }) })
    await page.goto(`${vista.origin}/sessions/${session.id}`)
    const sender = page.locator('.sender-card textarea')
    await expect(sender).toBeEnabled()
    await page.waitForTimeout(350)
    const requests: string[] = []
    page.on('request', request => { requests.push(request.method() + ' ' + new URL(request.url()).pathname) })
    const count = (path: string) => requests.filter(request => request === path).length
    const release = vista.pauseModelReplies()
    let started: { sessions: number; status: number }
    try {
      await sender.fill('Measure a tool-driven turn')
      await sender.press('Enter')
      await expect.poll(() => vista.modelRequests.length).toBe(1)
      await expect(page.locator(`.session-card[data-session-id="${session.id}"] .session-indicator.running`)).toBeAttached()
      await page.waitForTimeout(350)
      started = { sessions: count('GET /api/sessions'), status: count('GET /api/status') }
    } finally { release() }
    await expect(page.locator('.messages article.assistant:not(.main-output) .markdown')).toHaveText('Final progress answer.')
    await expect(page.locator(`.session-card[data-session-id="${session.id}"] .session-indicator.running`)).toHaveCount(0)
    await page.waitForTimeout(350)
    const totals = { sessions: count('GET /api/sessions'), status: count('GET /api/status'), models: count('GET /api/models'), preferences: count('PATCH /api/preferences') }
    await info.attach('message-request-counts', { body: JSON.stringify({ started, totals, requests }, null, 2), contentType: 'application/json' })
    expect(started).toEqual({ sessions: 0, status: 0 })
    expect(totals.status).toBe(0)
    expect(totals.sessions).toBe(0)
    expect(totals.models).toBe(0)
    expect(totals.preferences).toBe(0)
    expect(vista.modelRequests).toHaveLength(4)
    await expect(page.locator('.state-sync-error, .conversation-sync-notice')).toHaveCount(0)
  })
})

async function stateDeliveryFault(page: Page) {
  await page.addInitScript(() => {
    const original = window.fetch.bind(window)
    const control = { drop: false, opened: 0 }
    Object.assign(window, { stateStreamTest: control })
    window.fetch = async (input, init) => {
      const response = await original(input, init)
      const path = new URL(typeof input === 'string' ? input : input instanceof URL ? input : input.url, location.href).pathname
      if (path !== '/api/events' || !response.body) return response
      control.opened++
      const reader = response.body.getReader()
      return new Response(new ReadableStream<Uint8Array>({
        async start(sink) {
          try {
            while (true) {
              const result = await reader.read()
              if (result.done) { sink.close(); return }
              if (!control.drop) sink.enqueue(result.value)
            }
          } catch (error) { try { sink.error(error) } catch {} }
          finally { reader.releaseLock() }
        },
        cancel() { return reader.cancel().catch(() => {}) },
      }), { headers: response.headers, status: response.status })
    }
  })
}

test.describe('new conversations', () => {
  test.use({ replyWithPrompt: true })

  test('first and subsequent replies update the sidebar without sessions or status reads', async ({ page, vista }, info) => {
    test.skip(info.project.name !== 'desktop', 'Request counts are independent of viewport')
    await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
    await page.goto(vista.origin + '/')
    await expect(page.locator('.sender-card textarea')).toBeEnabled()
    await page.waitForTimeout(350)
    const reads: string[] = []
    page.on('request', request => {
      const path = new URL(request.url()).pathname
      if (request.method() === 'GET' && ['/api/models', '/api/sessions', '/api/status'].includes(path)) reads.push(path)
    })
    const replies = page.locator('.messages article.assistant:not(.main-output) .markdown')
    for (const content of ['First pushed turn', 'Second pushed turn']) {
      await page.locator('.sender-card textarea').fill(content)
      await page.locator('.sender-card textarea').press('Enter')
      await expect(replies.last()).toHaveText('Mock reply: ' + content)
      await expect(page.getByRole('button', { name: 'Send', exact: true })).toBeVisible()
    }
    await expect(replies).toHaveCount(2)
    await expect(page.locator('.session-card .session-indicator.running')).toHaveCount(0)
    await expect(page.locator('.session-card').first()).toContainText('E2E conversation')
    await page.waitForTimeout(350)
    expect(reads).toEqual([])
    expect(vista.modelRequests).toHaveLength(2)
    await expect(page.locator('.state-sync-error, .conversation-sync-notice')).toHaveCount(0)
  })
})

test('a heartbeat repairs a lost state payload without reconnecting or polling unchanged topics', async ({ page, vista }, info) => {
  test.skip(info.project.name !== 'desktop', 'Transport recovery is independent of viewport')
  test.setTimeout(30_000)
  const other = await vista.api('/sessions', { method: 'POST', body: '{}' })
  await stateDeliveryFault(page)
  await page.goto(vista.origin + '/')
  const row = page.locator(`.session-card[data-session-id="${other.id}"]`)
  await expect(row).toBeAttached()
  await page.waitForTimeout(350)
  const reads: string[] = []
  page.on('request', request => {
    const path = new URL(request.url()).pathname
    if (request.method() === 'GET' && ['/api/models', '/api/sessions', '/api/status'].includes(path)) reads.push(path)
  })
  await page.evaluate(() => { (window as unknown as { stateStreamTest: { drop: boolean } }).stateStreamTest.drop = true })
  await vista.api(`/sessions/${other.id}`, { method: 'PATCH', body: JSON.stringify({ title: 'Repaired by heartbeat' }) })
  await page.waitForTimeout(350)
  await expect(row).not.toContainText('Repaired by heartbeat')
  await page.evaluate(() => { (window as unknown as { stateStreamTest: { drop: boolean } }).stateStreamTest.drop = false })
  await expect(row).toContainText('Repaired by heartbeat', { timeout: 20_000 })
  expect(reads).toEqual(['/api/sessions'])
  expect(await page.evaluate(() => (window as unknown as { stateStreamTest: { opened: number } }).stateStreamTest.opened)).toBe(1)
  await expect(page.locator('.state-sync-error')).toHaveCount(0)
})

test('foreground recovery reconciles withheld global updates and preserves the current draft', async ({ page, vista }) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
  const selected = await vista.api('/sessions', { method: 'POST', body: '{}' })
  const other = await vista.api('/sessions', { method: 'POST', body: '{}' })
  await stateDeliveryFault(page)
  await page.goto(`${vista.origin}/sessions/${selected.id}`)
  await expect(page.locator('.sender-card textarea')).toBeVisible()
  await page.locator('.sender-card textarea').fill('Preserve this draft through reconnection')
  const row = page.locator(`.session-card[data-session-id="${other.id}"]`)
  await expect(row).toBeAttached()
  await page.waitForTimeout(300)
  await page.evaluate(() => { (window as unknown as { stateStreamTest: { drop: boolean } }).stateStreamTest.drop = true })
  await vista.api(`/sessions/${other.id}`, { method: 'PATCH', body: JSON.stringify({ title: 'Recovered global change' }) })
  await page.waitForTimeout(300)
  await expect(row).not.toContainText('Recovered global change')
  await page.evaluate(() => {
    (window as unknown as { stateStreamTest: { drop: boolean } }).stateStreamTest.drop = false
    window.dispatchEvent(new Event('online'))
  })
  await expect(row).toContainText('Recovered global change')
  await expect(page.locator('.sender-card textarea')).toHaveValue('Preserve this draft through reconnection')
  await expect(page.locator('.state-sync-error')).toHaveCount(0)
  await expect.poll(() => page.evaluate(() => (window as unknown as { stateStreamTest: { opened: number } }).stateStreamTest.opened)).toBeGreaterThan(1)
})
