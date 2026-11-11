import { test, expect } from './fixtures'
import { scrollToConversationEntry } from './conversation-scroll'
import { test as historyTest } from './long-history-fixture'

test.describe('interleaved reply text and reasoning', () => {
  test.use({ providerOutput: 'responses', interleavedOutput: true })

  test('opening, reasoning and answer keep their order during execution, recovery and reload', async ({ page, context, vista }) => {
    await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    await page.reload()
    const release = vista.pauseModelReplies(true)
    const reads: string[] = []
    page.on('request', request => { if (request.url().includes('/model-output?')) reads.push(request.url()) })
    const opening = page.locator('.main-output .markdown')
    const reasoning = page.locator('.trace-reasoning')
    const answer = page.locator('article.assistant:not(.main-output)')
    const order = () => page.locator('.message-row.assistant .markdown, .trace-reasoning').evaluateAll(elements => elements
      .map(element => ({ text: element.textContent?.trim(), y: element.getBoundingClientRect().top }))
      .sort((a, b) => a.y - b.y).map(item => item.text))
    try {
      await page.locator('.sender-card textarea').fill('Explain the result')
      await page.getByRole('button', { name: 'Send', exact: true }).click()
      for (const reload of [false, true]) {
        if (reload) await page.reload()
        await expect(opening).toHaveText('Opening🙂.')
        await expect(reasoning).toHaveText('Checking one. Checking two. Checking three.')
        await expect.poll(order).toEqual(['Opening🙂.', 'Checking one. Checking two. Checking three.'])
        await expect(page.locator('.message-row.assistant .reply-footer')).toHaveCount(0)
      }
      release()
      await expect(answer.locator('.markdown')).toHaveText('Final answer🙂.')
      const sessionPath = new URL(page.url()).pathname
      const messages = await vista.api(`${sessionPath}/messages`)
      expect(messages).toHaveLength(2)
      expect(messages[1].content).toBe('Opening🙂. Final answer🙂.')
      const outputs = await vista.api(`${sessionPath}/runs/${messages[1].runId}/main-outputs`)
      expect(outputs).toHaveLength(2)
      expect(outputs.map((output: { offset: number; endOffset: number }) => [output.offset, output.endOffset])).toEqual([
        [0, Buffer.byteLength('Opening🙂. ')], [Buffer.byteLength('Opening🙂. '), Buffer.byteLength(messages[1].content)],
      ])
      for (const reload of [false, true]) {
        if (reload) {
          reads.length = 0
          await page.reload()
        }
        await expect(opening).toHaveText('Opening🙂.')
        await expect(answer.locator('.markdown')).toHaveText('Final answer🙂.')
        await expect(reasoning).toHaveText('Checking one. Checking two. Checking three.')
        await expect.poll(order).toEqual(['Opening🙂.', 'Checking one. Checking two. Checking three.', 'Final answer🙂.'])
        await expect(page.locator('.message-row.assistant .reply-footer')).toHaveCount(1)
        await expect(page.locator('.message-row.assistant .message-model-name')).toHaveCount(1)
        await answer.getByRole('button', { name: 'Copy reply', exact: true }).click()
        await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(messages[1].content)
        if (reload) expect(reads.length).toBeLessThanOrEqual(1)
      }
      expect(vista.modelRequests).toHaveLength(1)
    } finally { release() }
  })
})

