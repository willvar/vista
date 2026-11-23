import type { Page, TestInfo } from '@playwright/test'
import { test, expect, type VistaFixture } from './fixtures'
import type { Message, SessionWindowSnapshot } from '@vista/client'
import { installMockStateStream, notifyState } from './state-stream-fixture'

test.use({ modelFailure: { status: 400, body: { error: { type: 'invalid_request_error',
  message: 'Synthetic prompt_rendering_failed <|start|> </rfl:parameter> test-key' } } } })

function diagnosticData(prompt: string) { return JSON.parse(prompt.split('\n\n```json\n')[1]!.slice(0, -4)) }

test('one click sends a localized diagnostic question in a new conversation and preserves the source', async ({ page, vista }, testInfo) => {
  await page.evaluate(() => localStorage.setItem('vista-language', 'zh-CN'))
  await page.reload()
  await expect(page.locator('html')).toHaveAttribute('lang', 'zh-CN')
  const mutations: { path: string; content?: string; modelKey?: string }[] = []
  page.on('request', request => {
    if (request.method() !== 'POST') return
    const path = new URL(request.url()).pathname
    if (path === '/api/sessions' || /^\/api\/sessions\/[^/]+\/messages$/.test(path)) mutations.push({ path, ...request.postDataJSON() })
  })
  await page.locator('.sender-card textarea').fill('检查这次测试故障')
  await page.locator('.sender-card textarea').press('Enter')
  await expect(page.locator('.run-failure-message')).toBeVisible()
  const originalPath = new URL(page.url()).pathname
  const { session: source } = await vista.api(`${originalPath}/checkpoint`)
  const original = await vista.api(`${originalPath}/messages`) as Message[]
  const failure = original.find(message => message.failure)!
  const help = page.locator('.run-failure-help')
  const analyze = help.getByRole('button', { name: '在新会话分析此故障', exact: true })
  await expect(help).toContainText('也可尝试手动压缩上下文后继续')
  const linkLayout = await analyze.evaluate(element => {
    const paragraph = element.closest('p')!
    const prefix = [...paragraph.childNodes].find(node => node.nodeType === Node.TEXT_NODE && node.textContent?.trim())!
    const before = document.createRange(), link = document.createRange()
    before.selectNodeContents(prefix)
    link.selectNodeContents(element)
    return {
      height: element.getBoundingClientRect().height,
      lineHeight: parseFloat(getComputedStyle(paragraph).lineHeight),
      textTopDifference: Math.abs(before.getClientRects()[0]!.top - link.getClientRects()[0]!.top),
    }
  })
  expect(linkLayout.height, 'The diagnostic link fits the surrounding text line').toBeLessThanOrEqual(linkLayout.lineHeight + 1)
  expect(linkLayout.textTopDifference, 'The diagnostic link follows the preceding text inline').toBeLessThanOrEqual(1)
  await analyze.hover()
  await expect(analyze).toHaveCSS('background-color', 'rgba(0, 0, 0, 0)')
  await expect(analyze).toHaveCSS('border-top-width', '0px')
  await page.locator('.sender-card textarea').fill('保留原会话草稿')
  await analyze.click()
  await expect.poll(() => new URL(page.url()).pathname).not.toBe(originalPath)
  await expect(page.locator('.run-failure-message')).toBeVisible()
  expect(mutations.map(item => item.path)).toEqual([
    '/api/sessions', `/api${originalPath}/messages`, '/api/sessions', `/api${new URL(page.url()).pathname}/messages`,
  ])
  expect(mutations[3]!.modelKey).toBe(mutations[1]!.modelKey)
  const prompt = mutations[3]!.content!
  expect(prompt.startsWith('请调查以下 Vista 故障')).toBe(true)
  expect(Buffer.byteLength(prompt)).toBeLessThanOrEqual(8192)
  expect(prompt).not.toContain('<|start|>')
  expect(prompt).not.toContain('</rfl:parameter>')
  expect(prompt).not.toContain('test-key')
  const data = diagnosticData(prompt)
  expect(data.source).toMatchObject({ sessionId: source.id, runId: failure.runId, kind: 'execution', messageId: failure.id })
  expect(data.error.content).toBe(failure.content)
  expect(data.recentMessages).toEqual([expect.objectContaining({ role: 'user', content: '检查这次测试故障' })])
  expect(vista.modelRequests).toHaveLength(2)
  expect(vista.modelRequests[1]!.messages?.[0]?.content).toContain('Default to replies in Simplified Chinese')
  expect(vista.modelRequests[1]!.messages?.filter(message => message.role === 'user')).toHaveLength(1)
  expect(await vista.api(`${originalPath}/messages`)).toEqual(original)
  await page.screenshot({ path: testInfo.outputPath('diagnostic-conversation-zh-CN.png'), animations: 'disabled' })
  await page.goto(vista.origin + originalPath)
  await expect(page.locator('.sender-card textarea')).toHaveValue('保留原会话草稿')
})

