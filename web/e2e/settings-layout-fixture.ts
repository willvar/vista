import { test as base, expect } from '@playwright/test'
import { startFrontend } from './frontend-server'
import { nativeFixture } from './fixtures'
import { sharedFixture } from './shared-fixture'
import { stateTopics } from '../src/client/workspaceSync'
import type { ServerResponse } from 'node:http'

type SettingsFrontend = { origin: string; unexpected: string[] }

export const test = base.extend<{ mockOrigin: string }, { settingsResponses: Record<string, unknown>; settingsFrontend: SettingsFrontend }>({
  settingsResponses: [async ({}, use) => {
    // Capture the current implementation's public responses once per run.
    // Geometry tests do not maintain another set of provider/schema defaults.
    const responses = await sharedFixture('settings-layout', async () => {
      const values: Record<string, unknown> = {}
      await nativeFixture.use({}, async vista => {
        await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
        await vista.api('/resource-storage', { method: 'POST', body: JSON.stringify({ action: 'scan' }) })
        await expect.poll(async () => (await vista.api('/resource-storage')).phase).toBe('idle')
        const paths = ['/preferences', '/agent-settings', '/models', '/status', '/sessions?open=true',
          '/model-config', '/provider-config', '/model-catalog', '/mcp-config', '/skills',
          '/resource-storage', '/search-config', '/server-config', '/user-agent']
        await Promise.all(paths.map(async path => { values[path] = await vista.api(path) }))
      })
      return values
    })
    await use(responses)
  }, { scope: 'worker' }],
  settingsFrontend: [async ({ settingsResponses }, use) => {
    const unexpected: string[] = []
    const responses = new Map(Object.entries(settingsResponses).map(([path, value]) => [path, JSON.stringify(value)]))
    const clients = new Set<ServerResponse>()
    const frame = `event: state\ndata: ${JSON.stringify({ epoch: 'settings-layout', revisions: Object.fromEntries(stateTopics.map(topic => [topic, 0])) })}\n\n`
    // Serve immutable responses over loopback rather than serializing large
    // provider schemas through Playwright route.fulfill for every page read.
    const server = await startFrontend({ handle: (request, response) => {
      if (!request.url?.startsWith('/api/')) return false
      const path = request.url.slice(4)
      if (request.method === 'GET' && path.startsWith('/events') && new URL(path, 'http://fixture').pathname === '/events') {
        response.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-store' })
        clients.add(response)
        response.write(frame)
        const timer = setInterval(() => response.write(frame), 15_000)
        response.on('close', () => { clearInterval(timer); clients.delete(response) })
      } else if (request.method === 'POST' && path === '/activity') response.writeHead(204).end()
      else if (request.method === 'GET' && responses.has(path)) response.writeHead(200, { 'Content-Type': 'application/json' }).end(responses.get(path))
      else {
        unexpected.push(`${request.method} ${path}`)
        response.writeHead(500).end('Layout fixtures forbid uncaptured reads, changes and model requests')
      }
      return true
    } })
    try { await use({ origin: server.origin, unexpected }) }
    finally { for (const client of clients) client.end(); await server.close() }
  }, { scope: 'worker' }],
  mockOrigin: async ({ settingsFrontend }, use) => { await use(settingsFrontend.origin) },
  page: async ({ page, settingsFrontend }, use) => {
    const start = settingsFrontend.unexpected.length
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    await use(page)
    expect(settingsFrontend.unexpected.slice(start), 'Layout checks must only consume captured settings responses').toEqual([])
    expect(errors, 'Settings and language chunks must render without runtime errors').toEqual([])
  },
})
