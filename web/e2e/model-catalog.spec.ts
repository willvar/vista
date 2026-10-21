import { openSettingsSection } from './settings-navigation'
import { test, expect } from './fixtures'
import type { OMPCatalogInfo } from '../src/client/types'

function recordCheck(info: OMPCatalogInfo, automatic = false): OMPCatalogInfo {
  return { ...info, history: [{ checkedAt: info.checkedAt!, automatic, changed: !!info.lastCheckChanged, error: info.checkError,
    revision: info.revision, modelsDevRevision: info.modelsDevRevision, modelCount: info.modelCount, providerCount: info.providerCount, updatedAt: info.updatedAt }, ...info.history || []] }
}

test('OMP and models.dev updates live in About while Models manages connections', async ({ page, vista }, testInfo) => {
  let info: OMPCatalogInfo = { revision: 'a'.repeat(40), modelsDevRevision: '', modelCount: 5673, providerCount: 78, updatedAt: '' }
  let release: (() => void) | undefined
  let posts = 0
  await page.route('**/api/model-catalog', async route => {
    if (route.request().method() === 'POST') {
      posts++
      await new Promise<void>(resolve => { release = resolve })
      info = recordCheck({ revision: 'b'.repeat(40), modelsDevRevision: 'c'.repeat(64), modelCount: 9000, providerCount: 240, updatedAt: '2026-10-17T15:00:00Z', checkedAt: '2026-10-18T02:00:00Z', nextCheckAt: '2026-10-19T02:07:00Z', lastCheckChanged: true })
      await route.fulfill({ json: { ...info, changed: true } })
    } else await route.fulfill({ json: info })
  })
  try {
    await page.goto(vista.origin + '/settings/models')
    const settings = page.locator('.settings-page')
    const catalog = page.getByRole('region', { name: 'Model catalog', exact: true })
    await expect(settings.locator('.settings-nav a[aria-current="page"]')).toHaveText('Model connections')
    await expect(settings.locator('.settings-nav').getByRole('link', { name: 'Sessions', exact: true })).toHaveCount(0)
    await expect(catalog).toHaveCount(0)
    await expect(settings.getByRole('switch', { name: 'Debug mode', exact: true })).toHaveCount(0)
    await openSettingsSection(page, 'about')
    await expect(page).toHaveURL(/\/settings\/about$/)
    await expect(settings.getByRole('switch', { name: 'Debug mode', exact: true })).toBeVisible()
    await expect(catalog).toContainText('5,673 models')
    await expect(catalog).toContainText('aaaaaaa')
    await expect(catalog).toContainText('bundled with Vista')
    await catalog.getByRole('button', { name: 'Update history', exact: true }).click()
    const history = page.getByRole('dialog', { name: 'Update history', exact: true })
    await expect(history).toContainText('No checks in the last 7 days.')
    await page.keyboard.press('Escape')
    await expect(history).not.toBeVisible()
    await expect(settings).toBeVisible()

    await settings.getByRole('button', { name: 'Check for updates', exact: true }).click()
    await expect.poll(() => posts).toBe(1)
    await expect(settings.getByRole('button', { name: 'Updating…', exact: true })).toBeDisabled()
    await expect(settings.getByRole('button', { name: 'Connect provider', exact: true })).toHaveCount(0)

    release!()
    await expect(catalog.getByRole('status')).toHaveText('Model catalog updated.')
    await expect(catalog).toContainText('9,000 models')
    await expect(catalog).toContainText('bbbbbbb')
    await expect(catalog).toContainText('models.dev ccccccc')
    await expect(catalog).not.toContainText('Last checked')
    await expect(catalog).not.toContainText('Next check')
    await expect(catalog).not.toContainText('Update OMP and models.dev together')
    await expect(catalog).toContainText('Updated ')

    await page.emulateMedia({ colorScheme: 'dark' })
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
    await catalog.scrollIntoViewIfNeeded()
    await page.screenshot({ path: testInfo.outputPath('model-catalog-dark.png'), animations: 'disabled' })
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(testInfo.project.use.viewport!.width + 2)
    await catalog.getByRole('button', { name: 'Update history', exact: true }).click()
    await expect(history).toContainText('Update OMP and models.dev together')
    await expect(history).toContainText('Last checked')
    await expect(history).toContainText('Next check')
    await expect(history).toContainText('Records are kept for 7 days')
    await expect(history.locator('.omp-catalog-history-record')).toHaveCount(1)
    await expect(history.locator('.omp-catalog-history-record')).toContainText('Manual check')
    await expect(history.locator('.omp-catalog-history-record')).toContainText('Updated')
    await expect(history).toContainText('OMP bbbbbbb')
    await expect(history).toContainText('models.dev ccccccc')
    await page.screenshot({ path: testInfo.outputPath('model-catalog-history-dark.png'), animations: 'disabled' })
    await page.keyboard.press('Escape')
    await expect(history).not.toBeVisible()
    await page.reload()
    await expect(catalog).toContainText('bbbbbbb')
    await expect(catalog).toContainText('9,000 models')
    await expect(catalog).toContainText('models.dev ccccccc')
    await expect(catalog.getByRole('status')).toHaveText('Model catalog updated.')
    await catalog.getByRole('button', { name: 'Update history', exact: true }).click()
    await expect(history.locator('.omp-catalog-history-record')).toHaveCount(1)
    await page.keyboard.press('Escape')
    await openSettingsSection(page, 'models')
    await expect(catalog).toHaveCount(0)
    await expect(settings.getByRole('button', { name: 'Connect provider', exact: true })).toBeVisible()
  } finally { release?.() }
})

