import { rmSync } from 'node:fs'
import { installMockStateStream } from './state-stream-fixture'
import { openSettingsSection } from './settings-navigation'
import { test as base, expect, type Page, type Locator } from '@playwright/test'
import { createServer } from 'vite'
import { resolve } from 'node:path'

const test = base.extend<{}, { mockOrigin: string }>({
  mockOrigin: [async ({}, use) => {
    const server = await createServer({
      cacheDir: resolve(import.meta.dirname, `../.vista/test-state/search-settings-vite-${process.pid}`),
      root: resolve(import.meta.dirname, '..'),
      configFile: resolve(import.meta.dirname, '../vite.config.ts'),
      server: { host: '127.0.0.1', port: 0, strictPort: false, watch: { ignored: ['**/.vista/**'] } },
    })
    await server.listen()
    const address = server.httpServer!.address()
    if (!address || typeof address === 'string') throw new Error('Mock UI server did not start')
    try { await use(`http://127.0.0.1:${address.port}`) } finally { await server.close(); rmSync(server.config.cacheDir, { recursive: true, force: true }) }
  }, { scope: 'worker' }],
})

async function mockSearchSettings(page: Page, origin: string) {
  const fixture = {
    keys: {} as Record<string, string>,
    order: ['firecrawl', 'parallel', 'tavily', 'exa', 'deepseek'],
    fetchOrder: ['jina', 'deepseek'],
    searchPreviewBytes: 50 * 1024,
    fetchPreviewBytes: 50 * 1024,
    writes: [] as { method: string; name?: string; apiKey?: string; order?: string[]; fetchOrder?: string[]; searchPreviewBytes?: number; fetchPreviewBytes?: number }[],
    unexpected: [] as string[],
    failSave: false,
    failRead: false,
  }
  await page.addInitScript(() => {
    if (!localStorage.getItem('vista-language')) localStorage.setItem('vista-language', 'en')
  })
  await installMockStateStream(page)
  await page.route('**/api/**', async route => {
    const request = route.request()
    const path = new URL(request.url()).pathname.slice(4)
    const method = request.method()
    const reply = (json: unknown) => route.fulfill({ json })
    if (path === '/activity' && method === 'POST') return route.fulfill({ status: 204 })
    if (path === '/search-config') {
      if (method === 'GET') {
        if (fixture.failRead) return route.fulfill({ status: 500, body: 'Could not load search settings' })
        return reply({ order: fixture.order, fetchOrder: fixture.fetchOrder, providers: ['firecrawl', 'parallel', 'tavily', 'exa', 'deepseek'].map(name => ({ name, source: fixture.keys[name] ? 'settings' : name === 'exa' ? 'environment' : 'none' })), jinaReader: { name: 'jina', source: fixture.keys.jina ? 'settings' : 'none' }, deepseekFetch: { name: 'deepseek', source: fixture.keys['deepseek-fetch'] ? 'settings' : 'none' }, searchPreviewBytes: fixture.searchPreviewBytes, fetchPreviewBytes: fixture.fetchPreviewBytes })
      }
      if (method === 'PUT' || method === 'DELETE' || method === 'PATCH') {
        const body = request.postDataJSON()
        fixture.writes.push({ method, ...body })
        if (fixture.failSave) return route.fulfill({ status: 500, body: 'Could not save search settings' })
        if (method === 'PATCH') {
          if (body.order) fixture.order = body.order
          if (body.fetchOrder) fixture.fetchOrder = body.fetchOrder
          if (body.searchPreviewBytes !== undefined) fixture.searchPreviewBytes = body.searchPreviewBytes
          if (body.fetchPreviewBytes !== undefined) fixture.fetchPreviewBytes = body.fetchPreviewBytes
        }
        else if (method === 'PUT') fixture.keys[body.name] = body.apiKey
        else delete fixture.keys[body.name]
        return route.fulfill({ status: 204 })
      }
    }
    if (method === 'GET') {
      if (path === '/preferences') return reply({ userId: 'settings-user', lastMode: 'manual', lastTier: 'speed', manualModelKey: '', reasoningByModelId: {}, feedbackInvitationSeen: true })
      if (path === '/models') return reply({ default: '', models: [] })
      if (path === '/agent-settings') return reply({ instructions: '', instructionsPath: '/home/test/.config/vista/AGENTS.md', delegationDepth: 1, maxDelegationDepth: 3 })
      if (path === '/sessions') return reply([])
      if (path === '/status') return reply({ state: 'running', sessions: { total: 0, open: 0, closed: 0, running: 0, workers: 0 }, workerMetrics: [] })
    }
    fixture.unexpected.push(`${method} ${path}`)
    return route.fulfill({ status: 404, body: 'Unmocked API request' })
  })
  await page.goto(`${origin}/settings/general`)
  await openSettingsSection(page, 'search')
  await expect(page.getByRole('button', { name: 'Edit Exa API key', exact: true })).toBeEnabled()
  return fixture
}

