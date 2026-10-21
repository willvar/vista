import { test, expect } from './fixtures'

test.use({ replyWithPrompt: true, followUpTool: true })

test('a follow-up preserves the preceding streamed answer in conversation order', async ({ page, vista }, testInfo) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
  await page.reload()
  const sender = page.locator('.sender-card textarea')
  const release = vista.pauseModelReplies(true)
  try {
    await sender.fill('First question')
    await sender.press('Enter')
    await expect(page.locator('.live-response')).toContainText('Mock reply: First question')
    const firstProcessHeading = page.locator('.message-row.user').first().locator('.trace-overview-heading')
    await expect(firstProcessHeading).toHaveAttribute('aria-expanded', 'false')
    await sender.fill('Follow-up question')
    await sender.press('Enter')
    await expect(page.locator('.pending-input')).toContainText('Follow-up question')
    release()

    const replies = page.locator('article.assistant')
    await expect(replies).toHaveCount(2)
    await expect(page.locator('.live-response')).toHaveCount(0)
    await expect(page.locator('.message-row article .markdown')).toHaveText([
      'First question', 'Mock reply: First question', 'Follow-up question', 'Mock reply: Follow-up question',
    ])
    const sessionId = new URL(page.url()).pathname.split('/').at(-1)!
    const messages = await vista.api(`/sessions/${sessionId}/messages`)
    expect(new Set(messages.map((message: any) => message.runId)).size).toBe(1)
    expect(messages.map((message: any) => message.role)).toEqual(['user', 'assistant', 'user', 'assistant'])
    expect(messages.filter((message: any) => message.role === 'assistant').map((message: any) => message.modelResults.map((result: any) => result.turn))).toEqual([[1], [2, 3]])
    expect(messages[3].resultsAfter).toBe(messages[1].sequence)
    expect(vista.modelRequests[1]!.messages!.filter(message => message.role === 'assistant').map(message => message.content)).toEqual(['Mock reply: First question'])
    await expect(firstProcessHeading).toHaveAttribute('aria-expanded', 'false')

    const assertReplyTraces = async () => {
      const rows = page.locator('.message-row.user')
      const firstTrace = rows.nth(0).locator('.run-trace')
      const followUpTrace = rows.nth(1).locator('.run-trace')
      await expect(firstTrace).toHaveCount(1)
      const firstHeading = firstTrace.locator('.trace-overview-heading')
      if (await firstHeading.getAttribute('aria-expanded') !== 'true') await firstHeading.click()
      await expect(firstTrace.locator('.trace-reasoning')).toHaveText('Checking the first question')
      await expect(firstTrace.locator('[data-step-key^="tool/"]')).toHaveCount(0)
      await expect(followUpTrace).toHaveCount(1)
      await expect(followUpTrace).toHaveAttribute('data-after-sequence', String(messages[1].sequence))
      const heading = followUpTrace.locator('.trace-overview-heading')
      if (await heading.getAttribute('aria-expanded') !== 'true') await heading.click()
      await expect(followUpTrace).toContainText("printf 'price lookup result'")
      await expect(followUpTrace.locator('.trace-overview-heading')).toHaveAttribute('aria-expanded', 'true')
      await expect(followUpTrace.locator('[data-step-key^="tool/"]')).toBeVisible()
      await expect(followUpTrace.locator('[data-step-key^="tool/"]')).not.toContainText('turn 2')
      await expect(followUpTrace.locator('[data-step-key^="reasoning/"]')).toHaveCount(0)
      await expect(followUpTrace).not.toContainText('Mock reply: Follow-up question')
    }
    await assertReplyTraces()

    await page.reload()
    await expect(replies).toHaveCount(2)
    await expect(replies.nth(0)).toContainText('Mock reply: First question')
    await expect(replies.nth(1)).toContainText('Mock reply: Follow-up question')
    await assertReplyTraces()
    await page.screenshot({ path: testInfo.outputPath('follow-up-replies.png'), animations: 'disabled' })

    // A new run must receive both answers once, including the preserved stage.
    await sender.fill('Third question')
    await sender.press('Enter')
    await expect(replies).toHaveCount(3)
    const resumed = vista.modelRequests[3]!.messages!
    expect(resumed.filter(message => message.role === 'assistant' && message.content).map(message => message.content)).toEqual([
      'Mock reply: First question', 'Mock reply: Follow-up question',
    ])
    const calls = resumed.filter(message => message.tool_calls?.length).flatMap(message => message.tool_calls!)
    const results = resumed.filter(message => message.role === 'tool')
    expect(calls).toHaveLength(1)
    expect(calls[0]!.function.name).toBe('shell')
    expect(calls[0]!.function.arguments).toContain('price lookup result')
    expect(results).toHaveLength(1)
    expect(results[0]!.tool_call_id).toBe(calls[0]!.id)
    expect(results[0]!.content).toContain('price lookup result')
  } finally {
    release()
  }
})
