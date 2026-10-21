import { openSettingsSection } from './settings-navigation'
import { test as base, expect, type Page } from '@playwright/test'
import { createServer } from 'vite'
import { resolve } from 'node:path'

const test = base.extend<{}, { mockOrigin: string }>({
  mockOrigin: [async ({}, use) => {
    const server = await createServer({
      root: resolve(import.meta.dirname, '..'), configFile: resolve(import.meta.dirname, '../vite.config.ts'),
      server: { host: '127.0.0.1', port: 0, strictPort: false, watch: { ignored: ['**/.vista/**'] } },
    })
    await server.listen()
    const address = server.httpServer!.address()
    if (!address || typeof address === 'string') throw new Error('Mock UI server did not start')
    try { await use(`http://127.0.0.1:${address.port}`) } finally { await server.close() }
  }, { scope: 'worker' }],
})

async function mockMCP(page: Page, origin: string) {
  const fixture = {
    servers: [] as Record<string, any>[], writes: [] as Record<string, any>[], canceled: [] as string[],
    unexpected: [] as string[], failRead: false, failSave: false, checkError: '', checkDelay: 0, loginDelay: 0,
    loginStatus: 'pending', logins: 0,
    tools: [
      { name: 'read_file', description: 'Read a local file.', inputSchema: { type: 'object', properties: { path: { type: 'string' } } } },
      { name: 'search_files', description: 'Find files by name.', inputSchema: { type: 'object' } },
    ],
  }
  await page.addInitScript(() => localStorage.setItem('vista-language', 'en'))
  await page.route('**/api/**', async route => {
    const request = route.request()
    const path = new URL(request.url()).pathname.slice(4)
    const method = request.method()
    const reply = (json: unknown) => route.fulfill({ json })
    if (path === '/mcp-config') {
      if (method === 'GET') {
        if (fixture.failRead) return route.fulfill({ status: 500, body: 'Could not load MCP settings' })
        return reply({ servers: fixture.servers.map(({ token, clientSecret, ...server }) => ({ ...server, hasToken: !!token, hasClientSecret: !!clientSecret })), maxServers: 4, defaultDirectory: '/home/user/project' })
      }
      const body = request.postDataJSON()
      fixture.writes.push({ method, ...body })
      if (fixture.failSave) return route.fulfill({ status: 500, body: 'Could not save MCP settings' })
      const current = fixture.servers.find(server => server.id === body.id)
      if (method === 'POST') fixture.servers.push({ ...body, id: `server-${fixture.writes.length}`, authorized: false, check: { status: 'unchecked', toolCount: 0 } })
      else if (method === 'PUT') {
        const token = body.clearToken ? '' : body.token || current!.token
        const clientSecret = body.clearClientSecret ? '' : body.clientSecret || current!.clientSecret
        const connectionChanged = ['transport', 'endpoint', 'command', 'directory', 'oauth', 'clientId', 'disabled'].some(key => body[key] !== current![key]) || JSON.stringify(body.args) !== JSON.stringify(current!.args) || token !== current!.token || clientSecret !== current!.clientSecret
        Object.assign(current!, body, { token, clientSecret, check: connectionChanged ? { status: 'unchecked', toolCount: 0 } : current!.check })
      }
      else if (method === 'PATCH') current!.disabled = body.disabled
      else if (method === 'DELETE') fixture.servers = fixture.servers.filter(server => server.id !== body.id)
      return route.fulfill({ status: 204 })
    }
    if (/^\/mcp-servers\/[^/]+\/check$/.test(path)) {
      const failure = fixture.checkError
      if (fixture.checkDelay) await new Promise(resolve => setTimeout(resolve, fixture.checkDelay))
      const check = { status: failure ? 'error' : 'available', checkedAt: new Date().toISOString(), toolCount: failure ? 0 : fixture.tools.length, error: failure }
      const server = fixture.servers.find(server => server.id === path.split('/')[2])
      if (!server) return route.fulfill({ status: 404, body: 'Unknown MCP server ID' })
      if (server) server.check = check
      return reply({ check, tools: failure ? [] : fixture.tools })
    }
    if (/^\/mcp-servers\/[^/]+\/login$/.test(path)) {
      const server = fixture.servers.find(server => server.id === path.split('/')[2])
      if (!server) return route.fulfill({ status: 404, body: 'Unknown MCP server ID' })
      const id = `login-${++fixture.logins}`
      if (fixture.loginDelay) await new Promise(resolve => setTimeout(resolve, fixture.loginDelay))
      return reply({ id, serverId: server.id, name: server.name, status: 'starting', expiresAt: new Date(Date.now() + 300000).toISOString() })
    }
    if (path.startsWith('/mcp-auth/')) {
      const id = path.split('/').at(-1)!
      if (method === 'DELETE') {
        fixture.canceled.push(id)
        return reply({ id, status: 'canceled' })
      }
      if (fixture.loginStatus === 'connected') fixture.servers.forEach(server => { if (server.oauth) server.authorized = true })
      return reply({ id, name: 'remote', status: fixture.loginStatus, authorizationUrl: 'https://auth.example/authorize', expiresAt: new Date(Date.now() + 300000).toISOString() })
    }
    if (method === 'GET') {
      if (path === '/preferences') return reply({ userId: 'mcp-settings-user', lastMode: 'manual', lastTier: 'speed', manualModelKey: '', reasoningByModelId: {}, feedbackInvitationSeen: true })
      if (path === '/models') return reply({ default: '', models: [] })
      if (path === '/agent-settings') return reply({ instructions: '', instructionsPath: '/home/test/.config/vista/AGENTS.md', maxBytes: 4096, delegationDepth: 1, maxDelegationDepth: 8 })
      if (path === '/sessions') return reply([])
      if (path === '/status') return reply({ state: 'running', sessions: { total: 0, open: 0, closed: 0, running: 0, workers: 0 }, workerMetrics: [] })
    }
    fixture.unexpected.push(`${method} ${path}`)
    return route.fulfill({ status: 404, body: 'Unmocked API request' })
  })
  await page.goto(`${origin}/settings/general`)
  await openSettingsSection(page, 'mcp')
  await expect(page.getByRole('button', { name: 'Add MCP server', exact: true })).toBeEnabled()
  return fixture
}

