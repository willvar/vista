import { test, expect } from './fixtures'
import type { ModelDiagnosticRecord, ModelDiagnosticSummary } from '../src/client/modelDiagnostics'

test.describe('captured model HTTP', () => {
  test.use({ progressCalls: 1 })

  test('an open HTTP view receives new saved indexes without rereading its index or selected body', async ({ page, vista }) => {
    await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
    await page.evaluate(() => { localStorage.setItem('vista-debug-mode', 'true'); document.cookie = 'vista-debug-mode=true; Path=/' })
    await page.reload()
    const release = vista.pauseModelReplies()
    try {
      await page.locator('.sender-card textarea').fill('Keep the diagnostic view live')
      await page.locator('.sender-card textarea').press('Enter')
      await expect.poll(() => vista.modelRequests.length).toBe(1)
      await page.getByRole('button', { name: 'Debug details', exact: true }).click()
      const modal = page.getByRole('dialog', { name: 'Debug details', exact: true })
      await modal.getByRole('tab', { name: 'Model HTTP', exact: true }).click()
      await expect(modal.locator('.trace-model-record')).toContainText('"purpose": "title"')
      const reads: string[] = []
      page.on('request', request => {
        const path = new URL(request.url()).pathname
        if (request.method() === 'GET' && /\/diagnostics(?:\/[^/]+)?$/.test(path)) reads.push(path)
      })
      release()
      await expect(page.locator('article.assistant').last()).toContainText('Final progress answer.')
      const select = modal.getByRole('combobox', { name: 'Select model request' })
      await select.fill('conversation')
      await expect(page.locator('.ant-select-item-option-content').filter({ hasText: /conversation.*main \/ [12]/ })).toHaveCount(2)
      await expect(modal.locator('.trace-model-record')).toContainText('"purpose": "title"')
      expect(reads).toEqual([])
      await page.locator('.ant-select-item-option-content').filter({ hasText: /conversation.*main \/ 1/ }).click()
      await expect(modal.locator('.trace-model-record')).toContainText('Keep the diagnostic view live')
      expect(reads).toHaveLength(1)
      await expect(page.locator('.state-sync-error')).toHaveCount(0)
    } finally { release() }
  })

  test('recovery cancels a pending body when its selected record no longer exists', async ({ page, vista }) => {
    await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
    await page.evaluate(() => { localStorage.setItem('vista-debug-mode', 'true'); document.cookie = 'vista-debug-mode=true; Path=/' })
    await page.reload()
    await page.locator('.sender-card textarea').fill('Inspect recovered diagnostics')
    await page.locator('.sender-card textarea').press('Enter')
    await expect(page.locator('article.assistant').last()).toContainText('Final progress answer.')
    const sessionPath = new URL(page.url()).pathname
    const messages = await vista.api(`${sessionPath}/messages`)
    const path = `${sessionPath}/runs/${messages[0].runId}/diagnostics`
    const calls = (await vista.api(path) as ModelDiagnosticSummary[]).filter(item => item.purpose === 'conversation')
    expect(calls).toHaveLength(2)
    const first = calls.find(item => item.turn === 1)!, second = calls.find(item => item.turn === 2)!
    let current = first, reads = 0, aborted = false
    let release!: () => void
    const held = new Promise<void>(resolve => { release = resolve })
    await page.route(`**/api${path}`, route => route.fulfill({ json: [current] }))
    await page.route(`**/api${path}/${first.id}`, async route => {
      reads++
      await held
      await route.continue().catch(() => {})
    })
    page.on('requestfailed', request => { if (new URL(request.url()).pathname === `/api${path}/${first.id}`) aborted = true })
    try {
      await page.getByRole('button', { name: 'Debug details', exact: true }).click()
      const modal = page.getByRole('dialog', { name: 'Debug details', exact: true })
      await modal.getByRole('tab', { name: 'Model HTTP', exact: true }).click()
      await expect.poll(() => reads).toBe(1)
      current = second
      await page.evaluate(() => window.dispatchEvent(new Event('online')))
      await expect(modal.locator('.trace-model-record')).toContainText(second.id)
      await expect.poll(() => aborted).toBe(true)
      release()
      await expect(modal.locator('.trace-model-record')).not.toContainText(first.id)
      await expect(page.locator('.state-sync-error')).toHaveCount(0)
    } finally { release() }
  })

  test('independent HTTP diagnostics survive reload and show request, raw SSE and parser counters', async ({ page, context, vista }, testInfo) => {
    await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
    // Also verifies upgrading an existing browser preference without its cookie.
    await page.evaluate(() => { localStorage.setItem('vista-debug-mode', 'true'); document.cookie = 'vista-debug-mode=; Path=/; Max-Age=0' })
    await page.reload()
    await page.locator('.sender-card textarea').fill('Inspect diagnostic evidence')
    await page.locator('.sender-card textarea').press('Enter')
    await expect(page.locator('article.assistant').last()).toContainText('Final progress answer.')
    const sessionPath = new URL(page.url()).pathname
    const messages = await vista.api(`${sessionPath}/messages`)
    const runId = messages[0].runId
    const path = `${sessionPath}/runs/${runId}/diagnostics`
    await expect.poll(async () => (await vista.api(path) as ModelDiagnosticSummary[]).filter(item => item.purpose === 'conversation').length).toBe(2)
    const calls = await vista.api(path) as ModelDiagnosticSummary[]
    expect(calls.some(item => item.purpose === 'title')).toBe(true)
    const first = calls.find(item => item.purpose === 'conversation' && item.turn === 1)!
    const record = await vista.api(`${path}/${first.id}`) as ModelDiagnosticRecord
    expect(record.parsing?.reasoningFields).toEqual(['reasoning_content'])
    expect(record.exchanges[0].requestHeaders.Authorization).toEqual(['[redacted]'])
    expect(JSON.stringify(record)).not.toContain('test-key')

    // Saved HTTP diagnostics are independent of the conversation event stream.
    await page.route('**/api/sessions/*/events?*', route => route.abort())
    await page.reload()
    await page.getByRole('button', { name: 'Debug details', exact: true }).click()
    const modal = page.getByRole('dialog', { name: 'Debug details', exact: true })
    await modal.getByRole('tab', { name: 'Model HTTP', exact: true }).click()
    const select = modal.getByRole('combobox', { name: 'Select model request' })
    await select.fill('conversation')
    await page.locator('.ant-select-item-option-content').filter({ hasText: /conversation.*main \/ 1/ }).click()
    await expect(modal.locator('.trace-model-record')).toContainText('Inspect diagnostic evidence')
    await expect(modal.locator('.trace-model-record')).toContainText('data: [DONE]')
    await expect(modal.locator('.trace-model-record')).toContainText('Private progress reasoning.')
    await expect(modal.locator('.trace-model-record')).toContainText('reasoning_content')
    await expect(modal.locator('.trace-model-record')).not.toContainText('test-key')
    await modal.getByText('Receive timing (original byte offsets)', { exact: true }).click()
    await expect(modal.locator('.trace-http-timings')).toContainText('offset')
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    await modal.getByRole('button', { name: 'Copy record', exact: true }).click()
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toContain(first.id)
    await page.screenshot({ path: testInfo.outputPath('model-http-diagnostics.png'), animations: 'disabled' })
    const geometry = await modal.evaluate(node => ({ width: node.getBoundingClientRect().width, viewport: innerWidth }))
    expect(geometry.width).toBeLessThanOrEqual(geometry.viewport)

    let rejectOnce = true
    await page.route(`**/api${path}`, route => {
      if (rejectOnce) { rejectOnce = false; return route.fulfill({ status: 503, body: 'Temporary diagnostic read failure' }) }
      return route.continue()
    })
    await modal.getByRole('button', { name: 'Refresh', exact: true }).click()
    await expect(modal.getByRole('alert')).toContainText('Temporary diagnostic read failure')
    await modal.getByRole('button', { name: 'Refresh', exact: true }).click()
    await expect(modal.getByRole('alert')).toHaveCount(0)
  })
})

