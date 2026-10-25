import { test, expect } from './fixtures'
import type { Session, SessionListSnapshot } from '../src/client/types'

test('archives stay out of homepage HTTP and SSE and synchronize only while the manager is open', async ({ page, vista }, info) => {
  test.skip(info.project.name !== 'desktop', 'Payload ownership is independent of viewport')
  await page.goto('about:blank')
  const live = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Open summary' }) })
  const closed: Session[] = []
  for (let i = 0; i < 12; i++) {
    const item = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: `Archive only ${i}: ` + 'Saved title '.repeat(10) }) })
    await vista.api(`/sessions/${item.id}/close`, { method: 'POST' })
    closed.push(item)
  }
  await page.addInitScript(() => {
    const original = window.fetch.bind(window)
    const control = { streams: 0, frames: [] as object[] }
    Object.assign(window, { sessionScopeTest: control })
    window.fetch = async (input, init) => {
      const response = await original(input, init)
      const path = new URL(typeof input === 'string' ? input : input instanceof URL ? input : input.url, location.href).pathname
      if (path !== '/api/events' || !response.body) return response
      control.streams++
      const reader = response.body.getReader()
      const decoder = new TextDecoder()
      let buffer = ''
      return new Response(new ReadableStream<Uint8Array>({
        async start(sink) {
          try {
            while (true) {
              const part = await reader.read()
              if (part.done) { sink.close(); return }
              buffer += decoder.decode(part.value, { stream: true })
              let end: number
              while ((end = buffer.indexOf('\n\n')) >= 0) {
                const frame = buffer.slice(0, end)
                buffer = buffer.slice(end + 2)
                if (frame.startsWith('event: state\n')) control.frames.push(JSON.parse(frame.split('\ndata: ')[1]!))
              }
              sink.enqueue(part.value)
            }
          } catch (error) { try { sink.error(error) } catch {} }
          finally { reader.releaseLock() }
        },
        cancel() { return reader.cancel().catch(() => {}) },
      }), { headers: response.headers, status: response.status })
    }
  })
  const reads: { scope: string; items: Session[]; bytes: number }[] = []
  const unrelated: string[] = []
  page.on('response', async response => {
    const url = new URL(response.url())
    if (response.request().method() === 'GET' && url.pathname === '/api/sessions') {
      const body = await response.text()
      reads.push({ scope: url.search, items: JSON.parse(body), bytes: Buffer.byteLength(body) })
    }
  })
  await page.goto(vista.origin + '/')
  await expect(page.locator('.session-card')).toHaveCount(1)
  await expect.poll(() => reads.length).toBe(1)
  expect(reads[0]!.scope).toBe('?open=true')
  expect(reads[0]!.items.map(item => item.id)).toEqual([live.id])
  expect(reads[0]!.bytes).toBeLessThan(2000)
  page.on('request', request => {
    const path = new URL(request.url()).pathname
    if (request.method() === 'GET' && ['/api/status', '/api/models'].includes(path)) unrelated.push(path)
  })
  await vista.api(`/sessions/${live.id}`, { method: 'PATCH', body: JSON.stringify({ title: 'Pushed open summary' }) })
  await expect(page.locator('.session-card')).toContainText('Pushed open summary')
  const frames = await page.evaluate(() => (window as unknown as { sessionScopeTest: { frames: { sessions?: SessionListSnapshot }[] } }).sessionScopeTest.frames)
  const list = frames.filter(frame => frame.sessions).at(-1)!.sessions!
  expect(list.items.map(item => item.id)).toEqual([live.id])
  expect(Object.keys(list.throughSequences)).toEqual([live.id])
  expect(JSON.stringify(frames)).not.toContain('Archive only')
  expect(reads).toHaveLength(1)

  await page.locator('.manage-sessions-link').click()
  const manager = page.locator('.session-manager-page')
  await expect(manager.locator('.managed-session')).toHaveCount(13)
  const archiveReads = () => reads.filter(read => read.scope === '')
  expect(archiveReads()).toHaveLength(1)
  expect(archiveReads()[0]!.items).toHaveLength(13)
  expect(archiveReads()[0]!.items.filter(item => !item.open)).toHaveLength(12)
  await vista.api(`/sessions/${closed[0]!.id}`, { method: 'PATCH', body: JSON.stringify({ title: 'Archive changed in another client' }) })
  await expect(manager).toContainText('Archive changed in another client')
  const beforeExit = archiveReads().length
  await manager.getByRole('button', { name: 'Close session manager', exact: true }).click()
  await expect(manager).toHaveCount(0)
  await vista.api(`/sessions/${closed[0]!.id}`, { method: 'PATCH', body: JSON.stringify({ title: 'Archive changed after leaving' }) })
  await vista.api(`/sessions/${live.id}`, { method: 'PATCH', body: JSON.stringify({ title: 'Still synchronized' }) })
  await expect(page.locator('.session-card')).toContainText('Still synchronized')
  await page.waitForTimeout(250)
  expect(archiveReads()).toHaveLength(beforeExit)
  expect(unrelated).toEqual([])
  expect(await page.evaluate(() => (window as unknown as { sessionScopeTest: { streams: number } }).sessionScopeTest.streams)).toBe(1)
  await info.attach('session-payloads', { body: JSON.stringify({ reads: reads.map(({ scope, bytes, items }) => ({ scope, bytes, count: items.length })), globalListBytes: Buffer.byteLength(JSON.stringify(list)) }), contentType: 'application/json' })
})

test('a homepage with only closed conversations shows the retained-conversation empty state', async ({ page, vista }) => {
  const item = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Retained conversation' }) })
  await vista.api(`/sessions/${item.id}/close`, { method: 'POST' })
  await page.goto(vista.origin + '/')
  await expect(page.locator('.sidebar-empty')).toHaveText('No open sessions. Find closed sessions in Manage sessions.')
  await page.goto(vista.origin + '/sessions')
  await expect(page.locator('.managed-session')).toHaveCount(1)
  await expect(page.locator('.session-manager-stats').first()).toContainText('1')
})

test('external closing preserves the current draft and confirmed deletion returns home without loading archives', async ({ page, vista }) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
  const item = await vista.api('/sessions', { method: 'POST', body: JSON.stringify({ title: 'Selected conversation' }) })
  await page.goto(`${vista.origin}/sessions/${item.id}`)
  const sender = page.locator('.sender-card textarea')
  await expect(page.locator('.conversation-header h2')).toHaveText('Selected conversation')
  await expect(sender).toBeEnabled()
  await sender.fill('Retain this unsent draft')
  await expect(sender).toHaveValue('Retain this unsent draft')
  const reads: string[] = []
  page.on('request', request => {
    const url = new URL(request.url())
    if (request.method() === 'GET' && url.pathname.startsWith('/api/sessions')) reads.push(url.pathname + url.search)
  })
  await vista.api(`/sessions/${item.id}/close`, { method: 'POST' })
  await expect(sender).toBeDisabled()
  await expect(sender).toHaveValue('Retain this unsent draft')
  await expect(page).toHaveURL(`${vista.origin}/sessions/${item.id}`)
  await vista.api(`/sessions/${item.id}`, { method: 'DELETE' })
  await expect(page).toHaveURL(vista.origin + '/')
  await expect(page.locator('.session-card')).toHaveCount(0)
  expect(reads.some(path => path.endsWith('/checkpoint'))).toBe(true)
  expect(reads.some(path => path === '/api/sessions' || path.includes('open=false'))).toBe(false)
  await expect(page.locator('.state-sync-error')).toHaveCount(0)
})
