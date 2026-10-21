import { test as base, expect, type Page } from '@playwright/test'
import { createServer } from 'vite'
import { resolve } from 'node:path'
import type { ModelSelectionStats } from '../src/client/types'

const test = base.extend<{}, { mockOrigin: string }>({
  mockOrigin: [async ({}, use) => {
    const server = await createServer({
      root: resolve(import.meta.dirname, '..'),
      configFile: resolve(import.meta.dirname, '../vite.config.ts'),
      server: { host: '127.0.0.1', port: 0, strictPort: false, watch: { ignored: ['**/.vista/**'] } },
    })
    await server.listen()
    const address = server.httpServer!.address()
    if (!address || typeof address === 'string') throw new Error('Mock UI server did not start')
    try { await use(`http://127.0.0.1:${address.port}`) } finally { await server.close() }
  }, { scope: 'worker' }],
})

function statistics(): ModelSelectionStats {
  const result: ModelSelectionStats = {
    speedScope: 'global', qualityScope: 'user', speedWindowDays: 30, qualityWindowSize: 200,
    models: [
      {
        modelId: 'gpt-6-luna', reasoningEffort: 'high', providers: ['Example'],
        speed: { count: 12, failures: 2, medianMs: 1250, p90Ms: 2600, lowConfidence: false, buckets: [{ inputBucket: 0, count: 12, failures: 2, medianMs: 1250, p90Ms: 2600, endpointMatched: true, lowConfidence: false }] },
        quality: { up: 9, down: 3, count: 12, score: 0.4677, lowConfidence: false },
      },
      {
        modelId: 'a-model-with-an-extraordinarily-long-identifier-to-check-mobile-layout', reasoningEffort: 'max', providers: ['Another connection with a long display name'],
        speed: { count: 1, failures: 0, medianMs: 3400, p90Ms: 3400, lowConfidence: true, buckets: [{ inputBucket: 2, count: 1, failures: 0, medianMs: 3400, p90Ms: 3400, endpointMatched: false, lowConfidence: true }] },
        quality: { up: 1, down: 0, count: 1, score: 0.2065, lowConfidence: true },
      },
      {
        modelId: 'unseen-model', reasoningEffort: '', providers: ['Example'],
        speed: { count: 0, failures: 0, medianMs: null, p90Ms: null, lowConfidence: true, buckets: [] },
        quality: { up: 0, down: 0, count: 0, score: 0, lowConfidence: true },
      },
    ],
  }
  for (const row of result.models) {
    row.speed.buckets = [0, 1, 2, 3].map(inputBucket => row.speed.buckets.find(bucket => bucket.inputBucket === inputBucket) || {
      inputBucket, count: 0, failures: 0, medianMs: null, p90Ms: null, endpointMatched: false, lowConfidence: true,
    })
  }
  return result
}

