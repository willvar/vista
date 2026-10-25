import { installMockStateStream } from './state-stream-fixture'
import { coreSnapshot } from './conversation-fixture'
import { closeSettings } from './settings-navigation'
import { test as base, expect, type Page } from '@playwright/test'
import { createServer } from 'vite'
import { resolve } from 'node:path'
import type { CompactionStatus, Message, Session, StreamEvent } from '../src/client/types'

const test = base.extend<{}, { mockOrigin: string }>({
  mockOrigin: [async ({}, use) => {
    const server = await createServer({ root: resolve(import.meta.dirname, '..'), configFile: resolve(import.meta.dirname, '../vite.config.ts'), server: { host: '127.0.0.1', port: 0, strictPort: false, watch: { ignored: ['**/.vista/**'] } } })
    await server.listen()
    const address = server.httpServer!.address()
    if (!address || typeof address === 'string') throw new Error('Mock UI server did not start')
    try { await use(`http://127.0.0.1:${address.port}`) } finally { await server.close() }
  }, { scope: 'worker' }],
})

async function mockWorkspace(page: Page, origin: string) {
  const session: Session = { id: 'iteration-session', title: 'Plan a small garden', modelKey: 'first', manualModelKey: 'first', manualReasoningEffort: 'high', selectionMode: 'manual', selectionTier: 'speed', selectionReasoningEffort: '', open: true, running: true, executing: true, inputQueuePaused: false, createdAt: '2026-01-01T12:00:00Z' }
  const preferences = { userId: 'test-user', lastMode: 'manual', lastTier: 'speed', manualModelKey: 'first', reasoningByModelId: { 'model-alpha': 'high', 'model-beta': 'low' }, feedbackInvitationSeen: false }
  const models = [{ key: 'first', name: 'Example', provider: 'openai-chat', id: 'model-alpha', tools: true, reasoningOptions: ['low', 'high'] }, { key: 'second', name: 'Example', provider: 'openai-chat', id: 'model-beta', tools: true, reasoningOptions: ['low', 'high'], inputModalities: ['text', 'image'] }]
  let inputs = [{ id: 'pending-1', mode: 'later', status: 'queued', content: 'Add drought-resistant plants and keep the budget under $100.' }, { id: 'pending-2', mode: 'now', status: 'queued', content: 'Use the sunny corner.' }]
  const feedback: Record<string, string> = {}
  const posts: any[] = []
  let sequence = 10
  const budget: CompactionStatus = { runId: 'run-1', phase: 'compacted', manual: false, sequence: 4 }
  const messages: Message[] = [{ id: 'user-1', runId: 'run-1', role: 'user', sequence: 1, content: 'Help me plan a small garden.', createdAt: '2026-01-01T12:00:00Z' }, { id: 'assistant-1', runId: 'run-1', role: 'assistant', sequence: 3, content: 'Start with herbs and native flowering plants.\n\n- Put sun-loving plants in the bright corner.\n- Leave a narrow path for access.', modelId: 'model-alpha', modelKey: 'first', createdAt: '2026-01-01T12:00:01Z' }]
  const events: StreamEvent[] = [{ sequence: 2, kind: 'model_request', data: { runId: 'run-1', turn: 1, id: 'model-alpha', key: 'first', provider: 'openai-chat' } }]
  await installMockStateStream(page)
  await page.route('**/api/**', async route => {
    const request = route.request(); const path = new URL(request.url()).pathname.slice(4); const method = request.method()
    const body = method === 'GET' || method === 'DELETE' ? {} : request.postDataJSON() || {}
    const reply = (json: unknown) => route.fulfill({ json })
    if (path === '/preferences') {
      if (method === 'PATCH') Object.assign(preferences, body)
      return reply(preferences)
    }
    if (path === '/models') return reply({ default: 'first', models })
    if (path === '/model-selection/stats') return reply({ speedScope: 'global', qualityScope: 'user', speedWindowDays: 30, qualityWindowSize: 200, models: [] })
    if (path === '/status') return reply({ state: 'running', sessions: { total: 1, open: 1, closed: 0, running: session.executing ? 1 : 0, workers: 1 }, workerMetrics: [{ sessionId: session.id, memory: { heapBytes: 1000000, rssBytes: 4000000, sampledAt: '2026-01-01T12:00:00Z' } }] })
    if (path === '/sessions') return reply([session])
    if (path.endsWith('/snapshot')) return reply(coreSnapshot(session, messages, events))
    if (path.endsWith('/selection')) {
      if (body.selectionMode && body.selectionMode !== session.selectionMode || body.selectionTier && body.selectionTier !== session.selectionTier) session.selectionReasoningEffort = ''
      Object.assign(session, body)
      return reply(session)
    }
    if (path.endsWith('/feedback')) {
      if (method === 'PUT') { if (body.value) feedback['assistant-1'] = body.value; else delete feedback['assistant-1']; return reply({ messageId: 'assistant-1', value: body.value, modelId: 'model-alpha' }) }
      return reply({ feedback })
    }
    if (path.endsWith('/context')) return reply(budget)
    if (path.endsWith('/cancel')) { session.executing = false; session.inputQueuePaused = true; inputs = inputs.map(item => ({ ...item, status: 'paused' })); return reply({}) }
    if (path.endsWith('/inputs/resume')) { session.inputQueuePaused = false; inputs = inputs.map(item => ({ ...item, status: 'queued' })); return reply({}) }
    const inputID = path.match(/\/inputs\/([^/]+)/)?.[1]
    if (inputID) {
      const input = inputs.find(item => item.id === inputID)!
      if (!input) return route.fulfill({ status: 409, body: 'Already delivered' })
      if (method === 'PATCH') Object.assign(input, body)
      else inputs = inputs.filter(item => item.id !== inputID)
      return reply({ input, images: [], event: { sequence: ++sequence, kind: method === 'PATCH' ? 'input_updated' : 'input_status', data: { ...input, status: method === 'PATCH' ? input.status : 'deleted' } } })
    }
    if (path.endsWith('/inputs')) return reply(inputs)
    if (path.endsWith('/messages')) {
      if (method === 'POST') { posts.push(body); const input = { id: `pending-${++sequence}`, mode: body.mode, status: session.inputQueuePaused ? 'paused' : 'queued', content: body.content }; inputs.push(input); return reply({ input }) }
      return reply(messages)
    }
    if (path.endsWith('/event-page')) return reply(events)
    if (path.endsWith('/events')) return route.fulfill({ contentType: 'text/event-stream', body: '' })
    return reply([])
  })
  await page.goto(origin + '/sessions/' + session.id)
  await expect(page.locator('.sender-card textarea')).toBeVisible()
  return { session, preferences, posts, feedback, budget, messages, events }
}
async function choose(page: Page, name: string, option: string) {
  const select = page.getByRole('combobox', { name, exact: true }); await select.click()
  await page.locator('.ant-select-dropdown:visible').getByText(option, { exact: true }).click()
}
async function withinViewport(page: Page) {
  const problems = await page.evaluate(() => {
    const viewport = { width: innerWidth, height: innerHeight }
    return ['.sender-card', '.sender-footer', '.pending-inputs', '.context-status'].flatMap(selector => {
      const box = document.querySelector(selector)?.getBoundingClientRect()
      return box && (box.left < -1 || box.right > viewport.width + 1) ? [selector] : []
    })
  })
  expect(problems).toEqual([])
}

