import { test, expect } from './fixtures'
import type { Message } from '../src/client/types'

test.use({ modelFailure: { status: 400, body: { error: { type: 'invalid_request_error',
  message: 'Synthetic prompt_rendering_failed <|start|> </rfl:parameter> test-key' } } } })

function diagnosticData(prompt: string) { return JSON.parse(prompt.split('\n\n```json\n')[1]!.slice(0, -4)) }

test('one click sends a localized diagnostic question in a new conversation and preserves the source', async ({ page, vista }, testInfo) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
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
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
  const source = await vista.api('/sessions', { method: 'POST', body: '{}' })
  await vista.api(`/sessions/${source.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'Original failed task' }) })
  await expect.poll(async () => (await vista.api(`/sessions/${source.id}/checkpoint`)).session.executing).toBe(false)
  const original = await vista.api(`/sessions/${source.id}/messages`)
  await vista.api(`/sessions/${source.id}/compact`, { method: 'POST', body: '{}' })
  await expect.poll(async () => (await vista.api(`/sessions/${source.id}/context`))?.phase).toBe('failed')
  const failure = await vista.api(`/sessions/${source.id}/context`)
  // The only mock connection just failed. Clear its cooldown to exercise automatic delivery.
  await vista.api('/model-selection/stats', { method: 'DELETE', body: JSON.stringify({ scope: 'speed' }) })
  await vista.api(`/sessions/${source.id}/selection`, { method: 'PATCH', body: JSON.stringify({ selectionMode: 'auto', selectionTier: 'quality' }) })
  await page.goto(`${vista.origin}/sessions/${source.id}`)
  await page.locator('.context-status').click()
  const detail = page.locator('.context-status-detail')
  await expect(detail).toContainText('manual context compaction')
  await detail.getByRole('button', { name: 'Analyze this failure in a new conversation', exact: true }).click()
  await expect.poll(() => new URL(page.url()).pathname).not.toBe(`/sessions/${source.id}`)
  const destination = new URL(page.url()).pathname
  await expect.poll(async () => (await vista.api(`${destination}/checkpoint`)).session.executing).toBe(false)
  await expect(page.locator('.sender-card-auto')).toBeVisible()
  await expect(page.locator('.composer-tier')).toContainText('Quality')
  expect((await vista.api(`${destination}/checkpoint`)).session).toMatchObject({ selectionMode: 'auto', selectionTier: 'quality' })
  const messages = await vista.api(`${destination}/messages`) as Message[]
  const data = diagnosticData(messages.find(message => message.role === 'user')!.content)
  expect(data.source).toMatchObject({ sessionId: source.id, runId: failure.runId, kind: 'compaction' })
  expect(data.error.content).toBe(failure.error)
  expect(await vista.api(`/sessions/${source.id}/messages`)).toEqual(original)
  expect(await vista.api(`/sessions/${source.id}/context`)).toEqual(failure)
})