test.describe('early HTTP failure', () => {
  test.use({ modelFailure: { status: 403, body: { error: { message: 'Forbidden fixture, key test-key' } } } })

  test('a request that fails before any trace steps still has a debug entrance', async ({ page, vista }) => {
    await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
    await page.evaluate(() => { localStorage.setItem('vista-debug-mode', 'true'); document.cookie = 'vista-debug-mode=true; Path=/' })
    await page.reload()
    await page.locator('.sender-card textarea').fill('Fail immediately')
    await page.locator('.sender-card textarea').press('Enter')
    await expect(page.locator('.run-failure-message')).toContainText('Forbidden fixture, key [redacted]')
    await page.getByRole('button', { name: 'Debug details', exact: true }).click()
    const modal = page.getByRole('dialog', { name: 'Debug details', exact: true })
    await modal.getByRole('tab', { name: 'Model HTTP', exact: true }).click()
    await expect(modal.locator('.trace-model-record')).toContainText('HTTP 403')
    await expect(modal.locator('.trace-model-record')).toContainText('Forbidden fixture, key [redacted]')
    await expect(modal.locator('.trace-model-record')).not.toContainText('test-key')
    await expect(page.getByRole('button', { name: 'Execution process', exact: true })).toHaveCount(0)
  })
})

test('uncaptured requests have an explicit empty state without inventing raw data', async ({ page, vista }) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
  await page.reload()
  await page.locator('.sender-card textarea').fill('Run without debug')
  await page.locator('.sender-card textarea').press('Enter')
  await expect(page.locator('article.assistant')).toContainText('Mock reply: Hi')
  await expect(page.getByRole('button', { name: 'Debug details', exact: true })).toHaveCount(0)
  await page.evaluate(() => localStorage.setItem('vista-debug-mode', 'true'))
  // The previous cookie is the persisted preference's primary source.
  await page.evaluate(() => { document.cookie = 'vista-debug-mode=true; Path=/' })
  await page.reload()
  await page.getByRole('button', { name: 'Debug details', exact: true }).click()
  const modal = page.getByRole('dialog', { name: 'Debug details', exact: true })
  await modal.getByRole('tab', { name: 'Model HTTP', exact: true }).click()
  await expect(modal.getByRole('status')).toHaveText('No saved records for this execution. Earlier requests cannot be recovered.')
  await expect(modal.locator('.trace-model-record')).toHaveCount(0)
})