test('pending deletion requires confirmation and Escape keeps the task running', async ({ page, mockOrigin }) => {
  const fixture = await mockWorkspace(page, mockOrigin)
  await page.getByRole('button', { name: 'Got it', exact: true }).click()
  const deletes: string[] = []
  const stops: string[] = []
  page.on('request', request => {
    const path = new URL(request.url()).pathname
    if (request.method() === 'DELETE' && path.includes('/inputs/')) deletes.push(path)
    if (path.endsWith('/cancel')) stops.push(path)
  })
  const item = page.locator('[data-input-id="pending-2"]')
  const remove = item.getByRole('button', { name: 'Delete pending message', exact: true })
  const confirmation = page.locator('.pending-delete-popover')
  await remove.click()
  await expect(confirmation.getByRole('alertdialog')).toContainText('Use the sunny corner.')
  expect(deletes).toEqual([])
  await confirmation.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(confirmation).toBeHidden()
  await expect(item).toBeVisible()
  expect(deletes).toEqual([])
  await remove.click()
  await expect(confirmation).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(confirmation).toBeHidden()
  await expect(item).toBeVisible()
  expect(stops).toEqual([])
  expect(fixture.session.executing).toBe(true)
  await remove.click()
  await expect(confirmation).toBeVisible()
  await confirmation.getByRole('button', { name: 'Delete', exact: true }).click()
  await expect(item).toHaveCount(0)
  expect(deletes).toEqual(['/api/sessions/iteration-session/inputs/pending-2'])
  expect(stops).toEqual([])
})

