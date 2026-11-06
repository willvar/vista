import { test, expect } from './fixtures'
import type { Message, StreamEvent } from '../src/client/types'
import type { ModelDiagnosticSummary } from '../src/client/modelDiagnostics'

test.describe('transient model failure', () => {
  test.use({ modelFailure: { status: 502, attempts: 1, retryAfterMs: 8000,
    body: { error: { type: 'server_error', message: 'Synthetic upstream request error: test-key' } } } })

  test('waiting survives reload and the recovered request has separate diagnostics', async ({ page, vista }) => {
    await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
    await page.evaluate(() => { localStorage.setItem('vista-debug-mode', 'true'); document.cookie = 'vista-debug-mode=true; Path=/' })
    await page.reload()
    await page.locator('.sender-card textarea').fill('Recover this request')
    await page.locator('.sender-card textarea').press('Enter')
    const status = page.locator('.trace-overview-heading strong[role="status"]')
    await expect(status).toContainText('waiting for retry 1/3')
    const sessionPath = new URL(page.url()).pathname
    await page.reload()
    await expect(status).toContainText('waiting for retry 1/3')
    expect(vista.modelRequests).toHaveLength(1)
    await expect(page.locator('article.assistant .markdown')).toContainText('Mock reply: Hi', { timeout: 15_000 })
    await expect(page.locator('.run-failure-message')).toHaveCount(0)
    expect(vista.modelRequests).toHaveLength(2)
    expect(vista.modelRequests[1]).toEqual(vista.modelRequests[0])
    const messages = await vista.api(`${sessionPath}/messages`) as Message[]
    const events = await vista.api(`${sessionPath}/event-page`) as StreamEvent[]
    expect(events.filter(event => event.kind === 'model_request')).toHaveLength(2)
    expect(events.filter(event => event.kind === 'model_retry').map(event => event.data.status)).toEqual(['waiting', 'succeeded'])
    const calls = (await vista.api(`${sessionPath}/runs/${messages[0]!.runId}/diagnostics`) as ModelDiagnosticSummary[])
      .filter(call => call.purpose === 'conversation')
    expect(calls).toHaveLength(2)
    expect(calls.filter(call => call.error).map(call => call.error)).toEqual([expect.stringContaining('[redacted]')])
    await page.locator('.trace-overview-heading').click()
    const retry = page.locator('[data-step-key^="retry/"]')
    await expect(retry).toHaveCount(1)
    await expect(retry).toContainText('Request recovered after retry 1/3')
    await retry.getByRole('button').first().click()
    await expect(retry).toContainText('Synthetic upstream request error: [redacted]')
    await expect(retry).not.toContainText('test-key')
  })
})

test.describe('cancellable recovery', () => {
  test.use({ modelFailure: { status: 503, retryAfterMs: 3000, body: { error: { message: 'Try later' } } } })

  test('stopping during the wait prevents another upstream attempt', async ({ page, vista }) => {
    await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
    await page.reload()
    await page.locator('.sender-card textarea').fill('Stop the pending retry')
    await page.locator('.sender-card textarea').press('Enter')
    await expect(page.locator('.trace-overview-heading strong[role="status"]')).toContainText('waiting for retry 1/3')
    await page.getByRole('button', { name: 'Stop current execution', exact: true }).click()
    await expect(page.locator('.run-overview')).toHaveAttribute('data-run-status', 'interrupted')
    // Observe past the provider's minimum delay to catch an uncancelled timer.
    await page.waitForTimeout(3500)
    expect(vista.modelRequests).toHaveLength(1)
    await page.reload()
    await expect(page.locator('.trace-overview-heading strong[role="status"]')).not.toContainText('waiting for retry')
    await page.locator('.trace-overview-heading').click()
    await expect(page.locator('[data-step-key^="retry/"]')).toContainText('interrupted')
  })
})

test.describe('exhausted recovery', () => {
  test.use({ modelFailure: { status: 502, attempts: 4, body: { error: { message: 'Upstream is unavailable' } } } })

  test('continuation keeps the original task, failure and unsent draft in the same conversation', async ({ page, vista }) => {
    test.setTimeout(45_000)
    await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
    await page.reload()
    await page.locator('.sender-card textarea').fill('Complete the original task')
    await page.locator('.sender-card textarea').press('Enter')
    await expect(page.locator('.run-failure-message')).toContainText('Upstream is unavailable', { timeout: 24_000 })
    expect(vista.modelRequests).toHaveLength(4)
    const sessionPath = new URL(page.url()).pathname
    await page.reload()
    const retry = page.locator('.run-failure-retry').getByRole('button', { name: 'Continue execution', exact: true })
    await expect(retry).toBeVisible()
    await expect(retry).toHaveCSS('padding-left', '0px')
    await expect(retry).toHaveCSS('border-top-width', '0px')
    await page.locator('.sender-card textarea').fill('Keep my unsent draft')
    await retry.click()
    await expect(page.locator('article.assistant .markdown').last()).toContainText('Mock reply: Hi')
    expect(new URL(page.url()).pathname).toBe(sessionPath)
    expect(vista.modelRequests).toHaveLength(5)
    const users = vista.modelRequests.at(-1)!.messages!.filter(message => message.role === 'user')
    expect(users).toHaveLength(2)
    expect(users[0]!.content).toBe('Complete the original task')
    expect(users[1]!.content).toContain('Continue the previous task from its saved progress')
    expect(users[1]!.content).not.toContain('Keep my unsent draft')
    await expect(page.locator('.sender-card textarea')).toHaveValue('Keep my unsent draft')
    await expect(page.locator('.run-failure-message')).toHaveCount(1)
    await expect(page.locator('.run-failure-retry')).toHaveCount(0)
  })
})

test.describe('continuation after completed tools', () => {
  test.use({ progressCalls: 1 })

  test('a new run restores completed tool results from the failed execution', async ({ page, vista }) => {
    await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
    await page.reload()
    await page.locator('.sender-card textarea').fill('Fail progress')
    await page.locator('.sender-card textarea').press('Enter')
    await expect(page.locator('.run-failure-message')).toContainText('Synthetic failure after saved progress')
    expect(vista.modelRequests).toHaveLength(2)
    await page.reload()
    await page.locator('.run-failure-retry').getByRole('button', { name: 'Continue execution', exact: true }).click()
    await expect(page.locator('article.assistant .markdown').last()).toHaveText('Final progress answer.')
    const restored = vista.modelRequests[2]!.messages!
    expect(restored.filter(message => message.role === 'tool')).toEqual([
      expect.objectContaining({ tool_call_id: 'progress-1', content: expect.stringContaining('Tool result') }),
    ])
    expect(restored.find(message => message.tool_calls)?.tool_calls).toEqual([
      expect.objectContaining({ id: 'progress-1', function: expect.objectContaining({ name: 'shell' }) }),
    ])
    await expect(page.locator('.run-failure-message')).toHaveCount(1)
  })
})
