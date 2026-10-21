import { closeSettings } from './settings-navigation'
import type { Page } from '@playwright/test'
import { test, expect } from './fixtures'

function gate() {
  let release!: () => void
  const wait = new Promise<void>(resolve => { release = resolve })
  return { wait, release }
}

async function activateSession(page: Page, title: string) {
  const drawer = page.locator('.mobile-sessions-trigger')
  if (await drawer.isVisible()) await drawer.click()
  await page.locator('.session-card').filter({ hasText: title }).click()
}

function message(id: string, runId: string, sequence: number, role: string, content: string) {
  return { id, runId, sequence, role, content, createdAt: new Date().toISOString() }
}

function frames(events: { sequence: number; kind: string; data: unknown }[]) {
  return events.map(event => `data: ${JSON.stringify(event)}\n\n`).join('')
}

test('switching sessions ignores a delayed history response and keeps the new draft', async ({ page, vista }) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
  const first = await vista.api('/sessions', { method: 'POST', body: '{}' })
  const second = await vista.api('/sessions', { method: 'POST', body: '{}' })
  await vista.api(`/sessions/${first.id}`, { method: 'PATCH', body: JSON.stringify({ title: 'Delayed session A' }) })
  await vista.api(`/sessions/${second.id}`, { method: 'PATCH', body: JSON.stringify({ title: 'Current session B' }) })
  const requested = gate()
  const releaseHistory = gate()
  const handled = gate()
  await page.route('**/api/sessions/**', async route => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (request.method() === 'GET' && path.endsWith('/main-outputs')) return route.fulfill({ json: [] })
    if (request.method() !== 'GET') return route.continue()
    if (path === `/api/sessions/${first.id}/messages`) {
      requested.release()
      await releaseHistory.wait
      try {
        await route.fulfill({ json: [message('late-a', 'run-a', 1, 'assistant', 'Late session A history')] })
      } finally { handled.release() }
      return
    }
    if (path === `/api/sessions/${second.id}/messages`) {
      return route.fulfill({ json: [message('current-b', 'run-b', 1, 'assistant', 'Current session B reply')] })
    }
    if (/\/(event-page|events)$/.test(path)) return route.fulfill({
      ...(path.endsWith('/events') ? { contentType: 'text/event-stream', body: '' } : { json: [] }),
    })
    return route.continue()
  })
  try {
    await page.goto(`${vista.origin}/sessions/${first.id}`)
    await requested.wait
    await activateSession(page, 'Current session B')
    await expect(page).toHaveURL(url => url.pathname === `/sessions/${second.id}`)
    await expect(page.locator('.messages article.assistant .markdown')).toHaveText('Current session B reply')
    await page.locator('.sender-card textarea').fill('Draft belongs to session B')
    releaseHistory.release()
    await handled.wait
    await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))
    await expect(page.locator('.conversation-header h2')).toHaveText('Current session B')
    await expect(page.locator('.messages article.assistant .markdown')).toHaveText('Current session B reply')
    await expect(page.locator('.messages')).not.toContainText('Late session A history')
    await expect(page.locator('.sender-card textarea')).toHaveValue('Draft belongs to session B')
    await expect(page.locator('.error')).toHaveCount(0)
  } finally { releaseHistory.release() }
})