test('independent selection, voluntary feedback, pending controls, and context fit the viewport', async ({ page, mockOrigin }, testInfo) => {
  const fixture = await mockWorkspace(page, mockOrigin)
  const smallScreen = page.viewportSize()!.width <= 830
  await expect(page.getByRole('dialog')).toContainText('Help Vista learn what works for you')
  await expect(page.locator('.ant-select-dropdown:visible')).toHaveCount(0)
  const invitation = page.getByRole('dialog')
  const invitationBox = await invitation.boundingBox()
  expect(invitationBox!.width).toBeLessThanOrEqual(page.viewportSize()!.width)
  await page.screenshot({ path: testInfo.outputPath('feedback-invitation.png'), animations: 'disabled' })
  await page.getByRole('button', { name: 'Got it', exact: true }).click()
  await expect.poll(() => fixture.preferences.feedbackInvitationSeen).toBe(true)
  await expect(page.getByRole(smallScreen ? 'combobox' : 'slider', { name: 'Reasoning effort', exact: true })).toBeVisible()
  await expect(page.locator('.sender-attach')).toHaveCount(0)
  await expect(page.locator('.sender-card').getByRole('combobox', { name: 'Model selection', exact: true })).toHaveCount(0)
  await page.goto(mockOrigin + '/settings/general?session=iteration-session')
  await choose(page, 'Model selection', 'Automatic')
  await expect.poll(() => fixture.preferences.lastMode).toBe('auto')
  await page.reload()
  await expect(page.locator('.model-selection-setting')).toContainText('Automatic')
  await page.getByRole('combobox', { name: 'Model selection', exact: true }).scrollIntoViewIfNeeded()
  await page.screenshot({ path: testInfo.outputPath('model-selection-setting.png'), animations: 'disabled' })
  await closeSettings(page)
  await expect(page.locator('.settings-page')).toBeHidden()
  await expect(page.locator('.sender-card').getByRole('combobox')).toHaveCount(1)
  await expect(page.getByRole('combobox', { name: 'Model', exact: true })).toHaveCount(0)
  await expect(page.getByRole('slider', { name: 'Reasoning effort', exact: true })).toHaveCount(0)
  await expect(page.locator('.sender-attach')).toBeVisible()
  await choose(page, 'Preference', 'Quality')
  await page.reload()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page.locator('.composer-tier')).toContainText('Quality')
  await page.goto(mockOrigin + '/settings/general?session=iteration-session')
  await choose(page, 'Model selection', 'Choose model')
  await closeSettings(page)
  await expect(page.locator('.settings-page')).toBeHidden()
  await expect(page.locator('.sender-card').getByRole('combobox')).toHaveCount(smallScreen ? 2 : 1)
  await expect(page.locator('.composer-mode')).toHaveCount(0)
  await expect(page.locator('.composer-model')).toContainText('Example · model-alpha')
  await expect(page.locator('.composer-reasoning')).toContainText('High')
  await expect(page.locator('.sender-attach')).toHaveCount(0)
  await choose(page, 'Model', 'model-beta')
  await expect(page.locator('.sender-attach')).toBeVisible()
  await expect(page.locator('.composer-reasoning')).toContainText('Low')
  await choose(page, 'Model', 'model-alpha')
  await expect(page.locator('.sender-attach')).toHaveCount(0)
  await expect(page.locator('.composer-reasoning')).toContainText('High')
  await expect.poll(() => fixture.preferences.manualModelKey).toBe('first')

  await page.getByRole('button', { name: 'Helpful answer', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Helpful answer', exact: true })).toHaveAttribute('aria-pressed', 'true')
  await page.getByRole('button', { name: 'Unhelpful answer', exact: true }).click()
  await expect.poll(() => fixture.feedback['assistant-1']).toBe('down')
  await page.getByRole('button', { name: 'Unhelpful answer', exact: true }).click()
  await expect.poll(() => fixture.feedback['assistant-1']).toBeUndefined()

  const first = page.locator('[data-input-id="pending-1"]')
  await first.getByRole('button', { name: 'Edit pending message', exact: true }).click()
  await first.getByRole('textbox', { name: 'Edit pending message', exact: true }).fill('Edited gardening note')
  await first.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(first).toContainText('Edited gardening note')
  await first.getByRole('button', { name: 'Add to current task', exact: true }).click()
  await expect(first).toContainText('Before the next model request')
  await first.getByRole('button', { name: 'Move back to draft', exact: true }).click()
  await expect(page.locator('.sender-card textarea')).toHaveValue('Edited gardening note')
  await expect(first).toHaveCount(0)
  await page.locator('.sender-card textarea').fill('Queue this note')
  await page.locator('.sender-card textarea').press('Control+Enter')
  await expect.poll(() => fixture.posts.length).toBe(1)
  expect(fixture.posts[0].mode).toBe('later')
  await page.getByRole('button', { name: 'Stop current execution', exact: true }).click()
  await expect(page.locator('.pending-inputs')).toContainText('Pending messages paused')
  await page.getByRole('button', { name: 'Resume', exact: true }).click()
  await expect(page.locator('.pending-inputs')).not.toContainText('Pending messages paused')
  await page.locator('[data-input-id="pending-2"]').getByRole('button', { name: 'Delete pending message', exact: true }).click()
  await page.locator('.pending-delete-popover').getByRole('button', { name: 'Delete', exact: true }).click()
  await expect(page.locator('[data-input-id="pending-2"]')).toHaveCount(0)

  fixture.session.title = 'Plan a small garden with native plants, a narrow path, and a practical budget '.repeat(6).trim()
  await page.reload()
  const header = page.locator('.workspace:not(.modal-page) > .conversation-header')
  const context = page.locator('.sender-card .context-status')
  await expect(header.getByRole('heading', { name: fixture.session.title, exact: true })).toBeVisible()
  await expect(context).toBeVisible()
  await expect(context).toHaveAttribute('aria-expanded', 'false')
  await expect(context.locator('.ant-progress')).toHaveCount(0)
  await expect(context).toContainText('Context compacted')
  await expect(header.locator('.context-status')).toHaveCount(0)
  await page.locator('.sender-card textarea').fill('A follow-up draft stays ready while viewing context usage.')
  await expect(page.getByRole('button', { name: 'Send', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Send', exact: true })).toBeEnabled()
  // Finish textarea autosizing before measuring the popover's effect on layout.
  await page.locator('.sender-card').evaluate(async element => {
    await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
    await Promise.all(element.getAnimations({ subtree: true }).map(animation => animation.finished.catch(() => {})))
  })
  const headerBefore = await header.boundingBox()
  const composerBefore = await page.locator('.sender-card').boundingBox()
  await context.click()
  const panel = page.locator('.context-status-detail:visible')
  await expect(context).toHaveAttribute('aria-expanded', 'true')
  await expect(panel).toBeVisible()
  await expect(panel).toContainText('Context saved. The task continues automatically.')
  const headerAfter = await header.boundingBox()
  const composerAfter = await page.locator('.sender-card').boundingBox()
  expect(headerBefore).not.toBeNull()
  expect(headerAfter).not.toBeNull()
  expect(composerBefore).not.toBeNull()
  expect(composerAfter).not.toBeNull()
  for (const dimension of ['x', 'y', 'width', 'height'] as const) {
    expect(Math.abs(headerAfter![dimension] - headerBefore![dimension]), 'Context details must not resize the conversation header').toBeLessThanOrEqual(1)
    expect(Math.abs(composerAfter![dimension] - composerBefore![dimension]), 'Context details must not shift or resize the composer').toBeLessThanOrEqual(1)
  }
  const panelGeometry = await panel.evaluate(element => {
    const box = element.getBoundingClientRect()
    return { x: box.x, y: box.y, right: box.right, bottom: box.bottom, viewportWidth: innerWidth, viewportHeight: innerHeight, overflow: element.scrollWidth - element.clientWidth }
  })
  expect(panelGeometry.x).toBeGreaterThanOrEqual(-1)
  expect(panelGeometry.y).toBeGreaterThanOrEqual(-1)
  expect(panelGeometry.right).toBeLessThanOrEqual(panelGeometry.viewportWidth + 1)
  expect(panelGeometry.bottom).toBeLessThanOrEqual(panelGeometry.viewportHeight + 1)
  expect(panelGeometry.overflow).toBeLessThanOrEqual(2)
  await withinViewport(page)
  const sendBox = await page.getByRole('button', { name: 'Send', exact: true }).boundingBox()
  expect(sendBox!.y + sendBox!.height).toBeLessThanOrEqual(page.viewportSize()!.height + 1)
  await panel.getByText('Context saved. The task continues automatically.', { exact: true }).click()
  await expect(panel).toBeVisible()
  await context.click()
  await expect(panel).toBeHidden()
  await expect(context).toHaveAttribute('aria-expanded', 'false')
  await context.click()
  await expect(panel).toBeVisible()
  const draft = page.locator('.sender-card textarea')
  await draft.click()
  await expect(panel).toBeHidden()
  await expect(context).toHaveAttribute('aria-expanded', 'false')
  await expect(draft).toBeFocused()
  await expect(draft).toHaveValue('A follow-up draft stays ready while viewing context usage.')
  await context.click()
  await expect(panel).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(panel).toBeHidden()
  await expect(context).toHaveAttribute('aria-expanded', 'false')
  await context.click()
  await expect(panel).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath('context-composer.png'), animations: 'disabled' })
  await page.screenshot({ path: testInfo.outputPath('iteration.png'), fullPage: true, animations: 'disabled' })
})