async function chooseCredential(page: Page, name: string, fetch = false) {
  await page.locator('.search-credentials').getByRole('combobox').click()
  const options = page.locator('.ui-select-dropdown:visible').getByRole('option', { name, exact: true })
  await options.nth(name === 'DeepSeek' && fetch ? 1 : 0).click()
}
const keyStatus = (page: Page) => page.locator('.search-credentials > .search-settings-hint').first()
async function editKey(page: Page, name: string, title = `Edit ${name} API key`, fetch = false) {
  await chooseCredential(page, name, fetch)
  await page.locator('.search-credentials').getByRole('button', { name: title, exact: true }).click()
  const dialog = page.getByRole('dialog', { name: title, exact: true })
  await expect(dialog.getByLabel('API Key', { exact: true })).toBeEnabled()
  return dialog
}
async function removeSavedKey(page: Page, dialog: Locator) {
  await dialog.getByRole('button', { name: 'Remove saved key', exact: true }).click()
  await page.locator('.search-key-remove-popover').getByRole('button', { name: 'Delete', exact: true }).click()
}

test('DeepSeek search and fetch keys are configured and cleared independently', async ({ page, mockOrigin }, testInfo) => {
  test.setTimeout(60_000)
  const fixture = await mockSearchSettings(page, mockOrigin)
  const status = async (fetch: boolean, expected: string) => {
    await chooseCredential(page, 'DeepSeek', fetch)
    await expect(keyStatus(page)).toHaveText(expected)
  }
  const open = async (fetch: boolean, hint: string) => {
    const dialog = await editKey(page, 'DeepSeek', 'Edit DeepSeek API key', fetch)
    await expect(dialog).toContainText(hint)
    return dialog
  }
  await status(false, 'Not configured')
  await status(true, 'Not configured')
  const searchDialog = await open(false, 'Used for web search.')
  await expect(searchDialog).toContainText('Not configured')
  await searchDialog.getByLabel('API Key', { exact: true }).fill('search-only-secret')
  await searchDialog.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(searchDialog).toBeHidden()
  await status(false, 'Saved API key')
  await status(true, 'Not configured')
  const fetchDialog = await open(true, 'Used for page reading.')
  await fetchDialog.getByLabel('API Key', { exact: true }).fill('fetch-only-secret')
  await page.screenshot({ path: testInfo.outputPath('deepseek-fetch-key.png'), animations: 'disabled' })
  await fetchDialog.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(fetchDialog).toBeHidden()
  expect(fixture.keys.deepseek).toBe('search-only-secret')
  expect(fixture.keys['deepseek-fetch']).toBe('fetch-only-secret')
  await page.reload()
  await status(true, 'Saved API key')
  const clearSearch = await open(false, 'Used for web search.')
  await expect(clearSearch.getByLabel('API Key', { exact: true })).toHaveValue('')
  const confirmation = page.locator('.search-key-remove-popover')
  const beforeRemoval = fixture.writes.length
  await clearSearch.getByRole('button', { name: 'Remove saved key', exact: true }).click()
  await expect(confirmation.getByRole('alertdialog')).toContainText('DeepSeek · Web search')
  expect(fixture.writes).toHaveLength(beforeRemoval)
  expect(fixture.keys.deepseek).toBe('search-only-secret')
  await confirmation.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(confirmation).toBeHidden()
  await expect(clearSearch).toBeVisible()
  await clearSearch.getByRole('button', { name: 'Remove saved key', exact: true }).click()
  await expect(confirmation).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(confirmation).toBeHidden()
  await expect(clearSearch).toBeVisible()
  await expect(page.locator('.settings-page')).toBeVisible()
  expect(fixture.writes).toHaveLength(beforeRemoval)
  fixture.failSave = true
  await removeSavedKey(page, clearSearch)
  await expect(confirmation.getByRole('alert')).toContainText('Could not save search settings')
  expect(fixture.keys.deepseek).toBe('search-only-secret')
  fixture.failSave = false
  await confirmation.getByRole('button', { name: 'Delete', exact: true }).click()
  await expect(clearSearch).toBeHidden()
  await status(false, 'Not configured')
  await status(true, 'Saved API key')
  expect(fixture.keys.deepseek).toBeUndefined()
  expect(fixture.keys['deepseek-fetch']).toBe('fetch-only-secret')
  const clearFetch = await open(true, 'Used for page reading.')
  await clearFetch.getByRole('button', { name: 'Remove saved key', exact: true }).click()
  await expect(confirmation.getByRole('alertdialog')).toContainText('DeepSeek · Page reading')
  expect(fixture.keys['deepseek-fetch']).toBe('fetch-only-secret')
  await confirmation.getByRole('button', { name: 'Delete', exact: true }).click()
  await expect(clearFetch).toBeHidden()
  await status(true, 'Not configured')
  expect(fixture.unexpected).toEqual([])
})

