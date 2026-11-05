import type { Page } from '@playwright/test'
import { test, expect } from './fixtures'
import { selectSessions } from './session-manager-actions'

async function filterSessions(page: Page, name: string) {
  await page.getByRole('radio', { name, exact: true }).check()
}

for (const kind of ['open', 'close', 'delete'] as const) {
  test(`bulk ${kind} keeps rows, counts and pagination stable while SSE continues synchronizing`, async ({ page, vista }, info) => {
    await page.emulateMedia({ reducedMotion: 'no-preference' })
    for (let i = 0; i < 3; i++) {
      const item = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: `Batch ${i}` }) })
      if (kind === 'open') await vista.api(`/sessions/${item.id}/close`, { method: 'POST' })
    }
    await page.goto(vista.origin + '/sessions')
    const manager = page.locator('.session-manager-page')
    if (kind !== 'delete') await filterSessions(page, kind === 'open' ? 'Closed sessions' : 'Open sessions')
    const rows = manager.locator('.managed-session')
    await expect(rows).toHaveCount(3)
    await selectSessions(manager)
    const initialRows = await rows.allTextContents()
    const stats = manager.locator('.session-manager-filters')
    const initialStats = await stats.textContent()
    const range = manager.locator('.list-pagination [role="status"]')
    await expect(range).toHaveText('1–3 of 3')
    await page.evaluate(() => {
      const alerts: string[] = []
      const ratios: number[] = []
      const closingButtons: string[] = []
      let frame = 0
      const sample = () => {
        const track = document.querySelector('.batch-operation-progress-track')
        const fill = track?.querySelector('.batch-operation-progress-fill')
        if (track && fill && track.getBoundingClientRect().width) ratios.push(fill.getBoundingClientRect().width / track.getBoundingClientRect().width)
        frame = requestAnimationFrame(sample)
      }
      frame = requestAnimationFrame(sample)
      Object.assign(window, { batchClosingAlerts: alerts, batchProgressRatios: ratios, batchClosingButtons: closingButtons, stopBatchMotionObservation: () => cancelAnimationFrame(frame) })
      new MutationObserver(() => {
        for (const element of document.querySelectorAll('.batch-operation-modal .ui-alert-error')) {
          if (element.getClientRects().length) alerts.push(element.textContent || '')
        }
        for (const element of document.querySelectorAll('.batch-operation-modal .ui-modal-container[data-state="closed"]:has(.batch-operation-progress)')) {
          if (element.getClientRects().length) closingButtons.push(...[...element.querySelectorAll('button')].map(button => button.textContent || button.getAttribute('aria-label') || ''))
        }
      }).observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ['data-state'] })
    })
    let pageReads = 0
    let streams = 0
    page.on('request', request => {
      const url = new URL(request.url())
      if (request.method() === 'GET' && url.pathname === '/api/sessions' && url.searchParams.has('page')) pageReads++
      if (url.pathname === '/api/events') streams++
    })
    const releases: (() => void)[] = []
    await page.route(/\/api\/sessions\/[^/]+(?:\/(?:open|close))?$/, async route => {
      if (route.request().method() !== (kind === 'delete' ? 'DELETE' : 'POST')) return route.continue()
      const response = await route.fetch()
      await new Promise<void>(resolve => { releases.push(resolve) })
      await route.fulfill({ response })
    })
    try {
      await manager.getByRole('checkbox', { name: 'Select all shown', exact: true }).check()
      await manager.getByRole('button', { name: new RegExp(`^${kind[0]!.toUpperCase()}${kind.slice(1)} selected`) }).click()
      if (kind === 'delete') {
        const confirmation = page.getByRole('dialog', { name: 'Delete selected', exact: true })
        await expect(confirmation).toContainText('Delete 3 selected sessions and their history?')
        await expect(confirmation.getByRole('progressbar')).toHaveCount(0)
        expect(releases).toHaveLength(0)
        await page.keyboard.press('Escape')
        await expect(confirmation).toBeHidden()
        await expect(manager).toBeVisible()
        await expect(manager.locator('.session-selection-toolbar [role="status"]')).toHaveText('3 selected')
        await manager.getByRole('button', { name: 'Delete selected', exact: true }).click()
        await confirmation.evaluate(element => { element.setAttribute('data-confirmation-dialog', 'true') })
        await confirmation.getByRole('button', { name: 'Delete', exact: true }).click()
        await expect(confirmation).toHaveAttribute('data-confirmation-dialog', 'true')
      }
      const dialog = page.locator('.batch-operation-modal').getByRole('dialog')
      const progress = dialog.locator('.batch-operation-progress')
      await expect(dialog).toBeVisible()
      await expect(manager.getByRole('progressbar')).toHaveCount(0)
      await page.keyboard.press('Escape')
      await page.mouse.click(2, 2)
      await expect(dialog).toBeVisible()
      await expect(manager).toBeVisible()
      await expect(dialog.getByRole('button')).toHaveCount(0)
      const bounds = await dialog.boundingBox()
      expect(bounds!.x).toBeGreaterThanOrEqual(0)
      expect(bounds!.y).toBeGreaterThanOrEqual(0)
      expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(page.viewportSize()!.width)
      expect(bounds!.y + bounds!.height).toBeLessThanOrEqual(page.viewportSize()!.height)
      for (let i = 0; i < 3; i++) {
        await expect.poll(() => releases.length).toBe(i + 1)
        // Each write has committed, but its response is held. A paged read
        // here can only come from SSE; the loop has not advanced yet.
        await expect.poll(() => pageReads).toBeGreaterThan(i)
        await expect(progress).toHaveText(`Processed ${i} / 3`)
        await expect(progress.getByRole('progressbar')).toHaveAttribute('value', String(i))
        await expect(rows).toHaveText(initialRows)
        await expect(stats).toHaveText(initialStats!)
        await expect(range).toHaveText('1–3 of 3')
        await expect(manager.locator('.session-selection-toolbar [role="status"]')).toHaveText('3 selected')
        await expect(manager.getByText('Loading conversations…', { exact: true })).toHaveCount(0)
        if (i === 1) await page.screenshot({ path: info.outputPath(`batch-${kind}-progress.png`), animations: 'disabled' })
        releases[i]!()
      }
      await expect(dialog).toBeHidden()
      expect(await page.evaluate(() => (window as unknown as { batchClosingAlerts: string[] }).batchClosingAlerts), 'Successful batches must not flash an error during the closing animation').toEqual([])
      expect(await page.evaluate(() => (window as unknown as { batchClosingButtons: string[] }).batchClosingButtons), 'Successful batches retain the progress view throughout closing').toEqual([])
      expect(await page.evaluate(() => (window as unknown as { batchProgressRatios: number[] }).batchProgressRatios.some(ratio => ratio > .02 && ratio < .31 || ratio > .36 && ratio < .64 || ratio > .69 && ratio < .98)), 'Rendered fill must pass through intermediate widths instead of jumping between counts').toBe(true)
      await expect(rows).toHaveCount(0)
      await expect(manager.locator('.list-pagination')).toHaveCount(0)
      await expect(manager.getByRole('textbox', { name: 'Search sessions', exact: true })).toBeEnabled()
      await expect(page.locator('.state-sync-error')).toHaveCount(0)
      expect(streams, 'Batch processing does not reopen or unsubscribe the shared stream').toBe(0)
      await filterSessions(page, 'All sessions')
      await expect(rows).toHaveCount(kind === 'delete' ? 0 : 3)
    } finally {
      releases.forEach(release => release())
      await page.evaluate(() => (window as unknown as { stopBatchMotionObservation: () => void }).stopBatchMotionObservation())
    }
  })
}