test('catalog failure retains its version and allows another check', async ({ page, vista }, testInfo) => {
  let info: OMPCatalogInfo = { revision: 'a'.repeat(40), modelsDevRevision: 'c'.repeat(64), modelCount: 9000, providerCount: 240 }
  let attempts = 0
  await page.route('**/api/model-catalog', async route => {
    if (route.request().method() === 'GET') { await route.fulfill({ json: info }); return }
    attempts++
    if (attempts === 1) {
      info = recordCheck({ ...info, checkedAt: '2026-10-18T02:00:00Z', nextCheckAt: '2026-10-18T08:00:00Z', checkError: 'models.dev catalog returned HTTP 503', lastCheckChanged: false })
      await route.fulfill({ status: 502, json: { code: 'catalog_update_failed', message: info.checkError } })
    } else {
      info = recordCheck({ ...info, checkedAt: '2026-10-18T03:00:00Z', nextCheckAt: '2026-10-19T03:08:00Z', checkError: '' })
      await route.fulfill({ json: { ...info, changed: false } })
    }
  })
  await page.goto(vista.origin + '/settings/about')
  const catalog = page.getByRole('region', { name: 'Model catalog', exact: true })
  const check = page.locator('.settings-header-action')
  await check.click()
  await expect(catalog.getByRole('alert')).toHaveText('models.dev catalog returned HTTP 503')
  await expect(catalog).toContainText('aaaaaaa')
  await expect(catalog).toContainText('models.dev ccccccc')
  await expect(check).toBeEnabled()
  await page.screenshot({ path: testInfo.outputPath('model-catalog-failed.png'), animations: 'disabled' })
  await check.click()
  await expect(catalog.getByRole('alert')).toHaveCount(0)
  await expect(catalog.getByRole('status')).toHaveText('Check complete. Catalog unchanged.')
  await catalog.getByRole('button', { name: 'Update history', exact: true }).click()
  const history = page.getByRole('dialog', { name: 'Update history', exact: true })
  const records = history.locator('.omp-catalog-history-record')
  await expect(records).toHaveCount(2)
  await expect(records.first()).toContainText('Unchanged')
  await expect(records.last()).toContainText('Failed')
  await expect(records.last()).toContainText('models.dev catalog returned HTTP 503')
})