for (const setting of [
  { key: 'searchPreviewBytes', label: 'Initial search result size (bytes)', otherKey: 'fetchPreviewBytes', otherLabel: 'Initial page content size (bytes)' },
  { key: 'fetchPreviewBytes', label: 'Initial page content size (bytes)', otherKey: 'searchPreviewBytes', otherLabel: 'Initial search result size (bytes)' },
] as const) {
  test(`${setting.key} preserves both drafts and saves exact byte budgets or unlimited`, async ({ page, mockOrigin }, testInfo) => {
    const fixture = await mockSearchSettings(page, mockOrigin)
    const form = page.locator('.search-config-form')
    const input = form.getByRole('spinbutton', { name: setting.label, exact: true })
    const otherInput = form.getByRole('spinbutton', { name: setting.otherLabel, exact: true })
    const save = form.getByRole('button', { name: 'Save changes', exact: true })
    await expect(page.locator('.search-settings-content > p').first()).toHaveText('Configure web search and page reading services for the agent.')
    await expect(page.locator('.search-settings-content')).toContainText('skipping methods without required keys')
    await expect(page.getByRole('img', { name: 'A higher initial content limit can reduce errors and omissions caused by incomplete information, but uses more context and input tokens.', exact: true })).toHaveCount(1)
    await expect(form).toContainText('Preview sizes are UTF-8 byte counts; 0 means unlimited.')
    await expect(input).toHaveValue('51200')
    await expect(save).toBeDisabled()
    await otherInput.fill('2345')
    await input.fill('777')
    const keys = await editKey(page, 'Exa')
    await keys.getByLabel('API Key', { exact: true }).fill('summary-key-secret')
    await keys.getByRole('button', { name: 'Save changes', exact: true }).click()
    await expect(keys).toBeHidden()
    await expect(input).toHaveValue('777')
    await expect(otherInput).toHaveValue('2345')
    fixture.failSave = true
    await save.click()
    await expect(page.getByRole('alert')).toContainText('Could not save search settings')
    expect(fixture[setting.key]).toBe(51200)
    expect(fixture[setting.otherKey]).toBe(51200)
    fixture.failSave = false
    await page.getByRole('button', { name: 'Retry', exact: true }).click()
    await expect(input).toHaveValue('777')
    await expect(otherInput).toHaveValue('2345')
    await save.click()
    await expect(form.getByRole('status')).toHaveText('Updated. Changes apply to the next run.')
    expect(fixture[setting.key]).toBe(777)
    expect(fixture[setting.otherKey]).toBe(2345)
    await page.reload()
    await expect(input).toHaveValue('777')
    await expect(otherInput).toHaveValue('2345')
    await input.fill('0')
    await save.click()
    await expect(save).toBeDisabled()
    expect(fixture[setting.key]).toBe(0)
    await page.reload()
    await expect(input).toHaveValue('0')
    await input.fill('')
    await expect(save).toBeDisabled()
    await input.fill('1.5')
    await expect(save).toBeDisabled()
    await input.fill('1536')
    await save.click()
    await expect(save).toBeDisabled()
    expect(fixture[setting.key]).toBe(1536)
    await page.emulateMedia({ colorScheme: 'dark' })
    await input.scrollIntoViewIfNeeded()
    await page.screenshot({ path: testInfo.outputPath(`${setting.key}-dark.png`), animations: 'disabled' })
    expect(fixture.unexpected).toEqual([])
  })
}

