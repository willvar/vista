import { installMockStateStream } from './state-stream-fixture'
import { coreSnapshot } from './conversation-fixture'
import { test, expect } from './frontend-fixture'
import type { Page } from '@playwright/test'
import type { CompactionStatus, Message, Session, StreamEvent } from '../src/client/types'

async function mockWorkspace(page: Page, origin: string, withTiers = false, canAdjustReasoning = true) {
  const session: Session = { id: 'iteration-session', title: 'Plan a small garden', modelKey: 'first', manualModelKey: 'first', manualReasoningEffort: 'high', open: true, running: true, executing: true, inputQueuePaused: false, createdAt: '2026-01-01T12:00:00Z' }
  const preferences = { userId: 'test-user', manualModelKey: 'first', reasoningByModelId: { 'model-alpha': 'high', 'model-beta': 'low' } }
  let conversationDefault = 'first'
  const models = [{ key: 'first', name: 'Example', provider: 'openai-chat', id: 'model-alpha', recognized: true, tools: true, processingTiers: withTiers ? ['standard', 'fast', 'ultrafast'] : [], reasoningOptions: canAdjustReasoning ? ['low', 'high'] : ['low'] }, { key: 'second', name: 'Example', provider: 'openai-chat', id: 'model-beta', recognized: true, tools: true, reasoningOptions: canAdjustReasoning ? ['low', 'high'] : ['low'], inputModalities: ['text', 'image'] }]
  let inputs = [{ id: 'pending-1', status: 'queued', content: 'Add drought-resistant plants and keep the budget under $100.' }, { id: 'pending-2', status: 'queued', content: 'Use the sunny corner.' }]
  const posts: any[] = []
  let sequence = 10
  const budget: CompactionStatus = { runId: 'run-1', phase: 'compacted', manual: false, sequence: 4 }
  const messages: Message[] = [{ id: 'user-1', runId: 'run-1', role: 'user', sequence: 1, content: 'Help me plan a small garden.', createdAt: '2026-01-01T12:00:00Z' }, { id: 'assistant-1', runId: 'run-1', role: 'assistant', sequence: 3, content: 'Start with herbs and native flowering plants.\n\n- Put sun-loving plants in the bright corner.\n- Leave a narrow path for access.', modelId: 'model-alpha', modelKey: 'first', createdAt: '2026-01-01T12:00:01Z' }]
  const events: StreamEvent[] = [{ sequence: 2, kind: 'model_request', data: { runId: 'run-1', turn: 1, id: 'model-alpha', key: 'first', provider: 'openai-chat' } }]
  await installMockStateStream(page)
  await page.addInitScript(() => {
    const original = window.fetch.bind(window)
    window.fetch = async (input, init) => {
      const path = new URL(typeof input === 'string' ? input : input instanceof URL ? input : input.url, location.href).pathname
      if (path !== '/api/sessions/iteration-session/events') return original(input, init)
      const snapshot = await (await original('/api/sessions/iteration-session/snapshot', { signal: init?.signal })).json()
      const encoder = new TextEncoder()
      let timer: ReturnType<typeof setInterval> | undefined
      let close: () => void
      const body = new ReadableStream<Uint8Array>({
        start(sink) {
          sink.enqueue(encoder.encode(`event: sync\ndata: ${JSON.stringify({ session: snapshot.session, throughSequence: snapshot.throughSequence })}\n\n`))
          let after = snapshot.throughSequence
          Object.assign(window, { pushIterationFixture(next: typeof snapshot) {
            for (const event of next.events) if (event.sequence > after) {
              sink.enqueue(encoder.encode(`event: record\ndata: ${JSON.stringify({ ...event, afterSequence: after })}\n\n`))
              after = event.sequence
            }
            sink.enqueue(encoder.encode(`event: sync\ndata: ${JSON.stringify({ session: next.session, throughSequence: next.throughSequence })}\n\n`))
          } })
          timer = setInterval(() => sink.enqueue(encoder.encode(': heartbeat\n\n')), 15_000)
          close = () => { clearInterval(timer); try { sink.close() } catch {} }
          init?.signal?.addEventListener('abort', close, { once: true })
        },
        cancel() { close(); init?.signal?.removeEventListener('abort', close) },
      })
      return new Response(body, { headers: { 'Content-Type': 'text/event-stream' } })
    }
  })
  await page.route('**/api/**', async route => {
    const request = route.request(); const path = new URL(request.url()).pathname.slice(4); const method = request.method()
    const body = method === 'GET' || method === 'DELETE' ? {} : request.postDataJSON() || {}
    const reply = (json: unknown) => route.fulfill({ json })
    if (path === '/preferences') {
      if (method === 'PATCH') Object.assign(preferences, body)
      return reply(preferences)
    }
    if (path === '/models') return reply({ default: 'first', conversationDefault, models })
    if (path === '/status') return reply({ state: 'running', sessions: { total: 1, open: 1, closed: 0, running: session.executing ? 1 : 0, workers: 1 }, workerMetrics: [{ sessionId: session.id, memory: { heapBytes: 1000000, rssBytes: 4000000, sampledAt: '2026-01-01T12:00:00Z' } }] })
    if (path === '/sessions') return reply([session])
    if (path.endsWith('/snapshot')) return reply(coreSnapshot(session, messages, events, inputs))
    if (path.endsWith('/overview')) return reply({ modelCalls: 1, throughSequence: events.at(-1)?.sequence || 0, startedAt: session.createdAt, events })
    if (path.endsWith('/selection')) {
      Object.assign(session, body)
      return reply(session)
    }
    if (path.endsWith('/context')) return reply(budget)
    if (path.endsWith('/context-usage')) return reply({ tokens: 5600, sequence: 4 })
    if (path.endsWith('/cancel') || path.endsWith('/inputs/resume')) {
      const paused = path.endsWith('/cancel')
      if (paused) session.executing = false
      session.inputQueuePaused = paused
      inputs = inputs.map(item => ({ ...item, status: paused ? 'paused' : 'queued' }))
      for (const input of inputs) events.push({ sequence: ++sequence, kind: 'input_status', data: { id: input.id, status: input.status } })
      const queueEvent = { sequence: ++sequence, kind: 'input_queue_state', data: { paused } }
      events.push(queueEvent)
      if (paused) events.push({ sequence: ++sequence, kind: 'run_finished', data: { runId: 'run-1', status: 'canceled' } })
      await reply({ event: queueEvent })
      await page.evaluate(snapshot => (window as any).pushIterationFixture(snapshot), coreSnapshot(session, messages, events, inputs))
      return
    }
    const inputID = path.match(/\/inputs\/([^/]+)/)?.[1]
    if (inputID) {
      const input = inputs.find(item => item.id === inputID)!
      if (!input) return route.fulfill({ status: 409, body: 'Already delivered' })
      if (method === 'PATCH') Object.assign(input, body)
      else inputs = inputs.filter(item => item.id !== inputID)
      const event = { sequence: ++sequence, kind: method === 'PATCH' ? 'input_updated' : 'input_status', data: { ...input, status: method === 'PATCH' ? input.status : 'deleted' } }
      events.push(event)
      return reply({ input, images: [], event })
    }
    if (path.endsWith('/inputs')) return reply(inputs)
    if (path.endsWith('/messages')) {
      if (method === 'POST') { posts.push(body); const input = { id: `pending-${++sequence}`, status: session.inputQueuePaused ? 'paused' : 'queued', content: body.content }; inputs.push(input); const event = { sequence, kind: 'input_queued', data: input }; events.push(event); return reply({ input, event }) }
      return reply(messages)
    }
    if (path.endsWith('/event-page')) return reply(events)
    if (path.endsWith('/events')) return route.fulfill({ contentType: 'text/event-stream', body: '' })
    return reply([])
  })
  await page.goto(origin + '/sessions/' + session.id)
  await expect(page.locator('.sender-card textarea')).toBeVisible()
  return { session, preferences, posts, budget, messages, events, setDefault: (model: string) => { conversationDefault = model } }
}
async function choose(page: Page, name: string, option: string) {
  const select = page.getByRole('combobox', { name, exact: true }); await select.click()
  await page.locator('.ui-select-dropdown:visible').getByText(option, { exact: true }).click()
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

test('configured defaults, pending controls, and context fit the viewport', async ({ page, mockOrigin }, testInfo) => {
  test.slow()
  const errors: string[] = []
  page.on('pageerror', error => errors.push(error.message))
  const fixture = await mockWorkspace(page, mockOrigin)
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page.locator('.composer-mode, .composer-model')).toHaveCount(0)
  const responseOptions = page.getByRole('button', { name: 'Response options', exact: true })
  const responsePanel = page.getByRole('dialog', { name: 'Response options', exact: true })
  await responseOptions.click()
  await expect(responsePanel.getByRole('combobox', { name: 'Reasoning effort', exact: true })).toHaveValue('High')
  await page.keyboard.press('Escape')
  await expect(responsePanel).toBeHidden()
  await expect(page.locator('.sender-attach')).toHaveCount(0)

  await expect(page.getByRole('button', { name: 'Rate answer', exact: true })).toHaveCount(0)
  const queueLayout = await page.locator('.pending-inputs').evaluate(element => {
    const column = element.closest('.conversation-column')!
    const sender = column.querySelector('.sender-card')!
    const execution = column.querySelector('.conversation-execution')
    const next = element.nextElementSibling
    return { inHistory: !!element.closest('.conversation-history'), adjacentComposer: !!(next?.matches('.sender-card') || next?.querySelector('.sender-card')),
      top: element.getBoundingClientRect().top, bottom: element.getBoundingClientRect().bottom,
      senderTop: sender.getBoundingClientRect().top, executionTop: execution?.getBoundingClientRect().top, executionInComposer: !!execution && sender.contains(execution) }
  })
  await expect(page.locator('.conversation-recent-message')).toHaveCount(0)
  expect(queueLayout.inHistory).toBe(false)
  expect(queueLayout.adjacentComposer).toBe(true)
  expect(queueLayout.bottom).toBeLessThanOrEqual(queueLayout.senderTop + 1)
  if (queueLayout.executionTop !== undefined) {
    expect(queueLayout.executionInComposer).toBe(true)
    expect(queueLayout.executionTop).toBeGreaterThanOrEqual(queueLayout.senderTop)
  }
  const first = page.locator('[data-input-id="pending-1"]')
  await first.getByRole('button', { name: 'Edit pending message', exact: true }).click()
  await first.getByRole('textbox', { name: 'Edit pending message', exact: true }).fill('Edited gardening note')
  await first.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(first).toContainText('Edited gardening note')
  await expect(first.getByRole('button', { name: 'Add to current task', exact: true })).toHaveCount(0)
  await expect(first).toContainText('Before the next model request')
  await first.getByRole('button', { name: 'Move back to draft', exact: true }).click()
  await expect(page.locator('.sender-card textarea')).toHaveValue('Edited gardening note')
  await expect(first).toHaveCount(0)
  await page.locator('.sender-card textarea').fill('Send this note')
  await page.locator('.sender-card textarea').press('Control+Enter')
  await expect.poll(() => fixture.posts.length).toBe(1)
  expect(fixture.posts[0]).not.toHaveProperty('mode')
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
  const context = page.locator('.conversation-header .context-compaction')
  await expect(header.getByRole('heading', { name: fixture.session.title, exact: true })).toBeVisible()
  await expect(context).toBeVisible()
  await expect(context.locator('.ui-progress')).toHaveCount(0)
  await expect(page.locator('.sender-card .context-status')).toHaveCount(0)
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
  await context.hover()
  const panel = page.locator('.context-usage-hint:visible')
  await expect(panel).toBeVisible()
  await expect(panel).toContainText('Estimated tokens from retained instructions, messages, summaries and tool content: 5.6K.')
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
  await page.keyboard.press('Escape')
  await expect(panel).toBeHidden()
  const draft = page.locator('.sender-card textarea')
  await draft.click({ position: { x: 8, y: 8 } })
  await expect(draft).toBeFocused()
  await expect(draft).toHaveValue('A follow-up draft stays ready while viewing context usage.')
  await page.screenshot({ path: testInfo.outputPath('context-composer.png'), animations: 'disabled' })
  await page.screenshot({ path: testInfo.outputPath('iteration.png'), fullPage: true, animations: 'disabled' })
  expect(errors).toEqual([])
})