test('automatic catalog checks show progress, persisted errors and recovery', async ({ page, vista }, testInfo) => {
  await page.clock.install()
  let info: OMPCatalogInfo = { revision: 'a'.repeat(40), modelsDevRevision: 'c'.repeat(64), modelCount: 9000, providerCount: 240, updatedAt: '2026-10-17T15:00:00Z' }
  let posts = 0
  await page.route('**/api/model-catalog', async route => {
    if (route.request().method() === 'POST') posts++
    await route.fulfill({ json: info })
  })
  await page.goto(vista.origin + '/settings/about')
  const catalog = page.getByRole('region', { name: 'Model catalog', exact: true })
  await expect(page.getByRole('button', { name: 'Check for updates', exact: true })).toBeEnabled()
  info = { ...info, updating: true }
  await page.clock.fastForward(5000)
  await expect(page.getByRole('button', { name: 'Updating…', exact: true })).toBeDisabled()
  info = recordCheck({ ...info, updating: false, checkedAt: '2026-10-18T02:00:00Z', nextCheckAt: '2026-10-18T08:00:00Z', checkError: 'Independent sources disagree; keeping the current catalog.' }, true)
  await page.clock.fastForward(5000)
  await expect(catalog.getByRole('alert')).toHaveText(info.checkError!)
  await expect(catalog).not.toContainText('Last checked')
  await expect(catalog).not.toContainText('Next check')
  await expect(catalog).toContainText('aaaaaaa')
  await page.reload()
  await expect(catalog.getByRole('alert')).toHaveText(info.checkError!)
  await catalog.getByRole('button', { name: 'Update history', exact: true }).click()
  const history = page.getByRole('dialog', { name: 'Update history', exact: true })
  await expect(history).toContainText('Last checked')
  await expect(history).toContainText('Next check')
  await expect(history.locator('.omp-catalog-history-record')).toContainText('Automatic check')
  await expect(history.locator('.omp-catalog-history-record')).toContainText(info.checkError!)
  await page.screenshot({ path: testInfo.outputPath('catalog-automatic-retry.png'), animations: 'disabled' })
  info = recordCheck({ ...info, checkedAt: '2026-10-18T08:00:00Z', nextCheckAt: '2026-10-19T08:09:00Z', checkError: '', lastCheckChanged: false }, true)
  await page.clock.fastForward(5000)
  await expect(catalog.getByRole('alert')).toHaveCount(0)
  await expect(catalog.getByRole('status')).toHaveText('Check complete. Catalog unchanged.')
  await expect(history.locator('.omp-catalog-history-record')).toHaveCount(2)
  await expect(history.locator('.omp-catalog-history-record').first()).toContainText('Unchanged')
  await page.keyboard.press('Escape')
  await expect(catalog).toBeVisible()
  expect(posts).toBe(0)
})

test('catalog conflicts identify the model and field in the alert and history', async ({ page, vista }, testInfo) => {
  const conflict = 'models.dev catalog update rejected; keeping the current catalog:\nhttps://models.dev/api.json: downloaded catalog could not confirm 1 metadata changes:\nopencode-zen/deepseek-v4.1-flash-free contextWindow: 200000 -> 50000 (https://catalog.stencil.so/models.json.zstd has 64000)'
  const info = recordCheck({ revision: 'a'.repeat(40), modelsDevRevision: 'c'.repeat(64), modelCount: 9000, providerCount: 240,
    checkedAt: '2026-10-19T02:00:00Z', nextCheckAt: '2026-10-19T08:00:00Z', checkError: conflict, lastCheckChanged: false })
  await page.route('**/api/model-catalog', route => route.fulfill({ json: info }))
  await page.goto(vista.origin + '/settings/about')
  const catalog = page.getByRole('region', { name: 'Model catalog', exact: true })
  await expect(catalog.getByRole('alert')).toHaveText(conflict)
  expect(await catalog.locator('.ant-alert-title').evaluate(node => getComputedStyle(node).whiteSpace)).toBe('pre-wrap')
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(testInfo.project.use.viewport!.width + 2)
  await catalog.getByRole('button', { name: 'Update history', exact: true }).click()
  const history = page.getByRole('dialog', { name: 'Update history', exact: true })
  await expect(history).toBeVisible()
  await expect(history.locator('.omp-catalog-history-error')).toHaveText(conflict)
  await history.screenshot({ path: testInfo.outputPath('catalog-field-conflict.png'), animations: 'disabled' })
})

test('a delayed status read cannot overwrite a newer manual check', async ({ page, vista }) => {
  await page.clock.install()
  let info: OMPCatalogInfo = { revision: 'a'.repeat(40), modelCount: 5673, providerCount: 78 }
  let reads = 0
  let release: (() => void) | undefined
  await page.route('**/api/model-catalog', async route => {
    if (route.request().method() === 'POST') {
      info = { ...info, revision: 'b'.repeat(40), checkedAt: '2026-10-18T02:00:00Z', lastCheckChanged: true }
      await route.fulfill({ json: { ...info, changed: true } })
      return
    }
    const response = { ...info }
    if (++reads === 2) await new Promise<void>(resolve => { release = resolve })
    await route.fulfill({ json: response })
  })
  try {
    await page.goto(vista.origin + '/settings/about')
    const catalog = page.getByRole('region', { name: 'Model catalog', exact: true })
    await expect(catalog).toContainText('aaaaaaa')
    await page.clock.fastForward(5000)
    await expect.poll(() => !!release).toBe(true)
    await page.getByRole('button', { name: 'Check for updates', exact: true }).click()
    await expect(catalog).toContainText('bbbbbbb')
    const delivered = page.waitForResponse(response => response.url().endsWith('/api/model-catalog') && response.request().method() === 'GET')
    release!()
    // Let the old response reach the component before asserting the version.
    await delivered
    await page.waitForTimeout(100)
    await expect(catalog).toContainText('bbbbbbb')
    await expect(catalog.getByRole('status')).toHaveText('Model catalog updated.')
  } finally { release?.() }
})