test('search and reader keys save, survive reload without readback, and can be removed', async ({ page, mockOrigin }) => {
  test.slow()
  const fixture = await mockSearchSettings(page, mockOrigin)
  const settings = page.locator('.settings-page:visible')
  await expect(settings.locator('.settings-nav a[aria-current="page"]')).toHaveText('Web queries')
  await expect(settings.locator('input[type=password]')).toHaveCount(0)
  await expect(settings.locator('[data-provider="vista"]')).toHaveCount(0)
  const names = ['Firecrawl', 'Parallel', 'Tavily', 'Exa', 'Jina Reader']
  for (const name of names) {
    const id = name === 'Jina Reader' ? 'jina' : name.toLowerCase()
    await chooseCredential(page, name)
    await expect(keyStatus(page)).toHaveText(name === 'Exa' ? 'Using environment key' : 'Anonymous access')
    const form = await editKey(page, name)
    const input = form.getByLabel('API Key', { exact: true })
    const save = form.getByRole('button', { name: 'Save changes', exact: true })
    await expect(save).toBeDisabled()
    await expect(form).toContainText(name === 'Exa' ? 'Using environment key' : 'Anonymous access')
    await input.fill(`  ${id}-user-secret  `)
    await expect(input).toHaveAttribute('type', 'password')
    await save.click()
    await expect(form).toBeHidden()
    await expect(keyStatus(page)).toHaveText('Saved API key')
  }
  expect(fixture.writes).toEqual([...['firecrawl', 'parallel', 'tavily', 'exa'].map(name => ({ method: 'PUT', name, apiKey: `${name}-user-secret` })), { method: 'PUT', name: 'jina', apiKey: 'jina-user-secret' }])
  await page.reload()
  for (const name of names) {
    const form = await editKey(page, name)
    await expect(form).toContainText('Saved API key')
    await expect(form.getByLabel('API Key', { exact: true })).toHaveValue('')
    await form.getByRole('button', { name: 'Cancel', exact: true }).click()
    await expect(form).toBeHidden()
  }
  const browserStorage = await page.evaluate(() => JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage } }))
  expect(browserStorage).not.toContain('user-secret')
  expect(await settings.textContent()).not.toContain('user-secret')
  for (const name of names) {
    const form = await editKey(page, name)
    await removeSavedKey(page, form)
    await expect(form).toBeHidden()
    await expect(keyStatus(page)).toHaveText(name === 'Exa' ? 'Using environment key' : 'Anonymous access')
    const cleared = await editKey(page, name)
    await expect(cleared).toContainText(name === 'Exa' ? 'Using environment key' : 'Anonymous access')
    await expect(cleared.getByRole('button', { name: 'Remove saved key', exact: true })).toHaveCount(0)
    await cleared.getByRole('button', { name: 'Cancel', exact: true }).click()
    await expect(cleared).toBeHidden()
  }
  await page.reload()
  await chooseCredential(page, 'Exa')
  await expect(keyStatus(page)).toHaveText('Using environment key')
  expect(fixture.keys).toEqual({})
  expect(fixture.unexpected).toEqual([])
})

