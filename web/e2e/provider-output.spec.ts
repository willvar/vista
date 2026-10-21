import { test, expect } from './fixtures'
import type { StreamEvent } from '../src/client/types'

const source = 'https://example.com/source'

for (const format of ['perplexity', 'openrouter', 'responses'] as const) {
  test.describe(format, () => {
    test.use({ providerOutput: format })

    test('citations and provider output survive refresh without replaying the request', async ({ page, vista }) => {
      await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
      await page.reload()
      await page.locator('.sender-card textarea').fill('Find the source')
      await page.getByRole('button', { name: 'Send', exact: true }).click()
      await expect(page.locator('article.assistant .reply-footer')).toBeVisible()
      const sessionPath = new URL(page.url()).pathname
      const history = await vista.api(`${sessionPath}/event-page`) as StreamEvent[]
      expect(history.filter(item => item.kind === 'source').map(item => item.data.content)).toEqual([source])
      if (format === 'responses') {
        expect(vista.modelRequests[0]?.reasoning?.summary).toBe('auto')
        expect(history.filter(item => item.kind === 'model_reasoning_delta').map(item => item.data.content)).toEqual(['Checked the available source before answering.'])
        const hosted = history.filter(item => item.kind === 'tool' && item.data.tool?.callId === 'hosted-fixture')
        expect(hosted).toHaveLength(1)
        expect(hosted[0]!.data).toMatchObject({ taskId: 'main', status: 'succeeded', tool: { providerExecuted: true } })
      }
      for (const reload of [false, true]) {
        if (reload) await page.reload()
        await expect(page.locator('article.assistant .markdown')).toHaveText('Cited answer.')
        const trace = page.locator('.run-trace')
        await trace.locator('.trace-overview-heading').click()
        await trace.locator('.antd-thought-chain-node-title').filter({ hasText: /^Sources$/ }).click()
        await expect(trace.getByRole('link', { name: source, exact: true })).toHaveAttribute('href', source)
        if (format === 'responses') {
          const reasoning = trace.locator('.trace-reasoning')
          await expect(reasoning).toHaveText('Checked the available source before answering.')
          await expect(reasoning.locator('.reply-footer')).toHaveCount(0)
          await expect(reasoning.locator('button, .antd-thought-chain')).toHaveCount(0)
          await trace.locator('.antd-thought-chain-node-title').filter({ hasText: 'Executed by Provider' }).click()
          await expect(trace.locator('pre.trace-output')).toContainText('Provider executed this tool.')
        }
      }
      expect(vista.modelRequests).toHaveLength(1)
    })
  })
}

test.describe('Responses terminal reasoning', () => {
  test.use({ providerOutput: 'responses', terminalReasoning: true })

  test('terminal summary stays in execution and reads directly after one expansion', async ({ page, vista }, testInfo) => {
    await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
    await page.reload()
    await page.locator('.sender-card textarea').fill('Find the source')
    await page.locator('.sender-card textarea').press('Enter')
    for (const reload of [false, true]) {
      if (reload) await page.reload()
      const reasoning = page.locator('.run-trace .trace-reasoning')
      const answer = page.locator('article.assistant .markdown')
      await expect(reasoning).toBeHidden()
      await expect(answer).toHaveText('Cited answer.')
      await expect(page.locator('.message-row article .markdown')).toHaveText([
        'Find the source', 'Cited answer.',
      ])
      await expect(page.locator('.trace-overview-heading')).toHaveAttribute('aria-expanded', 'false')
      await page.locator('.trace-overview-heading').click()
      await expect(reasoning).toHaveText('Checked the available source before answering.')
      await expect(reasoning.locator('button, .antd-thought-chain')).toHaveCount(0)
      const reasoningColor = await reasoning.evaluate(el => getComputedStyle(el).color)
      expect(reasoningColor).not.toBe(await answer.evaluate(el => getComputedStyle(el).color))
      await expect(reasoning.locator('.reply-footer')).toHaveCount(0)
    }
    await page.screenshot({ path: testInfo.outputPath('reasoning-in-process.png'), animations: 'disabled' })
    expect(vista.modelRequests).toHaveLength(1)
  })
})