test.describe('persistent main output', () => {
  test.use({ progressCalls: 6 })

  test('six progress bodies survive tools, final reply and refresh with one process entry', async ({ page, vista }, testInfo) => {
    await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
    await page.reload()
    const release = vista.pauseModelReplies(true)
    try {
      await page.locator('.sender-card textarea').fill('Show progress')
      await page.locator('.sender-card textarea').press('Enter')
      await expect(page.locator('.main-output').first()).toContainText('Progress 1.')
      release()
      await expect(page.locator('article.assistant').last()).toContainText('Final progress answer.')
      const id = new URL(page.url()).pathname.split('/').at(-1)!
      const messages = await vista.api(`/sessions/${id}/messages`)
      expect(messages).toHaveLength(2)
      expect(messages[1].modelResults).toHaveLength(7)
      const check = async () => {
        await expect(page.locator('.message-space')).toHaveClass(/virtualized/)
        await expect(page.locator('.message-space')).toHaveAttribute('data-count', '8')
        await expect(page.locator('.run-trace')).toHaveCount(1)
        await expect(page.locator('.trace-steps')).toHaveAttribute('data-step-count', '13')
        await expect(page.locator('[data-display-key^="reasoning/"]')).toHaveCount(0)
        const outputs = await vista.api(`/sessions/${id}/runs/${messages[0].runId}/main-outputs`)
        for (let i = 0; i < 6; i++) {
          const row = await scrollToConversationEntry(page, `[data-display-key="output/${messages[0].runId}/main/${i + 1}"]`, outputs[i].sequence)
          await expect(row).toContainText(`Progress ${i + 1}.`)
          await expect(row.locator('.message-actions, .reply-footer, .message-meta small, .message-model-results')).toHaveCount(0)
          await expect(row.locator('.message-meta time')).toHaveCount(1)
          await expect(page.locator('.trace-reasoning').first()).toHaveText('Private progress reasoning.')
        }
        await scrollToConversationEntry(page, `[data-reply-sequence="${messages[1].sequence}"]`, Infinity)
        const finalReply = page.locator('article.assistant:not(.main-output)')
        await expect(finalReply.getByRole('button', { name: 'Copy reply', exact: true })).toHaveCount(1)
        await expect(finalReply.locator('.message-meta small')).toHaveCount(0)
        await expect(finalReply.locator('.reply-footer .message-model-name')).toContainText('Layout test')
        await expect(finalReply.locator('.message-model-name + .token-usage')).toContainText('input 140')
        expect(await page.locator('.messages').evaluate(el => el.scrollWidth <= el.clientWidth + 1)).toBe(true)
      }
      await check()
      await page.reload()
      await check()
      await page.locator('article.assistant:not(.main-output)').scrollIntoViewIfNeeded()
      await page.screenshot({ path: testInfo.outputPath('reply-model-footer.png'), animations: 'disabled' })
      await scrollToConversationEntry(page, `[data-display-key="output/${messages[0].runId}/main/1"]`, 1)
      await page.screenshot({ path: testInfo.outputPath('persistent-main-text.png'), animations: 'disabled' })
      await expect(page.locator('.run-trace .trace-reasoning').first()).toHaveText('Private progress reasoning.')
      await expect(page.locator('.trace-reasoning button, .trace-reasoning .ui-thought-chain')).toHaveCount(0)
      await expect(page.locator('.run-trace [data-task-id="main"] .trace-agent-identity')).toHaveCount(0)
      await expect(page.locator('.run-trace')).not.toContainText('Saved progress text.')
      await page.screenshot({ path: testInfo.outputPath('main-process-expanded.png'), animations: 'disabled' })
    } finally { release() }
  })

  test('cancelled streaming body remains readable after refresh', async ({ page, vista }) => {
    await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
    await page.reload()
    const release = vista.pauseModelReplies(true)
    try {
      await page.locator('.sender-card textarea').fill('Cancel progress')
      await page.locator('.sender-card textarea').press('Enter')
      await expect(page.locator('.main-output')).toContainText('Progress 1.')
      await expect(page.locator('.trace-reasoning')).toContainText('Private progress reasoning.')
      const id = new URL(page.url()).pathname.split('/').at(-1)!
      await vista.api(`/sessions/${id}/cancel`, { method: 'POST' })
      await expect(page.locator('.live-response')).toHaveCount(0)
      await expect(page.locator('.main-output')).toContainText('Progress 1.')
      await page.reload()
      await expect(page.locator('.main-output')).toContainText('Progress 1.')
      await expect(page.locator('.trace-reasoning')).toContainText('Private progress reasoning.')
      await expect(page.locator('.main-output')).toHaveCount(1)
    } finally { release() }
  })

  test('failed run retains progress and actual tool steps without duplicating the error after refresh', async ({ page, vista }, testInfo) => {
    await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
    await page.reload()
    await page.locator('.sender-card textarea').fill('Fail progress')
    await page.locator('.sender-card textarea').press('Enter')
    await expect(page.locator('.main-output')).toContainText('Progress 1.')
    const id = new URL(page.url()).pathname.split('/').at(-1)!
    await expect.poll(async () => (await vista.api('/sessions')).find((s: any) => s.id === id)?.executing).toBe(false)
    for (const reload of [false, true]) {
      if (reload) await page.reload()
      await expect(page.locator('.main-output')).toHaveCount(1)
      await expect(page.locator('.main-output')).toContainText('Progress 1.')
      await expect(page.locator('.live-response')).toHaveCount(0)
      const error = page.locator('article.assistant .run-failure-message')
      await expect(error).toHaveCount(1)
      await expect(error).toContainText('Synthetic failure after saved progress')
      const trace = page.locator('.run-trace')
      await expect(trace).toHaveCount(1)
      await expect(trace).toHaveAttribute('data-run-status', 'failed')
      await expect(trace.locator('.trace-reasoning')).toHaveText('Private progress reasoning.')
      await expect(trace.locator('[data-step-key^="tool/"]')).toBeVisible()
      await expect(trace.locator('[data-step-key^="finish/"]')).toHaveCount(0)
      await expect(trace).not.toContainText('Synthetic failure after saved progress')
    }
    await page.screenshot({ path: testInfo.outputPath('failure-with-tool-steps.png'), animations: 'disabled' })
  })
})

