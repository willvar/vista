import type { Page } from '@playwright/test'
import { test, expect, type VistaFixture } from './fixtures'

test.use({ replyWithPrompt: true })
test.beforeEach(async ({ vista }) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
})

// Faults affect only browser delivery. The isolated Vista service continues to
// save real events from the simulated upstream; recovery never resubmits them.
async function deliveryFault(page: Page) {
  await page.addInitScript(() => {
    const original = window.fetch.bind(window)
    const audit = { streams: 0, drop: '', dropped: 0, checkpoints: 0 }
    Object.assign(window, { __conversationDelivery: audit })
    window.fetch = async (input, options) => {
      const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
      if (!/\/api\/sessions\/[^/]+\/events\?/.test(raw)) return original(input, options)
      if (++audit.streams > 1) audit.drop = ''
      const response = await original(input, options)
      if (!response.body) return response
      const decoder = new TextDecoder(), encoder = new TextEncoder()
      let buffer = ''
      const body = response.body.pipeThrough(new TransformStream({
        transform(chunk, sink) {
          buffer += decoder.decode(chunk, { stream: true })
          let end: number
          while ((end = buffer.indexOf('\n\n')) >= 0) {
            const frame = buffer.slice(0, end + 2); buffer = buffer.slice(end + 2)
            if (frame.includes('event: sync')) audit.checkpoints++
            if (audit.drop === 'silent' || audit.drop === 'records' && frame.includes('event: record')) { audit.dropped++; continue }
            sink.enqueue(encoder.encode(frame))
          }
        },
      }))
      return new Response(body, { status: response.status, headers: response.headers })
    }
  })
}
async function send(page: Page, vista: VistaFixture, id: string, round: number) {
  await page.locator('.sender-card textarea').fill(`Recovery round ${round}`)
  await page.locator('.sender-card textarea').press('Enter')
  await expect.poll(async () => (await vista.api(`/sessions/${id}/messages`)).filter((message: any) => message.role === 'assistant').length).toBe(round)
}
async function start(page: Page, vista: VistaFixture) {
  const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
  await page.goto(`${vista.origin}/sessions/${session.id}`)
  await send(page, vista, session.id, 1)
  await expect(page.locator('.messages article.assistant .markdown')).toHaveText('Mock reply: Recovery round 1')
  await expect(page.locator('.conversation-sync-notice')).toHaveCount(0)
  return session.id as string
}

test('four saved replies recover from a connected silent stream without reload or repeated model requests', async ({ page, vista }) => {
  await deliveryFault(page)
  await page.clock.install()
  const id = await start(page, vista)
  await page.evaluate(() => { (window as any).__conversationDelivery.drop = 'silent' })
  for (let i = 2; i <= 5; i++) await send(page, vista, id, i)
  await expect(page.locator('.messages article.assistant')).toHaveCount(1)
  await page.locator('.sender-card textarea').fill('Preserve this unsent draft')
  await page.clock.fastForward(46_000)
  await page.clock.runFor(1500)
  await expect(page.locator('.messages article.assistant .markdown')).toHaveText(Array.from({ length: 5 }, (_, i) => `Mock reply: Recovery round ${i + 1}`))
  await expect(page.locator('.sender-card textarea')).toHaveValue('Preserve this unsent draft')
  await expect(page.locator('.conversation-sync-notice')).toHaveCount(0)
  expect(vista.modelRequests).toHaveLength(5)
  const audit = await page.evaluate(() => (window as any).__conversationDelivery)
  expect(audit.streams).toBe(2)
  expect(audit.dropped).toBeGreaterThan(0)
})

test('reopening a conversation closed by another client restores delivery and preserves its draft', async ({ page, vista }) => {
  await deliveryFault(page)
  const id = await start(page, vista)
  await page.locator('.sender-card textarea').fill('Keep this draft while closed')
  await vista.api(`/sessions/${id}/close`, { method: 'POST' })
  await page.evaluate(() => window.dispatchEvent(new Event('online')))
  await expect(page.locator('.sender-card textarea')).toBeDisabled()
  const closedStreams = await page.evaluate(() => (window as any).__conversationDelivery.streams)
  await expect(page.locator('.conversation-sync-notice')).toHaveCount(0)
  await vista.api(`/sessions/${id}/open`, { method: 'POST' })
  await expect(page.locator('.sender-card textarea')).toBeEnabled()
  await expect(page.locator('.sender-card textarea')).toHaveValue('Keep this draft while closed')
  await expect.poll(() => page.evaluate(() => (window as any).__conversationDelivery.streams)).toBeGreaterThan(closedStreams)
  await send(page, vista, id, 2)
  await expect(page.locator('.messages article.assistant .markdown')).toHaveText(['Mock reply: Recovery round 1', 'Mock reply: Recovery round 2'])
  expect(vista.modelRequests).toHaveLength(2)
})