test('processing tier follows the selected model without changing the active run', { tag: '@responsive' }, async ({ page, mockOrigin }) => {
  const fixture = await mockWorkspace(page, mockOrigin, true)
  const trigger = page.locator('.conversation-header').getByRole('button', { name: 'Response options', exact: true })
  const panel = page.getByRole('dialog', { name: 'Response options', exact: true })
  const tier = panel.getByRole('combobox', { name: 'Processing tier', exact: true })
  await expect(panel).toBeHidden()
  await expect(trigger).toHaveText('')
  await expect(page.locator('.sender-card').getByRole('combobox', { name: 'Processing tier' })).toHaveCount(0)
  await trigger.click()
  await expect(tier).toHaveValue('Default')
  await expect(panel.getByRole('combobox', { name: 'Reasoning effort', exact: true })).toBeVisible()
  await expect(tier).toHaveAccessibleDescription(/cost/i)
  await expect(panel.getByRole('slider')).toHaveCount(0)
  const controls = panel.locator('.ui-select-selector')
  await expect(controls).toHaveCount(2)
  const [reasoningBox, tierBox] = await controls.evaluateAll(elements => elements.map(element => {
    const box = element.getBoundingClientRect()
    return { left: box.left, width: box.width, height: box.height }
  }))
  expect(reasoningBox).toEqual(tierBox)
  expect(fixture.session.processingTier).toBeUndefined()
  await choose(page, 'Processing tier', 'Standard')
  await expect.poll(() => fixture.session.processingTier).toBe('standard')
  await expect(trigger).toHaveText('')
  await choose(page, 'Processing tier', 'Ultrafast')
  await expect.poll(() => fixture.session.processingTier).toBe('ultrafast')
  await tier.click()
  const list = page.locator('.ui-select-dropdown:visible')
  await expect(list).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(list).toBeHidden()
  await expect(panel).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(panel).toBeHidden()
  await expect(trigger).toHaveText('Ultrafast')
  await expect(trigger).toHaveAccessibleDescription('Ultrafast')
  expect(fixture.session.executing).toBe(true)
  await page.reload()
  await expect(trigger).toHaveText('Ultrafast')
  await trigger.click()
  await choose(page, 'Processing tier', 'Default')
  await expect.poll(() => fixture.session.processingTier).toBe('')
  await expect(trigger).toHaveText('')
  await choose(page, 'Processing tier', 'Ultrafast')
  await expect.poll(() => fixture.session.processingTier).toBe('ultrafast')
  fixture.setDefault('second')
  await page.reload()
  await expect(trigger).toHaveText('')
  await trigger.click()
  await expect(panel.getByRole('combobox', { name: 'Reasoning effort', exact: true })).toBeVisible()
  await expect(tier).toHaveCount(0)
  expect(fixture.session.executing).toBe(true)
  await expect(page.locator('.composer-model')).toHaveCount(0)
})

test('the response options entry requires at least one adjustable setting', { tag: '@responsive' }, async ({ page, mockOrigin }) => {
  const fixture = await mockWorkspace(page, mockOrigin, true, false)
  const trigger = page.getByRole('button', { name: 'Response options', exact: true })
  const panel = page.getByRole('dialog', { name: 'Response options', exact: true })
  await trigger.click()
  await expect(panel.getByRole('combobox', { name: 'Processing tier', exact: true })).toBeVisible()
  await expect(panel.getByRole('combobox', { name: 'Reasoning effort', exact: true })).toHaveCount(0)
  fixture.setDefault('second')
  await page.reload()
  await expect(page.locator('.sender-card textarea')).toBeEnabled()
  await expect(trigger).toHaveCount(0)
})