test('search priority preserves drafts on failure and key saves, then persists every provider position', async ({ page, mockOrigin }) => {
  const fixture = await mockSearchSettings(page, mockOrigin)
  const initial = [...fixture.order]
  const form = page.locator('.search-config-form')
  const order = form.getByRole('group', { name: 'Search priority', exact: true })
  const rowOrder = () => order.locator('.schema-order-item').evaluateAll(rows => rows.map(row => row.lastChild!.textContent))
  const save = form.getByRole('button', { name: 'Save changes', exact: true })

  await expect(order.getByRole('button', { name: 'Move item 1 up', exact: true })).toBeDisabled()
  await expect(order.getByRole('button', { name: 'Move item 5 down', exact: true })).toBeDisabled()
  await expect(save).toBeDisabled()
  await order.getByRole('button', { name: 'Move item 4 up', exact: true }).click()
  const reordered = ['firecrawl', 'parallel', 'exa', 'tavily', 'deepseek']
  const labels = ['Firecrawl', 'Parallel', 'Exa', 'Tavily', 'DeepSeek']
  expect(await rowOrder()).toEqual(labels)
  expect(fixture.order).toEqual(initial)
  fixture.failSave = true
  await save.click()
  await expect(page.getByRole('alert')).toContainText('Could not save search settings')
  expect(await rowOrder()).toEqual(labels)
  expect(fixture.order).toEqual(initial)
  fixture.failSave = false
  await page.getByRole('button', { name: 'Retry', exact: true }).click()
  await expect(page.getByRole('alert')).toHaveCount(0)
  await expect(save).toBeEnabled()
  expect(await rowOrder()).toEqual(labels)
  const keys = await editKey(page, 'Exa')
  await keys.getByLabel('API Key', { exact: true }).fill('order-key-secret')
  await keys.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(keys).toBeHidden()
  expect(await rowOrder()).toEqual(labels)
  expect(fixture.order).toEqual(initial)
  await save.click()
  await expect(form.getByRole('status')).toHaveText('Updated. Changes apply to the next run.')
  await expect(save).toBeDisabled()
  expect(fixture.order).toEqual(reordered)
  expect(fixture.keys.exa).toBe('order-key-secret')
  await page.reload()
  await expect(order.locator('.schema-array-row')).toHaveCount(5)
  expect(await rowOrder()).toEqual(labels)
  await expect(save).toBeDisabled()
  await order.getByRole('button', { name: 'Move item 3 up', exact: true }).click()
  await openSettingsSection(page, 'general')
  await openSettingsSection(page, 'search')
  await expect(order.locator('.schema-array-row')).toHaveCount(5)
  expect(await rowOrder()).toEqual(labels)
  expect(fixture.unexpected).toEqual([])
})