test('a valid checkpoint detects withheld records and replay failure falls back to a snapshot', async ({ page, vista }) => {
  await deliveryFault(page)
  let snapshotReads = 0, failedReplay = 0
  page.on('request', request => { if (new URL(request.url()).pathname.endsWith('/snapshot')) snapshotReads++ })
  await page.route('**/api/sessions/*/event-page?after=*', route => { failedReplay++; return route.fulfill({ status: 503, body: 'Injected replay failure' }) })
  const id = await start(page, vista)
  await page.evaluate(() => { (window as any).__conversationDelivery.drop = 'records' })
  await send(page, vista, id, 2)
  await expect(page.locator('.messages article.assistant .markdown')).toHaveText(['Mock reply: Recovery round 1', 'Mock reply: Recovery round 2'])
  await expect(page.locator('.conversation-sync-notice')).toHaveCount(0)
  expect(snapshotReads).toBe(2)
  expect(failedReplay).toBe(1)
  expect(vista.modelRequests).toHaveLength(2)
})

for (const failing of ['snapshot', 'context']) {
  test(`one failed initial ${failing} request cannot leave the conversation without a subscription`, async ({ page, vista }) => {
    let failures = 0, streams = 0
    await page.route(`**/api/sessions/*/${failing}`, async route => {
      if (!failures++) return route.fulfill({ status: 503, body: 'Injected initial failure' })
      return route.continue()
    })
    page.on('request', request => { if (/^\/api\/sessions\/[^/]+\/events$/.test(new URL(request.url()).pathname)) streams++ })
    const id = await start(page, vista)
    await send(page, vista, id, 2)
    await expect(page.locator('.messages article.assistant .markdown').last()).toHaveText('Mock reply: Recovery round 2')
    expect(streams).toBe(1)
    expect(vista.modelRequests).toHaveLength(2)
  })
}

test('an unrecoverable view read stays visible after a successful send and manual retry repairs it', async ({ page, vista }) => {
  await deliveryFault(page)
  await page.clock.install()
  const id = await start(page, vista)
  let failing = true
  await page.route('**/api/sessions/*/snapshot', route => failing ? route.fulfill({ status: 503, body: 'Injected unavailable snapshot' }) : route.continue())
  await page.route('**/api/sessions/*/event-page?after=*', route => failing ? route.fulfill({ status: 503, body: 'Injected unavailable replay' }) : route.continue())
  await page.evaluate(() => { (window as any).__conversationDelivery.drop = 'silent' })
  await send(page, vista, id, 2)
  await page.clock.fastForward(46_000)
  await page.clock.runFor(1500)
  await expect(page.locator('.conversation-sync-notice')).toContainText('could not be synchronized')
  await send(page, vista, id, 3)
  await expect(page.locator('.conversation-sync-notice')).toContainText('could not be synchronized')
  failing = false
  await page.getByRole('button', { name: 'Synchronize again', exact: true }).click()
  await expect(page.locator('.messages article.assistant .markdown')).toHaveText(Array.from({ length: 3 }, (_, i) => `Mock reply: Recovery round ${i + 1}`))
  await expect(page.locator('.conversation-sync-notice')).toHaveCount(0)
  expect(vista.modelRequests).toHaveLength(3)
})

test('restored connectivity reconciles saved replies immediately without waiting for inactivity', async ({ page, vista }) => {
  await deliveryFault(page)
  const id = await start(page, vista)
  await page.evaluate(() => { (window as any).__conversationDelivery.drop = 'silent' })
  await send(page, vista, id, 2)
  await expect(page.locator('.messages article.assistant')).toHaveCount(1)
  await page.evaluate(() => window.dispatchEvent(new Event('online')))
  await expect(page.locator('.messages article.assistant .markdown').last()).toHaveText('Mock reply: Recovery round 2')
  expect(vista.modelRequests).toHaveLength(2)
})

test('real server checkpoints keep a silent model healthy across two heartbeat intervals', async ({ page, vista }, info) => {
  test.skip(info.project.name !== 'desktop', 'One real-time transport control suffices; recovery UI is tested at every viewport')
  test.setTimeout(50_000)
  await deliveryFault(page)
  const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
  await page.goto(`${vista.origin}/sessions/${session.id}`)
  await expect.poll(() => page.evaluate(() => (window as any).__conversationDelivery.checkpoints)).toBe(1)
  const release = vista.pauseModelReplies()
  try {
    await page.locator('.sender-card textarea').fill('Wait for the upstream model')
    await page.locator('.sender-card textarea').press('Enter')
    await expect.poll(() => vista.modelRequests.length).toBe(1)
    await expect.poll(() => page.evaluate(() => (window as any).__conversationDelivery.checkpoints), { timeout: 34_000 }).toBeGreaterThanOrEqual(3)
    expect(await page.evaluate(() => (window as any).__conversationDelivery.streams)).toBe(1)
    await expect(page.locator('.conversation-sync-notice')).toHaveCount(0)
    await expect(page.locator('.messages article.assistant')).toHaveCount(0)
  } finally { release() }
  await expect(page.locator('.messages article.assistant .markdown')).toHaveText('Mock reply: Wait for the upstream model')
  expect(vista.modelRequests).toHaveLength(1)
})