async function mockSettings(page: Page, origin: string) {
  const session = { id: 'stats-session', title: 'Statistics fixture', modelKey: 'luna', manualModelKey: 'luna', manualReasoningEffort: 'high', selectionMode: 'manual', selectionTier: 'speed', open: true, running: false, executing: false, createdAt: '2026-01-01T12:00:00Z' }
  const preferences = { userId: 'test-user', lastMode: 'manual', lastTier: 'speed', manualModelKey: 'luna', reasoningByModelId: { 'gpt-6-luna': 'high' }, feedbackInvitationSeen: true }
  const stats = statistics()
  const resetScopes: string[] = []
  let statsReads = 0
  await page.route('**/api/**', async route => {
    const request = route.request()
    const path = new URL(request.url()).pathname.slice(4)
    const method = request.method()
    const body = request.postData() ? request.postDataJSON() : {}
    const reply = (json: unknown) => route.fulfill({ json })
    if (path === '/model-selection/stats') {
      if (method === 'DELETE') {
        resetScopes.push(body.scope)
        for (const row of stats.models) {
          if (body.scope === 'speed') row.speed = {
            count: 0, failures: 0, medianMs: null, p90Ms: null, lowConfidence: true,
            buckets: [0, 1, 2, 3].map(inputBucket => ({ inputBucket, count: 0, failures: 0, medianMs: null, p90Ms: null, endpointMatched: false, lowConfidence: true })),
          }
          if (body.scope === 'quality') row.quality = { up: 0, down: 0, count: 0, score: 0, lowConfidence: true }
        }
      } else statsReads++
      return reply(stats)
    }
    if (path === '/preferences') {
      if (method === 'PATCH') Object.assign(preferences, body)
      return reply(preferences)
    }
    if (path === '/models') return reply({ default: 'luna', models: [{ key: 'luna', name: 'Example', provider: 'openai-chat', id: 'gpt-6-luna', tools: true, reasoningOptions: ['low', 'high'] }] })
    if (path === '/agent-settings') return reply({ instructions: '', instructionsPath: '/home/test/.config/vista/AGENTS.md', maxBytes: 4096, delegationDepth: 1, maxDelegationDepth: 8 })
    if (path === '/status') return reply({ state: 'running', sessions: { total: 1, open: 1, closed: 0, running: 0, workers: 0 }, workerMetrics: [] })
    if (path === '/sessions') return reply([session])
    if (path.endsWith('/selection')) { Object.assign(session, body); return reply(session) }
    if (path.endsWith('/feedback')) return reply({ feedback: {} })
    if (path.endsWith('/context')) return reply(null)
    if (path.endsWith('/inputs') || path.endsWith('/messages') || path.endsWith('/event-page')) return reply([])
    if (path.endsWith('/events')) return route.fulfill({ contentType: 'text/event-stream', body: '' })
    if (path.endsWith('/summary')) return reply(null)
    return reply([])
  })
  await page.goto(`${origin}/settings/general?session=${session.id}`)
  await expect(page.getByRole('combobox', { name: 'Model selection', exact: true })).toBeEnabled()
  return { session, preferences, resetScopes, statsReads: () => statsReads }
}

async function chooseMode(page: Page, option: string) {
  await page.getByRole('combobox', { name: 'Model selection', exact: true }).click()
  await page.locator('.ant-select-dropdown:visible').getByText(option, { exact: true }).click()
}

async function settleLayout(page: Page) {
  await page.waitForTimeout(350)
  await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))
}

async function expectSettingsLayout(page: Page) {
  const layout = await page.locator('.settings-page:visible').evaluate(section => {
    const modal = section.closest('.ant-modal-container')!
    const bounds = modal.getBoundingClientRect()
    const content = section.querySelector('.settings-content')!
    return {
      x: bounds.x, y: bounds.y, right: bounds.right, bottom: bounds.bottom,
      width: bounds.width, height: bounds.height,
      viewportWidth: innerWidth, viewportHeight: innerHeight,
      pageWidth: document.documentElement.scrollWidth,
      modalOverflow: modal.scrollWidth - modal.clientWidth,
      contentOverflow: content.scrollWidth - content.clientWidth,
    }
  })
  expect(layout.x).toBeGreaterThanOrEqual(-1)
  expect(layout.y).toBeGreaterThanOrEqual(-1)
  expect(layout.right).toBeLessThanOrEqual(layout.viewportWidth + 1)
  expect(layout.bottom).toBeLessThanOrEqual(layout.viewportHeight + 1)
  expect(Math.abs(layout.x + layout.width / 2 - layout.viewportWidth / 2)).toBeLessThanOrEqual(2)
  expect(Math.abs(layout.y + layout.height / 2 - layout.viewportHeight / 2)).toBeLessThanOrEqual(2)
  expect(layout.pageWidth).toBeLessThanOrEqual(layout.viewportWidth + 2)
  expect(layout.modalOverflow).toBeLessThanOrEqual(2)
  expect(layout.contentOverflow).toBeLessThanOrEqual(2)
}

async function settingsGeometry(page: Page) {
  return page.locator('.settings-page:visible').evaluate(section => {
    const box = section.getBoundingClientRect()
    const content = section.querySelector('.settings-content')!
    return { x: box.x, y: box.y, width: box.width, height: box.height, scrollTop: content.scrollTop, scrollWidth: content.scrollWidth }
  })
}

