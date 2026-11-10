import { createServer, request as proxyRequest } from 'node:http'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { test, expect } from './fixtures'
import { openSettingsSection } from './settings-navigation'

test('the first view loads its language and shared code, deferring settings, charts and management', async ({ browser, vista }, testInfo) => {
  const context = await browser.newContext({ locale: 'zh-CN' })
  try {
    const page = await context.newPage()
    const paths: string[] = []
    page.on('request', request => { if (request.url().includes('/assets/')) paths.push(new URL(request.url()).pathname) })
    await page.goto(vista.origin)
    await expect(page.locator('.sender-card textarea')).toBeVisible()
    await expect(page.locator('html')).toHaveAttribute('lang', 'zh-CN')
    const firstView = [...paths]
    expect(firstView.some(path => /\/en-[^/]+\.js$/.test(path))).toBe(true)
    expect(firstView.some(path => /\/zh-CN-[^/]+\.js$/.test(path))).toBe(true)
    expect(firstView.some(path => /\/(?:SettingsPage|StatisticsSettings|SessionManagerPage|ja|fr|ru)-/.test(path))).toBe(false)
    const resources = await page.evaluate(() => performance.getEntriesByType('resource')
      .filter(entry => entry.name.includes('/assets/')).map(entry => {
        const resource = entry as PerformanceResourceTiming
        return { path: new URL(resource.name).pathname, transferBytes: resource.transferSize,
          encodedBytes: resource.encodedBodySize, decodedBytes: resource.decodedBodySize }
      }))
    await testInfo.attach('first-view-assets', { body: JSON.stringify(resources, null, 2), contentType: 'application/json' })

    await page.goto(vista.origin + '/settings/general')
    await expect(page.locator('.appearance-options')).toBeVisible()
    expect(paths.some(path => /\/SettingsPage-.*\.js$/.test(path))).toBe(true)
    expect(paths.some(path => /\/StatisticsSettings-/.test(path))).toBe(false)
    await page.goto(vista.origin + '/settings/statistics')
    await expect(page.locator('.statistics-settings')).toBeVisible()
    expect(paths.some(path => /\/StatisticsSettings-.*\.js$/.test(path))).toBe(true)
    await page.goto(vista.origin + '/sessions')
    await expect(page.locator('.session-manager-page')).toBeVisible()
    expect(paths.some(path => /\/SessionManagerPage-.*\.js$/.test(path))).toBe(true)
  } finally { await context.close() }
})

test('a missing lazy route keeps the conversation and draft, with deliberate reload recovery', { tag: '@responsive' }, async ({ page, vista }) => {
  const input = page.locator('.sender-card textarea')
  await input.fill('Keep this draft through a frontend update.')
  await page.route('**/assets/SettingsPage-*.js', route => route.fulfill({ status: 404, body: 'Missing old chunk' }))
  if (await page.locator('.mobile-sessions-trigger').isVisible()) await page.locator('.mobile-sessions-trigger').click()
  await page.getByRole('button', { name: 'Settings', exact: true }).click()
  await expect(page.locator('.asset-load-notice')).toBeVisible()
  if (await page.locator('.mobile-sessions-close').isVisible()) await page.locator('.mobile-sessions-close').click()
  await expect(input).toHaveValue('Keep this draft through a frontend update.')
  await expect(page).toHaveURL(vista.origin + '/')
  await page.locator('.asset-load-notice').getByRole('button', { name: 'Reload page' }).click()
  const confirmation = page.locator('.ui-modal:visible')
  await expect(confirmation).toContainText('Message text drafts saved in this browser are restored')
  await confirmation.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(input).toHaveValue('Keep this draft through a frontend update.')
  await page.unroute('**/assets/SettingsPage-*.js')
  await page.locator('.asset-load-notice').getByRole('button', { name: 'Reload page' }).click()
  await confirmation.getByRole('button', { name: 'Reload page', exact: true }).click()
  await expect(input).toHaveValue('Keep this draft through a frontend update.')
  await expect(page.locator('.asset-load-notice')).toHaveCount(0)
})

test('a failed statistics chunk leaves the settings shell usable', async ({ page, vista }) => {
  await page.goto(vista.origin + '/settings/general')
  await expect(page.locator('.appearance-options')).toBeVisible()
  await page.route('**/assets/StatisticsSettings-*.js', route => route.abort('failed'))
  await openSettingsSection(page, 'statistics')
  await expect(page.locator('.asset-load-notice')).toBeVisible()
  await openSettingsSection(page, 'general')
  await expect(page.locator('.appearance-options')).toBeVisible()
})