async function addRemote(page: Page, auth?: 'token' | 'oauth') {
  await page.getByRole('button', { name: 'Add MCP server', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'Add MCP server', exact: true })
  await dialog.getByLabel('Name', { exact: true }).fill('remote')
  await dialog.getByLabel('Server URL', { exact: true }).fill('https://mcp.example/tools')
  if (auth) {
    await dialog.getByLabel('Authentication', { exact: true }).click()
    await page.getByRole('option', { name: auth === 'token' ? 'Bearer token' : 'OAuth', exact: true }).click()
    if (auth === 'token') await dialog.getByLabel('Bearer token', { exact: true }).fill('private-token')
  }
  await dialog.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(dialog).toBeHidden()
  const row = page.locator('[data-mcp="remote"]')
  const id = await row.getAttribute('data-mcp-id')
  return page.locator(`[data-mcp-id="${id}"]`)
}

test('MCP services save, edit, toggle, reload and delete without exposing saved tokens', async ({ page, mockOrigin }) => {
  const fixture = await mockMCP(page, mockOrigin)
  const row = await addRemote(page, 'token')
  await expect(row).toContainText('Not checked')
  await expect(page.locator('.mcp-settings')).toContainText('Saved. Changes apply to the next run.')
  await row.getByRole('switch', { name: 'Enable remote' }).click()
  await expect(row.getByRole('switch')).not.toBeChecked()
  await page.reload()
  await expect(row).toContainText('Disabled')
  await row.getByRole('switch').click()
  await expect(row.getByRole('switch')).toBeChecked()
  await row.getByRole('button', { name: 'Edit remote', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'Edit MCP server', exact: true })
  await expect(dialog.getByLabel('Name', { exact: true })).toHaveValue('remote')
  await dialog.getByLabel('Name', { exact: true }).fill('Renamed tools')
  const token = dialog.getByLabel('Bearer token', { exact: true })
  await expect(token).toHaveValue('')
  await expect(token).toHaveAttribute('type', 'password')
  await expect(token).toHaveAttribute('placeholder', 'Leave blank to reuse the saved key')
  await dialog.getByLabel('Server URL', { exact: true }).fill('https://mcp.example/new')
  await dialog.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(dialog).toBeHidden()
  expect(fixture.servers[0]!.token).toBe('private-token')
  expect(fixture.writes.at(-1)!.id).toBe(fixture.servers[0]!.id)
  await expect(row).toHaveAttribute('data-mcp', 'Renamed tools')
  await expect(row).toContainText('https://mcp.example/new')
  await page.reload()
  await expect(row).toHaveAttribute('data-mcp', 'Renamed tools')
  await row.getByRole('switch', { name: 'Enable Renamed tools' }).click()
  await expect(row.getByRole('switch')).not.toBeChecked()
  await row.getByRole('switch').click()
  await row.getByRole('button', { name: 'Check connection & tools', exact: true }).click()
  const toolsDialog = page.getByRole('dialog', { name: 'Renamed tools · Tools', exact: true })
  await expect(toolsDialog.locator('.mcp-tool')).toHaveCount(2)
  await page.keyboard.press('Escape')
  await expect(toolsDialog).toBeHidden()
  await row.getByRole('button', { name: 'Edit Renamed tools', exact: true }).click()
  await dialog.getByLabel('Authentication', { exact: true }).click()
  await page.getByRole('option', { name: 'None', exact: true }).click()
  await dialog.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(dialog).toBeHidden()
  expect(fixture.servers[0]!.token).toBe('')
  const storage = await page.evaluate(() => JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage } }))
  expect(storage).not.toContain('private-token')
  await row.getByRole('button', { name: 'Delete Renamed tools', exact: true }).click()
  await page.getByRole('button', { name: 'Delete server', exact: true }).click()
  await expect(row).toHaveCount(0)
  await page.reload()
  await expect(page.locator('.mcp-empty')).toBeVisible()
  expect(fixture.unexpected).toEqual([])
})