test('batch progress respects reduced motion and responds to preference changes', async ({ page, vista }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  for (let i = 0; i < 2; i++) await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: `Reduced motion ${i}` }) })
  await page.goto(vista.origin + '/sessions')
  const manager = page.locator('.session-manager-page')
  await expect(manager.locator('.managed-session')).toHaveCount(2)
  await selectSessions(manager)
  const releases: (() => void)[] = []
  await page.route(/\/api\/sessions\/[^/]+\/close$/, async route => {
    const response = await route.fetch()
    await new Promise<void>(resolve => { releases.push(resolve) })
    await route.fulfill({ response })
  })
  try {
    await manager.getByRole('checkbox', { name: 'Select all shown', exact: true }).check()
    await manager.getByRole('button', { name: /^Close selected/ }).click()
    const dialog = page.getByRole('dialog', { name: 'Close selected', exact: true })
    const fill = dialog.locator('.batch-operation-progress-fill')
    await expect(fill).toHaveCSS('transition-property', 'none')
    await expect.poll(() => releases.length).toBe(1)
    releases[0]!()
    await expect.poll(() => releases.length).toBe(2)
    await expect(dialog.locator('.batch-operation-progress')).toHaveText('Processed 1 / 2')
    expect(await fill.evaluate(element => element.getBoundingClientRect().width / element.parentElement!.getBoundingClientRect().width)).toBeCloseTo(.5, 2)
    await page.emulateMedia({ reducedMotion: 'no-preference' })
    await expect(fill).toHaveCSS('transition-property', 'width')
    await expect(fill).toHaveCSS('transition-duration', '0.2s')
    releases[1]!()
    await expect(dialog).toBeHidden()
  } finally { releases.forEach(release => release()) }
})