test('a language choice persists before its catalog loads and survives immediate reload', async ({ browser, vista }) => {
  const context = await browser.newContext({ locale: 'zh-CN' })
  let release = () => {}
  const downloading = new Promise<void>(resolve => { release = resolve })
  try {
    await context.addCookies([{ name: 'vista-language', value: 'en', url: vista.origin }])
    const page = await context.newPage()
    await page.goto(vista.origin + '/settings/general')
    await expect(page.locator('html')).toHaveAttribute('lang', 'en')
    await page.route('**/assets/zh-CN-*.js', async route => {
      await downloading
      await route.continue().catch(() => {}) // Reload can cancel the old page's request.
    })
    const requested = page.waitForRequest('**/assets/zh-CN-*.js')
    const picker = page.locator('.language-setting .ui-select')
    await picker.click()
    await picker.locator('input').fill('auto')
    await page.locator('.ui-select-dropdown:visible .ui-select-item-option').click()
    await requested
    expect(await page.evaluate(() => localStorage.getItem('vista-language'))).toBe('auto')
    expect((await context.cookies(vista.origin)).find(cookie => cookie.name === 'vista-language')?.value).toBe('auto')
    await expect(page.locator('html')).toHaveAttribute('lang', 'en')
    await page.unroute('**/assets/zh-CN-*.js')
    const reloading = page.reload()
    release()
    await reloading
    await expect(page.locator('html')).toHaveAttribute('lang', 'zh-CN')
    await expect(picker).toContainText('自动')
  } finally { release(); await context.close() }
})

test('a failed language download retains the choice for deliberate reload recovery', async ({ page, vista }) => {
  await page.goto(vista.origin + '/settings/general')
  await page.route('**/assets/fr-*.js', route => route.fulfill({ status: 404, body: 'Missing language chunk' }))
  const picker = page.locator('.language-setting .ui-select')
  await picker.click()
  await picker.locator('input').fill('Français')
  await page.locator('.ui-select-dropdown:visible .ui-select-item-option').click()
  await expect(page.locator('.asset-load-notice')).toBeVisible()
  await expect(page.locator('html')).toHaveAttribute('lang', 'en')
  expect(await page.evaluate(() => localStorage.getItem('vista-language'))).toBe('fr')
  expect((await page.context().cookies(vista.origin)).find(cookie => cookie.name === 'vista-language')?.value).toBe('fr')
  await expect(picker).toContainText('Français')
  await page.unroute('**/assets/fr-*.js')
  await page.reload()
  await expect(page.locator('html')).toHaveAttribute('lang', 'fr')
  await expect(picker).toContainText('Français')
  await expect(page.locator('.asset-load-notice')).toHaveCount(0)
})

test('the same output runs under static hosting', async ({ page, vista }) => {
  const root = resolve(import.meta.dirname, '../../internal/server/assets')
  const server = createServer(async (request, response) => {
    const path = new URL(request.url!, 'http://127.0.0.1').pathname
    if (path.startsWith('/api/') || path === '/licenses') {
      const upstream = proxyRequest(vista.origin + request.url, { method: request.method,
        headers: { ...request.headers, host: new URL(vista.origin).host, origin: vista.origin } }, result => {
        response.writeHead(result.statusCode!, result.headers)
        result.pipe(response)
      })
      response.on('close', () => upstream.destroy())
      upstream.on('error', () => response.destroy())
      request.pipe(upstream)
      return
    }
    try {
      const file = path.startsWith('/assets/') ? path.slice(1) : 'index.html'
      const bytes = await readFile(resolve(root, file))
      response.setHeader('Content-Type', file.endsWith('.js') ? 'application/javascript' : file.endsWith('.css') ? 'text/css' : 'text/html')
      response.end(bytes)
    } catch { response.writeHead(404).end() }
  })
  await new Promise<void>(done => server.listen(0, '127.0.0.1', done))
  try {
    const address = server.address() as { port: number }
    await page.goto(`http://127.0.0.1:${address.port}/settings/general`)
    await expect(page.locator('.appearance-options')).toBeVisible()
    const notices = await page.request.get(`http://127.0.0.1:${address.port}/licenses`)
    expect(notices.ok()).toBe(true)
    expect(await notices.text()).toContain('Copyright (c) 2026 willvar')
  } finally {
    server.closeAllConnections()
    await new Promise<void>(done => server.close(() => done()))
  }
})