test('key editor preserves failed saves, retries loading, and discards canceled drafts', async ({ page, mockOrigin }) => {
  const fixture = await mockSearchSettings(page, mockOrigin)
  const settings = page.locator('.settings-page:visible')
  const form = await editKey(page, 'Firecrawl')
  const input = form.getByLabel('API Key', { exact: true })
  const save = form.getByRole('button', { name: 'Save changes', exact: true })
  await input.fill('   ')
  await expect(save).toBeDisabled()
  await input.fill('contains space')
  await expect(save).toBeDisabled()
  await input.fill('firecrawl-replacement-secret')
  fixture.failSave = true
  await save.click()
  await expect(form.getByRole('alert')).toContainText('Could not save search settings')
  await expect(input).toHaveValue('firecrawl-replacement-secret')
  await expect(form).toContainText('Anonymous access')
  fixture.failSave = false
  await save.click()
  await expect(form).toBeHidden()
  await expect(keyStatus(page)).toHaveText('Saved API key')
  await editKey(page, 'Firecrawl')
  await expect(input).toHaveValue('')
  await input.fill('unsaved-secret')
  await form.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(form).toBeHidden()
  await openSettingsSection(page, 'general')
  await openSettingsSection(page, 'search')
  await editKey(page, 'Firecrawl')
  await expect(input).toHaveValue('')
  await expect(form).toContainText('Saved API key')
  await form.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(form).toBeHidden()

  for (const dismiss of ['escape', 'outside']) {
    const dialog = await editKey(page, 'Parallel')
    const draft = dialog.getByLabel('API Key', { exact: true })
    await expect(draft).toHaveValue('')
    await draft.fill('discarded-secret')
    if (dismiss === 'escape') await draft.press('Escape')
    else await page.locator('.ui-modal-wrap').last().click({ position: { x: 5, y: 5 } })
    await expect(dialog).toBeHidden()
    await expect(settings).toBeVisible()
    await expect(keyStatus(page)).toHaveText('Anonymous access')
    expect(fixture.keys.parallel).toBeUndefined()
  }

  fixture.failRead = true
  await page.reload()
  await expect(settings.getByRole('alert')).toContainText('Could not load search settings')
  fixture.failRead = false
  await settings.getByRole('button', { name: 'Retry', exact: true }).click()
  await chooseCredential(page, 'Firecrawl')
  await expect(keyStatus(page)).toHaveText('Saved API key')
  expect(fixture.unexpected).toEqual([])
})

test('page reading and search order save together without leaking credentials', async ({ page, mockOrigin }, testInfo) => {
  const fixture = await mockSearchSettings(page, mockOrigin)
  const form = page.locator('.search-config-form')
  const fetch = form.getByRole('group', { name: 'Page reading priority', exact: true })
  const search = form.getByRole('group', { name: 'Search priority', exact: true })
  const rowOrder = () => fetch.locator('.schema-order-item').evaluateAll(rows => rows.map(row => row.lastChild!.textContent))
  const save = form.getByRole('button', { name: 'Save changes', exact: true })
  expect(await rowOrder()).toEqual(['Jina Reader', 'DeepSeek'])
  await expect(fetch.getByRole('button', { name: 'Move item 1 up', exact: true })).toBeDisabled()
  await expect(fetch.getByRole('button', { name: 'Move item 2 down', exact: true })).toBeDisabled()
  await expect(save).toBeDisabled()
  await fetch.getByRole('button', { name: 'Move item 2 up', exact: true }).click()
  expect(await rowOrder()).toEqual(['DeepSeek', 'Jina Reader'])
  await search.getByRole('button', { name: 'Move item 4 up', exact: true }).click()
  fixture.failSave = true
  await save.click()
  await expect(page.getByRole('alert')).toContainText('Could not save search settings')
  expect(fixture.fetchOrder).toEqual(['jina', 'deepseek'])
  expect(await rowOrder()).toEqual(['DeepSeek', 'Jina Reader'])
  fixture.failSave = false
  await page.getByRole('button', { name: 'Retry', exact: true }).click()
  const jina = await editKey(page, 'Jina Reader')
  await jina.getByLabel('API Key', { exact: true }).fill('jina-user-secret')
  await jina.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(jina).toBeHidden()
  expect(await rowOrder()).toEqual(['DeepSeek', 'Jina Reader'])
  await save.click()
  await expect(form.getByRole('status')).toHaveText('Updated. Changes apply to the next run.')
  expect(fixture.fetchOrder).toEqual(['deepseek', 'jina'])
  expect(fixture.order).toEqual(['firecrawl', 'parallel', 'exa', 'tavily', 'deepseek'])
  await expect(save).toBeDisabled()
  expect(fixture.writes.at(-1)).toEqual({ method: 'PATCH', order: fixture.order, fetchOrder: ['deepseek', 'jina'], searchPreviewBytes: 51200, fetchPreviewBytes: 51200 })
  await page.reload()
  await expect(fetch.locator('.schema-array-row')).toHaveCount(2)
  expect(await rowOrder()).toEqual(['DeepSeek', 'Jina Reader'])
  await expect(save).toBeDisabled()
  await chooseCredential(page, 'Jina Reader')
  await expect(keyStatus(page)).toHaveText('Saved API key')
  await fetch.scrollIntoViewIfNeeded()
  await page.screenshot({ path: testInfo.outputPath('saved-webfetch-order.png'), animations: 'disabled' })
  expect(fixture.unexpected).toEqual([])
})