test('batch failure details remain scrollable inside the secondary modal', async ({ page, vista }, info) => {
  await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Failed batch item' }) })
  await page.goto(vista.origin + '/sessions')
  const manager = page.locator('.session-manager-page')
  await expect(manager.locator('.managed-session')).toHaveCount(1)
  await selectSessions(manager)
  await page.route(/\/api\/sessions\/[^/]+\/close$/, route => route.fulfill({ status: 409, body: 'Deliberate batch failure. '.repeat(100) }))
  await manager.getByRole('checkbox', { name: 'Select all shown', exact: true }).check()
  await manager.getByRole('button', { name: /^Close selected/ }).click()
  const dialog = page.getByRole('dialog', { name: 'Close selected', exact: true })
  await expect(dialog.locator('.batch-operation-failures')).toContainText('Deliberate batch failure.')
  await expect(dialog.locator('.batch-operation-progress')).toHaveText('Processed 1 / 1')
  const bounds = await dialog.boundingBox()
  expect(bounds!.x).toBeGreaterThanOrEqual(0)
  expect(bounds!.y).toBeGreaterThanOrEqual(0)
  expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(page.viewportSize()!.width)
  expect(bounds!.y + bounds!.height).toBeLessThanOrEqual(page.viewportSize()!.height)
  expect(await dialog.locator('.ui-modal-body').evaluate(element => element.scrollHeight > element.clientHeight)).toBe(true)
  await expect(dialog.getByRole('button', { name: 'Close', exact: true }).last()).toBeInViewport()
  await page.screenshot({ path: info.outputPath('batch-failures.png'), animations: 'disabled' })
  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
  await expect(manager).toBeVisible()
  await expect(manager.getByRole('checkbox', { name: 'Select Failed batch item', exact: true })).toBeChecked()
  await expect(manager.getByRole('button', { name: /^Close selected/ })).toBeEnabled()
})

test('bulk failures and SSE reconnect preserve the frozen view and reveal remote changes at completion', async ({ page, vista }, info) => {
  test.skip(info.project.name !== 'desktop', 'Recovery and partial-result semantics are independent of viewport')
  await page.addInitScript(() => {
    const original = window.fetch.bind(window)
    let stream: AbortController | undefined
    Object.assign(window, { batchSyncTest: { disconnect: () => stream?.abort() } })
    window.fetch = (input, init) => {
      const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input : input.url, location.href)
      if (url.pathname !== '/api/events') return original(input, init)
      stream = new AbortController()
      return original(input, { ...init, signal: init?.signal ? AbortSignal.any([init.signal, stream.signal]) : stream.signal })
    }
  })
  const items = []
  for (let i = 0; i < 3; i++) items.push(await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: `Original ${i}` }) }))
  await page.goto(vista.origin + '/sessions')
  const manager = page.locator('.session-manager-page')
  await filterSessions(page, 'Open sessions')
  const rows = manager.locator('.managed-session')
  await expect(rows).toHaveCount(3)
  await selectSessions(manager)
  const titles = await rows.locator('.session-name').allTextContents()
  const failed = items.find(item => item.title === titles[1])!
  const initialRows = await rows.allTextContents()
  const initialStats = await manager.locator('.session-manager-filters').textContent()
  const releases: (() => void)[] = []
  let requests = 0
  let streams = 0
  page.on('request', request => { if (new URL(request.url()).pathname === '/api/events') streams++ })
  await page.route(/\/api\/sessions\/[^/]+\/close$/, async route => {
    requests++
    if (requests === 2) return route.fulfill({ status: 409, body: 'Deliberate batch test conflict' })
    const response = await route.fetch()
    await new Promise<void>(resolve => { releases.push(resolve) })
    await route.fulfill({ response })
  })
  try {
    await manager.getByRole('checkbox', { name: 'Select all shown', exact: true }).check()
    await manager.getByRole('button', { name: /^Close selected/ }).click()
    await expect.poll(() => releases.length).toBe(1)
    releases[0]!()
    await expect.poll(() => releases.length).toBe(2)
    await expect(page.locator('.batch-operation-progress')).toHaveText('Processed 2 / 3')
    await vista.api(`/sessions/${failed.id}`, { method: 'PATCH', body: JSON.stringify({ title: 'Renamed by another client' }) })
    await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Created by another client' }) })
    await page.evaluate(() => (window as unknown as { batchSyncTest: { disconnect: () => void } }).batchSyncTest.disconnect())
    await expect.poll(() => streams).toBe(1)
    // Workspace state recovers immediately; only this list's presentation is held.
    await expect(page.locator('.session-card')).toHaveCount(2)
    await expect(page.locator('.session-card').filter({ hasText: 'Renamed by another client' })).toHaveCount(1)
    await expect(rows).toHaveText(initialRows)
    await expect(manager.locator('.session-manager-filters')).toHaveText(initialStats!)
    await expect(manager.locator('.list-pagination [role="status"]')).toHaveText('1–3 of 3')
    await expect(page.locator('.batch-operation-failures')).toHaveCount(0)
    releases[1]!()
    const dialog = page.getByRole('dialog', { name: 'Close selected', exact: true })
    await expect(dialog).toBeVisible()
    await expect(dialog.locator('.batch-operation-progress')).toHaveText('Processed 3 / 3')
    await expect(rows).toHaveCount(2)
    await expect(rows.filter({ hasText: 'Renamed by another client' })).toHaveCount(1)
    await expect(rows.filter({ hasText: 'Created by another client' })).toHaveCount(1)
    await expect(dialog.locator('.batch-operation-failures')).toContainText('Deliberate batch test conflict')
    await expect(manager.locator('.batch-operation-failures')).toHaveCount(0)
    await dialog.getByRole('button', { name: 'Close', exact: true }).last().click()
    await expect(dialog).toBeHidden()
    await expect(manager.locator('.session-selection-toolbar [role="status"]')).toHaveText('1 selected')
    await expect(manager.getByRole('button', { name: /^Close selected/ })).toBeEnabled()
    await expect(manager.getByRole('checkbox', { name: 'Select Renamed by another client', exact: true })).toBeChecked()
    await vista.api(`/sessions/${failed.id}`, { method: 'PATCH', body: JSON.stringify({ title: 'Still synchronizing after batch' }) })
    await expect(rows.filter({ hasText: 'Still synchronizing after batch' })).toHaveCount(1)
    await expect(page.locator('.state-sync-error')).toHaveCount(0)
    expect(streams).toBe(1)
  } finally {
    releases.forEach(release => release())
  }
})