test('stdio arguments are explicit and the inner editor dismisses without closing settings', async ({ page, mockOrigin }) => {
  const fixture = await mockMCP(page, mockOrigin)
  for (const dismiss of ['escape', 'outside']) {
    await page.getByRole('button', { name: 'Add MCP server', exact: true }).click()
    const dialog = page.getByRole('dialog', { name: 'Add MCP server', exact: true })
    await dialog.getByLabel('Name', { exact: true }).fill('discarded')
    if (dismiss === 'escape') await dialog.getByLabel('Name', { exact: true }).press('Escape')
    else await page.locator('.ant-modal-wrap').last().click({ position: { x: 5, y: 5 } })
    await expect(dialog).toBeHidden()
    await expect(page.locator('.settings-page')).toBeVisible()
  }
  await page.getByRole('button', { name: 'Add MCP server', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'Add MCP server', exact: true })
  await expect(dialog.getByLabel('Name', { exact: true })).toHaveValue('')
  await dialog.getByLabel('Name', { exact: true }).fill('files')
  await dialog.getByLabel('Transport', { exact: true }).click()
  await page.getByRole('option', { name: 'Local · stdio', exact: true }).click()
  await dialog.getByLabel('Command (absolute path)', { exact: true }).fill('/usr/bin/npx')
  await dialog.getByLabel('Arguments', { exact: true }).fill('-y\n@modelcontextprotocol/server-filesystem\n/home/user/a b')
  await expect(dialog.getByLabel('Working directory (absolute path)', { exact: true })).toHaveValue('/home/user/project')
  await dialog.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(dialog).toBeHidden()
  expect(fixture.servers[0]!.args).toEqual(['-y', '@modelcontextprotocol/server-filesystem', '/home/user/a b'])
  await expect(page.locator('[data-mcp="files"]')).toContainText('stdio')
  expect(fixture.writes).toHaveLength(1)
  expect(fixture.unexpected).toEqual([])
})

