import { test, expect } from './fixtures'
import type { Message, StreamEvent } from '../src/client/types'

const explanation = "OpenCode's free tier can only be used from within OpenCode. Rejected <request> key: test-key."
const displayedExplanation = explanation.replace('test-key', '[redacted]')

for (const stream of [false, true]) {
  test.describe(stream ? 'Responses stream failure' : 'HTTP 403 failure', () => {
    test.use({
      providerOutput: stream ? 'responses' : undefined,
      modelFailure: { status: 403, stream, body: { type: 'error', error: { type: 'FreeTierError', message: explanation } } },
    })

    test('shows one upstream error in the conversation without an empty process entry after refresh', async ({ page, vista }, testInfo) => {
      await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
      await page.reload()
      await page.locator('.sender-card textarea').fill('Introduce yourself')
      await page.locator('.sender-card textarea').press('Enter')
      await expect(page.locator('article.assistant .run-failure-message')).toContainText(displayedExplanation)
      const sessionPath = new URL(page.url()).pathname
      const messages = await vista.api(`${sessionPath}/messages`) as Message[]
      const failure = messages.find(message => message.failure)
      expect(failure?.content).toContain('FreeTierError')
      expect(failure?.content).toContain(displayedExplanation)
      expect(failure?.content).not.toContain('test-key')
      const events = await vista.api(`${sessionPath}/event-page`) as StreamEvent[]
      expect(events.find(event => event.kind === 'run_finished')?.data).toMatchObject({ status: 'failed', message: { failure: true, content: failure!.content } })
      for (const reload of [false, true]) {
        if (reload) await page.reload()
        const message = page.locator('article.assistant .run-failure-message')
        await expect(message).toHaveCount(1)
        await expect(message).toHaveText(failure!.content)
        await expect(message).toHaveAttribute('role', 'alert')
        await expect(page.locator('.run-trace')).toHaveCount(0)
        await expect(page.getByRole('button', { name: 'Execution process', exact: true })).toHaveCount(0)
      }
      expect(vista.modelRequests).toHaveLength(1)
      await page.locator('.sender-card textarea').fill('Try again after failure')
      await page.locator('.sender-card textarea').press('Enter')
      await expect(page.locator('article.user .markdown').filter({ hasText: 'Try again after failure' })).toBeVisible()
      await expect(page.locator('article.assistant .run-failure-message')).toHaveCount(2)
      expect(vista.modelRequests).toHaveLength(2)
      await expect(page.locator('.pending-inputs')).toHaveCount(0)
      await expect(page.getByRole('button', { name: 'Resume', exact: true })).toHaveCount(0)
      await page.reload()
      await expect(page.locator('article.assistant .run-failure-message')).toHaveCount(2)
      await expect(page.locator('.pending-inputs')).toHaveCount(0)
      await page.screenshot({ path: testInfo.outputPath('provider-error.png'), animations: 'disabled' })
    })
  })
}