historyTest.describe('long progress history', () => {
  historyTest('prepares complete long bodies in a continuous timeline with bounded mounted rows', async ({ context, vista, historySeed }, testInfo) => {
    test.setTimeout(90_000)
    await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
    const session = (await vista.api('/sessions')).find((item: { id: string }) => item.id === historySeed.sessionId)
    expect(session.executing).toBe(false)
    expect(vista.modelRequests).toHaveLength(0)
    const artifacts = await vista.api(`/sessions/${session.id}/artifacts`)
    expect(artifacts).toHaveLength(105)
    expect((await vista.api(`/sessions/${session.id}/artifacts/${artifacts[0].id}/output`)).content).toBe('Tool result')
    await testInfo.attach('native-history-seed', { body: JSON.stringify(historySeed), contentType: 'application/json' })
    // Each viewport restores an independent database and managed outputs from
    // the same real 105-tool run prepared once for this Playwright invocation.
    const page = await context.newPage()
    const indexes: string[] = []
    page.on('request', request => { if (request.url().includes('/main-outputs')) indexes.push(request.url()) })
    await page.goto(`${vista.origin}/sessions/${session.id}`)
    await expect(page.locator('.messages')).toHaveAttribute('aria-busy', 'false')
    expect(indexes.some(path => path.includes('?before='))).toBe(true)
    await expect(page.locator('.message-space')).toHaveClass(/virtualized/)
    expect(await page.locator('.message-row').count()).toBeLessThan(20)
    const first = page.locator('[data-display-key^="output/"][data-display-key$="/main/1"]')
    await scrollToConversationEntry(page, '[data-display-key^="output/"][data-display-key$="/main/1"]', 1)
    await expect(page.locator('.message-space')).toHaveAttribute('data-count', '107')
    await expect(first).toContainText('Long progress ending.')
    expect(Buffer.byteLength(await first.locator('.markdown').innerText())).toBeGreaterThan(8192)
    await first.scrollIntoViewIfNeeded()
    await page.screenshot({ path: testInfo.outputPath('long-main-text.png'), animations: 'disabled' })
    await page.locator('.messages').evaluate(el => { el.scrollTop = 0 })
    const steps = page.locator('.run-trace .trace-steps')
    await expect(steps).toHaveAttribute('data-step-count', '211')
    expect(await steps.locator('.trace-step-row').count()).toBeLessThanOrEqual(50)
    const reasoning = page.locator('[data-step-key="reasoning/main/1"] .trace-reasoning')
    await scrollToConversationEntry(page, '[data-step-key="reasoning/main/1"]', 2)
    await expect(reasoning).toContainText('Long reasoning ending.')
    expect(Buffer.byteLength(await reasoning.innerText())).toBeGreaterThan(8192)
    await expect(reasoning.locator('button, .ui-thought-chain')).toHaveCount(0)
    await page.screenshot({ path: testInfo.outputPath('long-reasoning-in-process.png'), animations: 'disabled' })
    await page.reload()
    await expect(page.locator('.messages')).toHaveAttribute('aria-busy', 'false')
    await scrollToConversationEntry(page, '[data-display-key^="output/"][data-display-key$="/main/1"]', 1)
    await expect(first).toContainText('Long progress ending.')
    expect(vista.modelRequests).toHaveLength(0)
  })
})
