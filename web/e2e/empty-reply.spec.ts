import { test, expect } from './fixtures'
import type { StreamEvent } from '../src/client/types'

test.use({ providerOutput: 'responses', emptyReply: true })

test('normal empty completion is visible without a fabricated reply and survives refresh', async ({ page, vista }) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
  await page.reload()
  await page.locator('.sender-card textarea').fill('Finish without text')
  await page.locator('.sender-card textarea').press('Enter')
  const status = page.locator('.trace-overview-heading strong[role="status"]')
  for (const reload of [false, true]) {
    if (reload) await page.reload()
    await expect(status).toHaveText('Execution finished; model returned no text')
    await expect(page.locator('.run-overview')).toHaveAttribute('data-run-status', 'succeeded')
    await expect(page.locator('article.assistant')).toHaveCount(0)
    await expect(page.locator('.run-failure-message')).toHaveCount(0)
    await expect(page.locator('.sender-card textarea')).toBeEnabled()
  }
  const sessionPath = new URL(page.url()).pathname
  const events = await vista.api(`${sessionPath}/event-page`) as StreamEvent[]
  const finished = events.find(event => event.kind === 'run_finished')!
  expect(finished.data).toMatchObject({ status: 'succeeded', emptyReply: true, message: null })
  expect(vista.modelRequests).toHaveLength(1)
  await page.locator('.sender-card textarea').fill('Follow up')
  await page.locator('.sender-card textarea').press('Enter')
  await expect(status).toHaveCount(2)
  await expect(status.last()).toHaveText('Execution finished; model returned no text')
  await expect(page.locator('article.assistant')).toHaveCount(0)
  expect(vista.modelRequests).toHaveLength(2)
})
