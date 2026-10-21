import { test, expect } from './fixtures'

test.beforeEach(async ({ vista }) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
})

test('measured TPS matches persisted model timing and survives refresh', async ({ page, vista }) => {
  const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
  await page.goto(`${vista.origin}/sessions/${session.id}`)
  const sender = page.locator('.sender-card')
  await expect(sender.getByPlaceholder('Send a message')).toBeEnabled()
  await sender.getByPlaceholder('Send a message').fill('Hi')
  const send = sender.getByRole('button', { name: 'Send', exact: true })
  await send.scrollIntoViewIfNeeded()
  await expect(send).toBeEnabled()
  await send.click()
  const reply = page.locator('.messages article.assistant').filter({ hasText: 'Mock reply: Hi' })
  await expect(reply.locator('.model-tps')).toHaveText(/TPS \d+\.\d/)
  const messages = await vista.api(`/sessions/${session.id}/messages`)
  const result = messages.find((message: { role: string }) => message.role === 'assistant')?.modelResults?.[0]
  expect(result?.durationMs, 'New model results must retain their measured request time').toBeGreaterThan(0)
  expect(result?.usage?.outputTokens).toBe(5)
  const expected = ` · TPS ${(result.usage.outputTokens * 1000 / result.durationMs).toFixed(1)}`
  await expect(reply.locator('.model-tps')).toHaveText(expected)
  await expect(reply.locator('.model-tps')).toHaveAttribute('title', /including time to first token and excluding tool execution/)
  await page.reload()
  await expect(reply.locator('.model-tps')).toHaveText(expected)
})

test('missing or partial timing remains visible without invented TPS', async ({ page, vista }, testInfo) => {
  const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
  const usage = { inputTokens: 20, outputTokens: 10, cacheReadTokens: 0, reasoningTokens: 0 }
  const baseResult = { provider: 'openai-chat', id: vista.modelId, depth: 0, finishReason: 'completed' }
  const createdAt = new Date().toISOString()
  const scenarios = [
    { id: 'historical', title: 'Historical timing fixture', results: [{ ...baseResult, sequence: 1, turn: 1, usage }] },
    { id: 'mixed', title: 'Partial timing fixture', results: [{ ...baseResult, sequence: 2, turn: 1, usage }, { ...baseResult, sequence: 3, turn: 2, usage, durationMs: 400 }] },
    { id: 'missing-usage', title: 'Missing usage fixture', results: [{ ...baseResult, sequence: 4, turn: 1, durationMs: 500 }] },
    { id: 'measured', title: 'Measured timing fixture', results: [{ ...baseResult, sequence: 5, turn: 1, usage, durationMs: 400 }] },
  ]
  const messages = scenarios.map((scenario, index) => ({
    id: scenario.id, sequence: 10 + index, role: 'assistant', content: scenario.title,
    runId: scenario.id, createdAt, outputTurn: scenario.results.length,
    provider: 'openai-chat', modelId: vista.modelId, modelKey: 'test',
    modelResults: scenario.results.map(result => ({ ...result, runId: scenario.id })),
  }))
  await page.route('**/api/sessions/**', route => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (request.method() !== 'GET') return route.continue()
    if (path === `/api/sessions/${session.id}/messages`) return route.fulfill({ json: messages })
    if (path === `/api/sessions/${session.id}/event-page`) return route.fulfill({ json: [] })
    if (path === `/api/sessions/${session.id}/events`) return route.fulfill({ status: 200, contentType: 'text/event-stream', body: '' })
    if (path.includes('/runs/') && path.endsWith('/main-outputs')) return route.fulfill({ json: [] })
    return route.continue()
  })
  await page.goto(`${vista.origin}/sessions/${session.id}`)
  for (const scenario of scenarios) {
    const reply = page.locator('.messages article.assistant').filter({ hasText: scenario.title })
    await expect(reply.locator('.model-tps')).toHaveText(scenario.id === 'measured' ? ' · TPS 25.0' : ' · TPS —')
    await expect(reply.locator('.model-tps')).toHaveAttribute('title', scenario.id === 'measured'
      ? /including time to first token and excluding tool execution/
      : /Request timing or token usage is missing/)
  }
  const mixed = page.locator('.messages article.assistant').filter({ hasText: 'Partial timing fixture' })
  await mixed.getByRole('button', { name: 'Model usage details', exact: true }).click()
  const details = page.locator('.usage-popover:visible .usage-details')
  await expect(details.locator('thead th').last()).toHaveText('TPS')
  await expect(details.locator('tbody tr[data-turn="1"] td').last()).toHaveText('—')
  await expect(details.locator('tbody tr[data-turn="2"] td').last()).toHaveText('25.0')
  await expect(details.locator('.usage-summary')).toContainText('TPS —')
  await page.waitForTimeout(350)
  const geometry = await details.evaluate(element => {
    const bounds = element.getBoundingClientRect()
    const table = element.querySelector('.usage-table-scroll')!
    return { x: bounds.x, y: bounds.y, right: bounds.right, bottom: bounds.bottom,
      width: innerWidth, height: innerHeight, overflow: element.scrollHeight - element.clientHeight,
      tableWidth: table.clientWidth, tableScrollWidth: table.scrollWidth }
  })
  expect(geometry.x).toBeGreaterThanOrEqual(0)
  expect(geometry.y).toBeGreaterThanOrEqual(0)
  expect(geometry.right).toBeLessThanOrEqual(geometry.width + 2)
  expect(geometry.bottom).toBeLessThanOrEqual(geometry.height + 2)
  expect(geometry.overflow, 'Usage table must scroll within the bounded panel').toBeLessThanOrEqual(2)
  if (geometry.width < 800) expect(geometry.tableScrollWidth).toBeGreaterThan(geometry.tableWidth)
  await testInfo.attach('usage-table-layout', { body: await page.screenshot(), contentType: 'image/png' })
  await expect(page.locator('.messages')).not.toContainText(/TPS (?:NaN|Infinity)/)
})