async function expectConfirmationLayer(page: Page, selector: string) {
  const popup = page.locator(selector + ':visible')
  await expect(popup).toBeVisible()
  await settleLayout(page)
  const layer = await popup.evaluate(element => {
    const box = element.getBoundingClientRect()
    return {
      insideModal: !!element.closest('.ant-modal'),
      zIndex: Number(getComputedStyle(element).zIndex),
      x: box.x, y: box.y, right: box.right, bottom: box.bottom,
      viewportWidth: innerWidth, viewportHeight: innerHeight,
      horizontalOverflow: element.scrollWidth - element.clientWidth,
    }
  })
  expect(layer.insideModal, 'Confirmation belongs above, outside the Settings modal').toBe(false)
  expect(layer.zIndex).toBeGreaterThan(1000)
  expect(layer.x).toBeGreaterThanOrEqual(-1)
  expect(layer.y).toBeGreaterThanOrEqual(-1)
  expect(layer.right).toBeLessThanOrEqual(layer.viewportWidth + 1)
  expect(layer.bottom).toBeLessThanOrEqual(layer.viewportHeight + 1)
  expect(layer.horizontalOverflow).toBeLessThanOrEqual(2)
  return popup
}

test('automatic mode exposes scoped learning statistics and resets without disturbing Settings', async ({ page, mockOrigin }, testInfo) => {
  const fixture = await mockSettings(page, mockOrigin)
  const stats = page.locator('.model-selection-stats')
  await expect(stats).toHaveCount(0)
  expect(fixture.statsReads()).toBe(0)
  await expect(page.getByRole('combobox', { name: 'Follow-up behavior', exact: true })).toHaveCount(0)
  const settingStyles = await page.locator('.locale-settings, .model-selection-setting').evaluateAll(elements => elements.map(element => {
    const style = getComputedStyle(element)
    return { background: style.backgroundColor, separator: style.borderTopWidth, sideBorder: style.borderInlineStartWidth, padding: style.padding, margin: style.margin }
  }))
  expect(settingStyles).toHaveLength(2)
  expect(settingStyles[0]).toEqual(settingStyles[1])
  expect(settingStyles[0]!.background).toBe('rgba(0, 0, 0, 0)')
  expect(settingStyles[0]!.separator).toBe('1px')
  expect(settingStyles[0]!.sideBorder).toBe('0px')

  await chooseMode(page, 'Automatic')
  await expect.poll(() => fixture.preferences.lastMode).toBe('auto')
  await expect(stats).toBeVisible()
  await expect(stats).toContainText('Shared across users · last 30 days')
  await expect.poll(() => fixture.statsReads()).toBe(1)
  await expect(stats.locator('.selection-stats-model')).toHaveCount(3)
  const luna = stats.locator('[data-model-id="gpt-6-luna"]')
  const unknown = stats.locator('[data-model-id="unseen-model"]')
  const metric = (label: string) => luna.locator('dl > div').filter({ hasText: label }).locator('dd')
  await expect(metric('Median first text')).toHaveText('1.25 s')
  await expect(metric('P90 first text')).toHaveText('2.6 s')
  await expect(metric('Samples')).toHaveText('12')
  await expect(metric('Failures')).toHaveText('2')
  await expect(unknown).toContainText('Not measured')
  await expect(stats.locator('[data-model-id^="a-model"]')).toContainText('Few samples')

  const search = page.getByRole('textbox', { name: 'Search models or providers', exact: true })
  await search.fill('gpt-6-luna')
  await expect(stats.locator('.selection-stats-model')).toHaveCount(1)
  await search.fill('does-not-exist')
  await expect(stats).toContainText('No matching models.')
  await search.fill('')
  await page.getByRole('combobox', { name: 'Input length', exact: true }).click()
  await page.locator('.ant-select-dropdown:visible').getByText('Up to 4K tokens', { exact: true }).click()
  await expect(metric('Median first text')).toHaveText('1.25 s')
  await expect(stats.locator('[data-model-id^="a-model"]')).toContainText('Not measured')
  await page.getByRole('combobox', { name: 'Input length', exact: true }).click()
  await page.locator('.ant-select-dropdown:visible').getByText('All input lengths', { exact: true }).click()
  await settleLayout(page)
  await expectSettingsLayout(page)
  await stats.getByRole('heading', { name: 'Learning data', exact: true }).scrollIntoViewIfNeeded()
  await page.screenshot({ path: testInfo.outputPath('selection-speed-stats.png'), animations: 'disabled' })

  await page.reload()
  await expect(page.locator('.model-selection-setting')).toContainText('Automatic')
  await expect(stats).toBeVisible()
  await expect.poll(() => fixture.statsReads()).toBe(2)
  await stats.getByRole('button', { name: 'Refresh', exact: true }).click()
  await expect.poll(() => fixture.statsReads()).toBe(3)

  await stats.getByRole('tab', { name: 'Quality', exact: true }).click()
  await expect(stats).toContainText('Only your feedback · latest 200 ratings')
  await expect(page.getByRole('combobox', { name: 'Input length', exact: true })).toHaveCount(0)
  await expect(metric('Score / 100')).toHaveText('46.8')
  await expect(metric('Thumbs up')).toHaveText('9')
  await expect(metric('Thumbs down')).toHaveText('3')
  await expect(metric('Ratings')).toHaveText('12')
  await expect(unknown).toContainText('Not rated')
  await expectSettingsLayout(page)
  await stats.getByRole('heading', { name: 'Learning data', exact: true }).scrollIntoViewIfNeeded()
  await page.screenshot({ path: testInfo.outputPath('selection-quality-stats.png'), animations: 'disabled' })

  await stats.getByRole('tab', { name: 'Speed', exact: true }).click()
  const resetSpeed = stats.getByRole('button', { name: 'Reset speed data', exact: true })
  await resetSpeed.scrollIntoViewIfNeeded()
  const before = await settingsGeometry(page)
  await resetSpeed.click()
  let popup = await expectConfirmationLayer(page, '.selection-stats-reset-popover')
  expect(await settingsGeometry(page)).toEqual(before)
  await expect(popup).toContainText('Clear shared speed measurements')
  await page.screenshot({ path: testInfo.outputPath('selection-stats-confirmation.png'), animations: 'disabled' })
  await popup.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(popup).toBeHidden()
  expect(fixture.resetScopes).toEqual([])
  await expect(metric('Samples')).toHaveText('12')
  await resetSpeed.click()
  popup = await expectConfirmationLayer(page, '.selection-stats-reset-popover')
  await popup.getByRole('button', { name: 'Reset speed data', exact: true }).click()
  await expect.poll(() => fixture.resetScopes).toEqual(['speed'])
  await expect(popup).toBeHidden()
  await expect(luna).toContainText('Not measured')

  await stats.getByRole('tab', { name: 'Quality', exact: true }).click()
  await expect(metric('Ratings')).toHaveText('12')
  await expect(metric('Thumbs up')).toHaveText('9')
  await stats.getByRole('button', { name: 'Reset quality data', exact: true }).click()
  popup = await expectConfirmationLayer(page, '.selection-stats-reset-popover')
  await expect(popup).toContainText('Clear your thumbs up/down feedback')
  await popup.getByRole('button', { name: 'Reset quality data', exact: true }).click()
  await expect.poll(() => fixture.resetScopes).toEqual(['speed', 'quality'])
  await expect(popup).toBeHidden()
  await expect(luna).toContainText('Not rated')
  await expectSettingsLayout(page)

  await chooseMode(page, 'Choose model')
  await expect(stats).toHaveCount(0)
  await expect.poll(() => fixture.preferences.lastMode).toBe('manual')
  await page.reload()
  await expect(page.locator('.model-selection-setting')).toContainText('Choose model')
  await expect(stats).toHaveCount(0)
  expect(fixture.statsReads()).toBe(3)
})