test('batch completion replaces an older in-flight page read before releasing the view', async ({ page, vista }, info) => {
  test.skip(info.project.name !== 'desktop', 'Read-ordering semantics are independent of viewport')
  for (let i = 0; i < 2; i++) await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: `Read order ${i}` }) })
  await page.goto(vista.origin + '/sessions')
  await filterSessions(page, 'Open sessions')
  const manager = page.locator('.session-manager-page')
  const rows = manager.locator('.managed-session')
  await expect(rows).toHaveCount(2)
  await selectSessions(manager)
  const mutations: (() => void)[] = []
  let releaseOldPage: (() => void) | undefined
  let releaseFinalPage: (() => void) | undefined
  let pageReads = 0
  let firstCommitted = false
  await page.route(/\/api\/sessions\/[^/]+\/close$/, async route => {
    const response = await route.fetch()
    firstCommitted = true
    await new Promise<void>(resolve => { mutations.push(resolve) })
    await route.fulfill({ response })
  })
  await page.route(/\/api\/sessions\?/, async route => {
    if (!firstCommitted) return route.continue()
    const response = await route.fetch()
    pageReads++
    if (pageReads === 1) {
      expect((await response.json()).items).toHaveLength(1)
      await new Promise<void>(resolve => { releaseOldPage = resolve })
    } else if (pageReads === 2) {
      await new Promise<void>(resolve => { releaseFinalPage = resolve })
    }
    await route.fulfill({ response }).catch(() => { /* The stale request is deliberately canceled. */ })
  })
  try {
    await manager.getByRole('checkbox', { name: 'Select all shown', exact: true }).check()
    await manager.getByRole('button', { name: /^Close selected/ }).click()
    await expect.poll(() => mutations.length).toBe(1)
    await expect.poll(() => !!releaseOldPage).toBe(true)
    mutations[0]!()
    await expect.poll(() => mutations.length).toBe(2)
    mutations[1]!()
    await expect.poll(() => !!releaseFinalPage).toBe(true)
    await expect(page.locator('.batch-operation-progress')).toHaveText('Processed 2 / 2')
    await expect(manager.locator('.session-selection-toolbar [role="status"]')).toHaveText('2 selected')
    await expect(rows).toHaveCount(2)
    releaseFinalPage!()
    await expect(page.getByRole('dialog', { name: 'Close selected', exact: true })).toBeHidden()
    expect(pageReads).toBeGreaterThanOrEqual(2)
    await expect(rows).toHaveCount(0)
    releaseOldPage!()
    await page.waitForTimeout(200)
    await expect(rows).toHaveCount(0)
    await expect(manager.getByRole('textbox', { name: 'Search sessions', exact: true })).toBeEnabled()
    await expect(page.locator('.state-sync-error')).toHaveCount(0)
  } finally {
    mutations.forEach(release => release())
    releaseOldPage?.()
    releaseFinalPage?.()
  }
})