test('connection checks show tools, schemas, errors and retries, and canceled reads stay closed', async ({ page, mockOrigin }) => {
  const fixture = await mockMCP(page, mockOrigin)
  const row = await addRemote(page)
  await row.getByRole('button', { name: 'Check connection & tools', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'remote · Tools', exact: true })
  await expect(dialog.locator('.mcp-tool')).toHaveCount(2)
  await dialog.getByLabel('Search tools', { exact: true }).fill('read')
  await expect(dialog.locator('.mcp-tool')).toHaveCount(1)
  await dialog.getByText('Input parameters', { exact: true }).click()
  await expect(dialog.locator('pre')).toContainText('path')
  fixture.checkError = 'HTTP 403: service denied access'
  await dialog.getByRole('button', { name: 'Check again', exact: true }).click()
  await expect(dialog.getByRole('alert')).toContainText('403')
  await expect(dialog.locator('.mcp-tool')).toHaveCount(0)
  fixture.checkError = ''
  await dialog.getByRole('button', { name: 'Check again', exact: true }).click()
  await expect(dialog.locator('.mcp-tool')).toHaveCount(2)
  await page.locator('.ant-modal-wrap').last().click({ position: { x: 5, y: 5 } })
  await expect(dialog).toBeHidden()
  await expect(row).toContainText('Available')
  fixture.checkDelay = 400
  await row.getByRole('button', { name: 'Check connection & tools', exact: true }).click()
  await expect(dialog).toContainText('Connecting and discovering tools')
  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
  await expect(page.locator('.settings-page')).toBeVisible()
  await page.waitForTimeout(500)
  await expect(dialog).toBeHidden()
  expect(fixture.unexpected).toEqual([])
})

test('OAuth authorizes in browser, cancels active and late starts, and reports completion', async ({ page, mockOrigin }) => {
  const fixture = await mockMCP(page, mockOrigin)
  const row = await addRemote(page, 'oauth')
  await expect(row).toContainText('Authorization required')
  await row.getByRole('button', { name: 'Authorize', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'Authorize remote', exact: true })
  const link = dialog.getByRole('link', { name: 'Open authorization page', exact: true })
  await expect(link).toHaveAttribute('href', 'https://auth.example/authorize')
  await expect(link).toHaveAttribute('target', '_blank')
  await dialog.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect.poll(() => fixture.canceled).toEqual(['login-1'])
  fixture.loginDelay = 500
  await row.getByRole('button', { name: 'Authorize', exact: true }).click()
  await expect(dialog).toContainText('Preparing authorization')
  await dialog.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect.poll(() => fixture.canceled).toEqual(['login-1', 'login-2'])
  fixture.loginDelay = 0
  await row.getByRole('button', { name: 'Authorize', exact: true }).click()
  await expect(link).toBeVisible()
  fixture.loginStatus = 'connected'
  await expect(dialog.getByRole('status')).toHaveText('Authorized. Ready for the next run.')
  await dialog.locator('.mcp-form-actions').getByRole('button', { name: 'Close', exact: true }).click()
  await expect(row.getByRole('button', { name: 'Reauthorize', exact: true })).toBeVisible()
  expect(fixture.canceled).toEqual(['login-1', 'login-2'])
  const connectionID = fixture.servers[0]!.id
  await row.getByRole('button', { name: 'Edit remote', exact: true }).click()
  const editor = page.getByRole('dialog', { name: 'Edit MCP server', exact: true })
  await editor.getByLabel('Name', { exact: true }).fill('远程服务')
  await editor.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(editor).toBeHidden()
  await expect(row).toHaveAttribute('data-mcp', '远程服务')
  expect(fixture.servers[0]!.id).toBe(connectionID)
  expect(fixture.servers[0]!.authorized).toBe(true)
  await row.getByRole('button', { name: 'Reauthorize', exact: true }).click()
  const renamedLogin = page.getByRole('dialog', { name: 'Authorize 远程服务', exact: true })
  await expect(renamedLogin.getByRole('status')).toHaveText('Authorized. Ready for the next run.')
  await renamedLogin.locator('.mcp-form-actions').getByRole('button', { name: 'Close', exact: true }).click()
  expect(fixture.unexpected).toEqual([])
})