test('a compaction failure exposes the same diagnostic action and preserves the failed compaction', async ({ page, vista }) => {
  await analyzeFailedCompaction(page, vista)
})

test('a delayed creation summary preserves the selected model in the diagnostic conversation', { tag: '@responsive' }, async ({ page, vista }, testInfo) => {
  await analyzeFailedCompaction(page, vista, testInfo)
})

async function analyzeFailedCompaction(page: Page, vista: VistaFixture, orderedDelivery?: TestInfo) {
  const source = await vista.api('/sessions', { method: 'POST', body: '{}' })
  await vista.api(`/sessions/${source.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'Original failed task' }) })
  await expect.poll(async () => (await vista.api(`/sessions/${source.id}/checkpoint`)).session.executing).toBe(false)
  const original = await vista.api(`/sessions/${source.id}/messages`)
  await vista.api(`/sessions/${source.id}/compact`, { method: 'POST', body: '{}' })
  await expect.poll(async () => (await vista.api(`/sessions/${source.id}/context`))?.phase).toBe('failed')
  const failure = await vista.api(`/sessions/${source.id}/context`)
  if (orderedDelivery) await installMockStateStream(page)
  await page.goto(`${vista.origin}/sessions/${source.id}`)
  await page.locator('.compaction-details').click()
  const detail = page.locator('.compaction-error-detail')
  await expect(detail).toContainText('manual context compaction')
  let submitted: Record<string, unknown> | undefined
  if (orderedDelivery) {
    let creationSnapshot: SessionWindowSnapshot
    let destinationID: string
    const snapshotTitle = 'Diagnostic creation snapshot'
    await page.route('**/api/sessions', async route => {
      if (route.request().method() !== 'POST') return route.continue()
      const response = await route.fetch()
      destinationID = (await response.json()).id
      // A real saved title makes application of the earlier list observable,
      // including when the sidebar is hidden on a mobile viewport.
      await vista.api(`/sessions/${destinationID}`, { method: 'PATCH', body: JSON.stringify({ title: snapshotTitle }) })
      const page = await vista.api('/sessions?page=1&pageSize=20&open=true')
      creationSnapshot = { ...page, interest: { size: 20, sort: 'created-desc' } }
      await route.fulfill({ response })
    })
    await page.route('**/api/sessions/*/messages', async route => {
      if (route.request().method() !== 'POST') return route.continue()
      submitted = route.request().postDataJSON()
      const response = await route.fetch()
      // The message commits auto/quality before the older creation list is
      // delivered. Withhold only the acknowledgement until that list applies.
      await notifyState(page, ['sessions'], { sessionWindow: creationSnapshot })
      await expect(page.locator(`.session-card[data-session-id="${destinationID}"] .session-name`)).toHaveText(snapshotTitle)
      await route.fulfill({ response })
    })
  }
  await detail.getByRole('button', { name: 'Analyze this failure in a new conversation', exact: true }).click()
  await expect.poll(() => new URL(page.url()).pathname).not.toBe(`/sessions/${source.id}`)
  const destination = new URL(page.url()).pathname
  await expect.poll(async () => (await vista.api(`${destination}/checkpoint`)).session.executing).toBe(false)
  const checkpoint = await vista.api(`${destination}/checkpoint`)
  if (orderedDelivery) {
    await orderedDelivery.attach('diagnostic-selection', { body: JSON.stringify({ submitted, checkpoint }), contentType: 'application/json' })
    expect(submitted).toHaveProperty('modelKey')
    expect(checkpoint.session.manualModelKey).toBe(submitted?.modelKey)
  }
  await expect(page.locator('.composer-model')).toHaveCount(0)
  expect((await vista.api(`${destination}/checkpoint`)).session.manualModelKey).toBeTruthy()
  const messages = await vista.api(`${destination}/messages`) as Message[]
  const data = diagnosticData(messages.find(message => message.role === 'user')!.content)
  expect(data.source).toMatchObject({ sessionId: source.id, runId: failure.runId, kind: 'compaction' })
  expect(data.error.content).toBe(failure.error)
  expect(await vista.api(`/sessions/${source.id}/messages`)).toEqual(original)
  expect(await vista.api(`/sessions/${source.id}/context`)).toEqual(failure)
}