test('settings overlay keeps the current session stream alive and closes back to it', async ({ page, vista }) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
  const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
  const runId = 'overlay-stream-run'
  const user = message('overlay-user', runId, 1, 'user', 'Controlled stream fixture')
  const reply = { ...message('overlay-reply', runId, 6, 'assistant', 'First fragment. Second fragment.'), outputTurn: 1 }
  const secondChunk = gate()
  const finish = gate()
  let streamRequests = 0
  let executing = true
  await page.route('**/api/sessions**', async route => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (request.method() === 'GET' && path.endsWith('/main-outputs')) return route.fulfill({ json: [] })
    if (request.method() !== 'GET') return route.continue()
    if (path === '/api/sessions') {
      const original = await route.fetch()
      return route.fulfill({ response: original, json: (await original.json()).map((row: { id: string }) =>
        row.id === session.id ? { ...row, executing } : row) })
    }
    if (path === `/api/sessions/${session.id}/messages`) return route.fulfill({ json: executing ? [user] : [user, reply] })
    if (path === `/api/sessions/${session.id}/event-page`) return route.fulfill({ json: [] })
    if (path === `/api/sessions/${session.id}/runs/${runId}/model-output`) {
      const content = 'First fragment. '
      return route.fulfill({ json: { content, offset: 0, nextOffset: content.length, availableBytes: content.length, lastSequence: 3 } })
    }
    if (path !== `/api/sessions/${session.id}/events`) return route.continue()
    streamRequests++
    if (streamRequests === 1) return route.fulfill({ contentType: 'text/event-stream', body: frames([
      { sequence: 2, kind: 'model_request', data: { runId, provider: 'openai-chat', id: vista.modelId, key: 'test', turn: 1 } },
      { sequence: 3, kind: 'model_delta', data: { runId, turn: 1, content: 'First fragment. ' } },
    ]) })
    if (streamRequests === 2) {
      await secondChunk.wait
      return route.fulfill({ contentType: 'text/event-stream', body: frames([
        { sequence: 4, kind: 'model_delta', data: { runId, turn: 1, content: 'Second fragment.' } },
      ]) })
    }
    if (streamRequests === 3) {
      await finish.wait
      executing = false
      return route.fulfill({ contentType: 'text/event-stream', body: frames([
        { sequence: 5, kind: 'model_result', data: { runId, turn: 1, modelResult: { provider: 'openai-chat', id: vista.modelId, finishReason: 'completed', durationMs: 200, usage: { inputTokens: 20, outputTokens: 5, cacheReadTokens: 0, reasoningTokens: 0 } } } },
        { sequence: 6, kind: 'run_finished', data: { runId, status: 'succeeded', message: reply } },
      ]) })
    }
    return route.fulfill({ contentType: 'text/event-stream', body: ': end of fixture\n\n' })
  })
  try {
    await page.goto(`${vista.origin}/sessions/${session.id}`)
    await expect(page.locator('.live-response .markdown')).toHaveText('First fragment.')
    const drawer = page.locator('.mobile-sessions-trigger')
    if (await drawer.isVisible()) await drawer.click()
    await page.getByRole('button', { name: 'Settings', exact: true }).click()
    await expect(page.locator('.settings-page')).toBeVisible()
    await expect(page).toHaveURL(url => url.pathname === (page.viewportSize()!.width <= 830 ? '/settings' : '/settings/general') && url.searchParams.get('session') === session.id)
    secondChunk.release()
    await expect(page.locator('.live-response .markdown')).toHaveText('First fragment. Second fragment.')
    await expect(page.locator('.settings-page')).toBeVisible()
    await closeSettings(page)
    await expect(page).toHaveURL(url => url.pathname === `/sessions/${session.id}`)
    await expect(page.locator('.live-response .markdown')).toHaveText('First fragment. Second fragment.')
    finish.release()
    await expect(page.locator('.messages article.assistant .markdown')).toHaveText(reply.content)
    await expect(page.locator('.live-response')).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Send', exact: true })).toBeDisabled()
    await expect(page.locator('.error')).toHaveCount(0)
  } finally { secondChunk.release(); finish.release() }
})

test('cancelling execution restores Send and allows the next message', async ({ page, vista }) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
  const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
  const runId = 'cancel-fixture-run'
  const user = message('cancel-user', runId, 1, 'user', 'Cancellation fixture')
  const initialEvents = [{ sequence: 2, kind: 'model_request', data: { runId, turn: 1, provider: 'openai-chat', id: vista.modelId, key: 'test' } }]
  let executing = true
  let cancelRequests = 0
  const sent: { content: string }[] = []
  await page.route('**/api/sessions**', async route => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (request.method() === 'GET' && path.endsWith('/main-outputs')) return route.fulfill({ json: [] })
    if (path === `/api/sessions/${session.id}/cancel` && request.method() === 'POST') {
      cancelRequests++
      executing = false
      return route.fulfill({ status: 204, body: '' })
    }
    if (path === `/api/sessions/${session.id}/messages` && request.method() === 'POST') {
      sent.push(request.postDataJSON())
      return route.fulfill({ json: { message: message('after-cancel', 'next-run', 4, 'user', 'After cancellation') } })
    }
    if (request.method() !== 'GET') return route.continue()
    if (path === '/api/sessions') {
      const original = await route.fetch()
      return route.fulfill({ response: original, json: (await original.json()).map((row: { id: string }) =>
        row.id === session.id ? { ...row, executing } : row) })
    }
    if (path === `/api/sessions/${session.id}/messages`) return route.fulfill({ json: [user] })
    if (path === `/api/sessions/${session.id}/event-page`) return route.fulfill({ json: initialEvents })
    if (path === `/api/sessions/${session.id}/events`) return route.fulfill({ contentType: 'text/event-stream', body: executing ? '' : frames([
      { sequence: 3, kind: 'run_finished', data: { runId, status: 'canceled', reason: 'Cancellation fixture' } },
    ]) })
    return route.continue()
  })
  await page.goto(`${vista.origin}/sessions/${session.id}`)
  const stop = page.getByRole('button', { name: 'Stop current execution', exact: true })
  await stop.scrollIntoViewIfNeeded()
  await expect(stop).toBeEnabled()
  await stop.click()
  await expect.poll(() => cancelRequests).toBe(1)
  await expect(stop).toHaveCount(0)
  const send = page.getByRole('button', { name: 'Send', exact: true })
  await expect(send).toBeDisabled()
  await page.locator('.sender-card textarea').fill('After cancellation')
  await send.scrollIntoViewIfNeeded()
  await expect(send).toBeEnabled()
  await send.click()
  await expect(page.locator('.messages article.user .markdown').last()).toHaveText('After cancellation')
  expect(sent).toHaveLength(1)
  expect(sent[0].content).toBe('After cancellation')
  await expect(page.locator('.sender-card textarea')).toHaveValue('')
  await expect(page.locator('.error')).toHaveCount(0)
})