test('read and save failures are visible and preserve edits for retry', async ({ page, mockOrigin }) => {
  const fixture = await mockMCP(page, mockOrigin)
  await page.getByRole('button', { name: 'Add MCP server', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'Add MCP server', exact: true })
  await dialog.getByLabel('Name', { exact: true }).fill('retry')
  await dialog.getByLabel('Server URL', { exact: true }).fill('https://mcp.example/retry')
  fixture.failSave = true
  await dialog.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(dialog.getByRole('alert')).toContainText('Could not save MCP settings')
  await expect(dialog.getByLabel('Name', { exact: true })).toHaveValue('retry')
  fixture.failSave = false
  await dialog.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(dialog).toBeHidden()
  fixture.failRead = true
  await page.reload()
  await expect(page.getByRole('alert')).toContainText('Could not load MCP settings')
  fixture.failRead = false
  await page.getByRole('button', { name: 'Retry', exact: true }).click()
  await expect(page.locator('[data-mcp="retry"]')).toBeVisible()
  expect(fixture.unexpected).toEqual([])
})

test('MCP dialogs and long tools stay readable in both languages and themes', async ({ page, mockOrigin }, testInfo) => {
  const fixture = await mockMCP(page, mockOrigin)
  await addRemote(page, 'oauth')
  fixture.servers[0]!.endpoint = 'https://mcp.example/' + 'long-endpoint/'.repeat(15)
  for (const language of ['en', 'zh-CN']) {
    await page.evaluate(value => localStorage.setItem('vista-language', value), language)
    // The initial-language script only seeds new documents; use a cookie to
    // select the language on reload, as the application does.
    await page.context().addCookies([{ name: 'vista-language', value: language, url: mockOrigin }])
    await page.reload()
    const settings = page.locator('.settings-page')
    const row = settings.locator('[data-mcp="remote"]')
    await expect(row).toBeVisible()
    await row.getByRole('button', { name: language === 'en' ? 'Edit remote' : '编辑 remote', exact: true }).click()
    const dialog = page.getByRole('dialog', { name: language === 'en' ? 'Edit MCP server' : '编辑 MCP 服务', exact: true })
    const save = dialog.getByRole('button', { name: language === 'en' ? 'Save changes' : '保存更改', exact: true })
    await save.scrollIntoViewIfNeeded()
    await save.click({ trial: true })
    const overflow = await dialog.evaluate(element => element.scrollWidth - element.clientWidth)
    expect(overflow).toBeLessThanOrEqual(2)
    await page.screenshot({ path: testInfo.outputPath(`mcp-editor-${language}.png`), animations: 'disabled' })
    await dialog.getByRole('button', { name: language === 'en' ? 'Cancel' : /^取\s*消$/, exact: true }).click()
    await expect(dialog).toBeHidden()
    await page.emulateMedia({ colorScheme: 'dark' })
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
    await row.scrollIntoViewIfNeeded()
    expect(await settings.evaluate(element => element.scrollWidth - element.clientWidth)).toBeLessThanOrEqual(2)
    await page.screenshot({ path: testInfo.outputPath(`mcp-settings-dark-${language}.png`), animations: 'disabled' })
  }
  expect(fixture.unexpected).toEqual([])
})