test('search settings remain readable and reachable in English and Chinese', async ({ page, mockOrigin }, testInfo) => {
  const fixture = await mockSearchSettings(page, mockOrigin)
  for (const language of ['en', 'zh-CN']) {
    await page.evaluate(value => localStorage.setItem('vista-language', value), language)
    await page.reload()
    const settings = page.locator('.settings-page:visible')
    await expect(settings.locator('.settings-nav a[aria-current="page"]')).toHaveText(language === 'en' ? 'Web queries' : '网络查询')
    const overview = settings.getByRole('figure', { name: language === 'en' ? 'What web search and page reading do' : '网络搜索与网页读取的作用', exact: true })
    await overview.scrollIntoViewIfNeeded()
    const overviewLayout = await overview.evaluate(element => {
      const rect = element.getBoundingClientRect()
      return { left: rect.left, right: rect.right, width: innerWidth, overflow: element.scrollWidth - element.clientWidth }
    })
    expect(overviewLayout.left).toBeGreaterThanOrEqual(0)
    expect(overviewLayout.right).toBeLessThanOrEqual(overviewLayout.width)
    expect(overviewLayout.overflow).toBeLessThanOrEqual(2)
    if (language === 'zh-CN') {
      const lineCount = await overview.getByText('按需深入', { exact: true }).evaluate(element => {
        const range = document.createRange()
        range.selectNodeContents(element)
        return range.getClientRects().length
      })
      expect(lineCount).toBe(1)
    }
    const runningAnimations = () => overview.evaluate(element => element.getAnimations({ subtree: true }).filter(animation => animation.playState === 'running').length)
    await page.emulateMedia({ reducedMotion: 'no-preference' })
    await expect.poll(runningAnimations).toBeGreaterThan(0)
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await expect.poll(runningAnimations).toBe(0)
    await page.emulateMedia({ reducedMotion: 'no-preference' })
    await expect.poll(runningAnimations).toBeGreaterThan(0)
    for (const colorScheme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme })
      await expect(page.locator('html')).toHaveAttribute('data-theme', colorScheme)
      await page.screenshot({ path: testInfo.outputPath(`web-capabilities-${language}-${colorScheme}.png`), animations: 'disabled' })
    }
    await page.emulateMedia({ colorScheme: 'light' })
    const fetch = settings.getByRole('group', { name: language === 'en' ? 'Page reading priority' : '网页读取优先顺序', exact: true })
    await expect(fetch.locator('.schema-order-item')).toHaveText(['1Jina Reader', '2DeepSeek'])
    await chooseCredential(page, 'Exa')
    await expect(keyStatus(page)).toHaveText(language === 'en' ? 'Using environment key' : '使用环境变量中的密钥')
    await fetch.scrollIntoViewIfNeeded()
    await page.screenshot({ path: testInfo.outputPath(`webfetch-order-${language}.png`), animations: 'disabled' })
    const order = settings.getByRole('group', { name: language === 'en' ? 'Search priority' : '搜索优先顺序', exact: true })
    await expect(order.locator('.schema-array-row')).toHaveCount(5)
    await order.getByRole('button', { name: language === 'en' ? 'Move item 1 down' : '下移第 1 项', exact: true }).click({ trial: true })
    await order.getByRole('button', { name: language === 'en' ? 'Move item 4 up' : '上移第 4 项', exact: true }).click({ trial: true })
    await order.scrollIntoViewIfNeeded()
    await page.screenshot({ path: testInfo.outputPath(`search-order-${language}.png`), animations: 'disabled' })
    for (const name of ['Jina Reader', 'Firecrawl', 'Parallel', 'Tavily', 'Exa']) {
      const title = language === 'en' ? `Edit ${name} API key` : `编辑 ${name} API Key`
      const form = await editKey(page, name, title)
      const input = form.getByLabel('API Key', { exact: true })
      await input.fill('check-layout')
      const save = form.getByRole('button', { name: language === 'en' ? 'Save changes' : '保存更改', exact: true })
      await expect(save).toBeEnabled()
      await save.scrollIntoViewIfNeeded()
      const bounds = await save.evaluate(element => {
        const rect = element.getBoundingClientRect()
        const hit = document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2)
        return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom, width: innerWidth, height: innerHeight, uncovered: !!hit && element.contains(hit) }
      })
      expect(bounds.left).toBeGreaterThanOrEqual(-1)
      expect(bounds.right).toBeLessThanOrEqual(bounds.width + 1)
      expect(bounds.top).toBeGreaterThanOrEqual(-1)
      expect(bounds.bottom).toBeLessThanOrEqual(bounds.height + 1)
      expect(bounds.uncovered).toBe(true)
      await save.click({ trial: true })
      if (name === 'Jina Reader') await page.screenshot({ path: testInfo.outputPath(`reader-key-dialog-${language}.png`), animations: 'disabled' })
      await form.getByRole('button', { name: language === 'en' ? 'Cancel' : /^取\s*消$/, exact: true }).click()
      await expect(form).toBeHidden()
    }
    const overflow = await settings.evaluate(section => {
      const elements = [document.documentElement, section.closest('.ui-modal-container')!, section.querySelector('.settings-content')!, ...section.querySelectorAll('form, .schema-array-row')]
      return Math.max(...elements.map(element => element.scrollWidth - element.clientWidth))
    })
    expect(overflow).toBeLessThanOrEqual(2)
    const screenshot = testInfo.outputPath(`search-settings-${language}.png`)
    await page.screenshot({ path: screenshot, animations: 'disabled' })
    await testInfo.attach(`search-settings-${language}`, { path: screenshot, contentType: 'image/png' })
  }
  await page.emulateMedia({ colorScheme: 'dark' })
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
  await page.screenshot({ path: testInfo.outputPath('search-settings-dark.png'), animations: 'disabled' })
  const dialog = await editKey(page, 'Jina Reader', '编辑 Jina Reader API Key')
  await dialog.getByLabel('API Key', { exact: true }).click()
  await page.screenshot({ path: testInfo.outputPath('reader-key-dialog-dark.png'), animations: 'disabled' })
  await dialog.getByRole('button', { name: /^取\s*消$/, exact: true }).click()
  await expect(dialog).toBeHidden()
  expect(fixture.writes).toEqual([])
  expect(fixture.unexpected).toEqual([])
})
