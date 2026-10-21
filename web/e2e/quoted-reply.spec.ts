import type { Locator, Page } from '@playwright/test'
import { test, expect, type VistaFixture } from './fixtures'
import type { Message, QueuedInput } from '../src/client/types'

test.use({ replyWithPrompt: true })
const passage = 'A quoted passage with café and 中文🙂.'
const secondPassage = 'A separate paragraph can replace the quote.'

async function createConversation(page: Page, vista: VistaFixture, content = `${passage}\n\n${secondPassage}`) {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
  const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
  await vista.api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content, modelKey: 'test' }) })
  await expect.poll(async () => (await vista.api(`/sessions/${session.id}/messages`) as Message[]).filter(message => message.role === 'assistant').length).toBe(1)
  await page.goto(`${vista.origin}/sessions/${session.id}`)
  await expect(page.locator('article.assistant [data-quote-role]')).toContainText(content.split('\n')[0]!)
  return session
}

async function selectText(source: Locator, text?: string) {
  await source.scrollIntoViewIfNeeded()
  return source.evaluate((element, text) => {
    const range = document.createRange()
    if (text) {
      const walker = document.createTreeWalker(element, NodeFilter.SHOW_TEXT)
      let node: Node | null
      while ((node = walker.nextNode())) {
        const offset = node.textContent?.indexOf(text) ?? -1
        if (offset < 0) continue
        range.setStart(node, offset)
        range.setEnd(node, offset + text.length)
        break
      }
      if (range.collapsed) throw new Error('Quoted passage was not found')
    } else range.selectNodeContents(element)
    const selection = window.getSelection()!
    selection.removeAllRanges()
    selection.addRange(range)
    document.dispatchEvent(new Event('selectionchange'))
    return selection.toString()
  }, text)
}

async function quoteSelected(page: Page, touch: boolean) {
  const action = page.getByRole('button', { name: 'Quote selection', exact: true })
  await expect(action).toBeVisible()
  const bounds = (await action.boundingBox())!
  const viewport = page.viewportSize()!
  expect(bounds.x).toBeGreaterThanOrEqual(0)
  expect(bounds.x + bounds.width).toBeLessThanOrEqual(viewport.width)
  expect(bounds.y).toBeGreaterThanOrEqual(0)
  expect(bounds.y + bounds.height).toBeLessThanOrEqual(viewport.height)
  if (touch) await action.tap()
  else await action.click()
  await expect(page.getByRole('group', { name: 'Quoted text', exact: true })).toBeVisible()
  await expect(page.locator('.sender-card textarea')).toBeFocused()
}

test('selected text reaches the model with the question and remains quoted after refresh', async ({ page, vista, isMobile }, testInfo) => {
  const session = await createConversation(page, vista)
  const input = page.locator('.sender-card textarea')
  await input.fill('Explain this passage.')
  await selectText(page.locator('article.assistant [data-quote-role]'), passage)
  await expect(page.locator('.message-quote-action')).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath('selection-action.png'), animations: 'disabled' })
  await quoteSelected(page, isMobile)
  await expect(input).toHaveValue('Explain this passage.')
  await expect(page.locator('.sender-quote p')).toHaveText(passage)
  await page.screenshot({ path: testInfo.outputPath('quoted-draft-light.png'), animations: 'disabled' })
  await page.emulateMedia({ colorScheme: 'dark' })
  await page.screenshot({ path: testInfo.outputPath('quoted-draft-dark.png'), animations: 'disabled' })
  await page.getByRole('button', { name: 'Send', exact: true }).click()
  const expected = `> ${passage}\n\nExplain this passage.`
  await expect.poll(() => vista.modelRequests.at(-1)?.messages?.filter(message => message.role === 'user').at(-1)?.content).toBe(expected)
  await expect(page.locator('.sender-quote')).toHaveCount(0)
  await expect(input).toHaveValue('')
  await expect.poll(async () => (await vista.api(`/sessions/${session.id}/messages`) as Message[]).filter(message => message.role === 'assistant').length).toBe(2)
  await page.reload()
  const message = page.locator('article.user').last()
  await expect(message.locator('blockquote')).toHaveText(passage)
  await expect(message).toContainText('Explain this passage.')
  await expect(message).not.toContainText(secondPassage)
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(page.viewportSize()!.width)
})

test('quotes replace, persist with their own session draft, and can be removed independently', async ({ page, vista, isMobile }) => {
  const first = await createConversation(page, vista)
  const input = page.locator('.sender-card textarea')
  await input.fill('Keep my existing question.')
  const source = page.locator('article.assistant [data-quote-role]')
  await selectText(source, passage)
  await quoteSelected(page, isMobile)
  await selectText(source, secondPassage)
  await quoteSelected(page, isMobile)
  await expect(page.locator('.sender-quote p')).toHaveText(secondPassage)
  await page.reload()
  await expect(page.locator('.sender-quote p')).toHaveText(secondPassage)
  await expect(input).toHaveValue('Keep my existing question.')
  const second = await vista.api('/sessions', { method: 'POST', body: '{}' })
  await page.goto(`${vista.origin}/sessions/${second.id}`)
  await expect(page.locator('.sender-quote')).toHaveCount(0)
  await input.fill('A different question.')
  await page.goto(`${vista.origin}/sessions/${first.id}`)
  await expect(page.locator('.sender-quote p')).toHaveText(secondPassage)
  await expect(input).toHaveValue('Keep my existing question.')
  await page.getByRole('button', { name: 'Remove quote', exact: true }).click()
  await expect(page.locator('.sender-quote')).toHaveCount(0)
  await expect(input).toBeFocused()
  await expect(input).toHaveValue('Keep my existing question.')
  await page.reload()
  await expect(page.locator('.sender-quote')).toHaveCount(0)
  await input.fill('')
  await selectText(page.locator('article.user [data-quote-role]'), passage)
  await quoteSelected(page, isMobile)
  await expect(page.locator('.sender-quote-label')).toHaveText('Quoted message')
  await expect(page.getByRole('button', { name: 'Send', exact: true })).toBeDisabled()
})

test('the quote action waits for a settled selection and stays dismissed after copying, scrolling, or Escape', async ({ page, vista, isMobile }) => {
  await createConversation(page, vista)
  const source = page.locator('article.assistant [data-quote-role]')
  const action = page.locator('.message-quote-action')
  await selectText(source, passage)
  await page.waitForTimeout(200)
  await expect(action).toHaveCount(0)
  await selectText(source, secondPassage)
  await page.waitForTimeout(200)
  await expect(action).toHaveCount(0)
  await expect(action).toBeVisible()

  await source.dispatchEvent('pointerdown', { pointerType: isMobile ? 'touch' : 'mouse', buttons: 1 })
  await selectText(source, passage)
  await page.waitForTimeout(500)
  await expect(action).toHaveCount(0)
  await source.dispatchEvent('pointerup', { pointerType: isMobile ? 'touch' : 'mouse', buttons: 0 })
  await expect(action).toHaveCount(0)
  await expect(action).toBeVisible()
  await page.evaluate(() => document.dispatchEvent(new Event('copy')))
  await source.dispatchEvent('pointerdown')
  await source.dispatchEvent('pointerup')
  await page.waitForTimeout(500)
  await expect(action).toHaveCount(0)
  expect(await page.evaluate(() => window.getSelection()?.toString())).toBe(passage)

  await selectText(source, secondPassage)
  await source.dispatchEvent(isMobile ? 'touchmove' : 'wheel')
  await page.locator('.messages').dispatchEvent('scroll')
  await page.waitForTimeout(500)
  await expect(action).toHaveCount(0)
  expect(await page.evaluate(() => window.getSelection()?.toString())).toBe(secondPassage)
  await selectText(source, passage)
  await page.keyboard.press('Escape')
  await page.waitForTimeout(500)
  await expect(action).toHaveCount(0)
  await expect(page.locator('.sender-quote')).toHaveCount(0)
})

test('selection stays within one message and Escape dismisses the action without sending', async ({ page, vista }) => {
  await createConversation(page, vista)
  await page.locator('.messages').evaluate(element => {
    const sources = element.querySelectorAll('[data-quote-role]')
    const range = document.createRange()
    range.setStart(sources[0]!.firstChild!.firstChild!, 0)
    range.setEnd(sources[1]!.firstChild!.firstChild!, 4)
    const selection = window.getSelection()!
    selection.removeAllRanges()
    selection.addRange(range)
    document.dispatchEvent(new Event('selectionchange'))
  })
  await expect(page.locator('.message-quote-action')).toHaveCount(0)
  await selectText(page.locator('article.assistant [data-quote-role]'), passage)
  await expect(page.locator('.message-quote-action')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.locator('.message-quote-action')).toHaveCount(0)
  await expect(page.locator('.sender-quote')).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Send', exact: true })).toBeDisabled()
  expect(vista.modelRequests).toHaveLength(1)
})

test('a quote can be queued during execution and reaches the next model request intact', async ({ page, vista, isMobile }) => {
  const session = await createConversation(page, vista)
  const release = vista.pauseModelReplies()
  try {
    const input = page.locator('.sender-card textarea')
    await input.fill('Keep working.')
    await input.press('Enter')
    await expect(page.getByRole('button', { name: 'Stop current execution', exact: true })).toBeEnabled()
    await selectText(page.locator('article.assistant [data-quote-role]'), passage)
    await quoteSelected(page, isMobile)
    await input.fill('Explain this after finishing.')
    await page.getByRole('button', { name: 'Queue', exact: true }).click()
    const expected = `> ${passage}\n\nExplain this after finishing.`
    await expect.poll(async () => (await vista.api(`/sessions/${session.id}/inputs`) as QueuedInput[]).find(item => item.content === expected)?.mode).toBe('later')
    await expect(page.locator('.sender-quote')).toHaveCount(0)
    release()
    await expect.poll(() => vista.modelRequests.at(-1)?.messages?.filter(message => message.role === 'user').at(-1)?.content).toBe(expected)
  } finally { release() }
})

test('long selections remain complete while the quote preview keeps the composer reachable', async ({ page, vista, isMobile }, testInfo) => {
  const content = 'Long quoted text café 中文🙂. '.repeat(160) + 'End of the full quote.'
  await createConversation(page, vista, content)
  const source = page.locator('article.assistant [data-quote-role]')
  await expect(source).toContainText('End of the full quote.')
  const selected = await selectText(source)
  expect(new TextEncoder().encode(selected).length).toBeGreaterThan(4096)
  await quoteSelected(page, isMobile)
  await expect(page.locator('.sender-quote p')).toHaveText(selected)
  const input = page.locator('.sender-card textarea')
  await input.fill('Explain the ending.')
  const send = page.getByRole('button', { name: 'Send', exact: true })
  await expect(send).toBeInViewport()
  await page.screenshot({ path: testInfo.outputPath('long-quote-preview.png'), animations: 'disabled' })
  await send.click()
  const expected = selected.split('\n').map(line => `> ${line}`).join('\n') + '\n\nExplain the ending.'
  await expect.poll(() => vista.modelRequests.at(-1)?.messages?.filter(message => message.role === 'user').at(-1)?.content).toBe(expected)
})

test('an oversized quoted message reports the existing input limit without losing or truncating the draft', async ({ page, vista, isMobile }) => {
  await createConversation(page, vista, 'Quoted text. '.repeat(630))
  const selected = await selectText(page.locator('article.assistant [data-quote-role]'))
  expect(new TextEncoder().encode(selected).length).toBeGreaterThan(8192)
  await quoteSelected(page, isMobile)
  const input = page.locator('.sender-card textarea')
  await input.fill('Explain this quote.')
  await page.getByRole('button', { name: 'Send', exact: true }).click()
  await expect(page.locator('.error')).toContainText('8192 bytes')
  await expect(page.locator('.sender-quote p')).toHaveText(selected)
  await expect(input).toHaveValue('Explain this quote.')
  await expect(page.getByRole('button', { name: 'Send', exact: true })).toBeEnabled()
  expect(vista.modelRequests).toHaveLength(1)
})

test('a real mouse drag can quote text without changing the existing question', async ({ page, vista, isMobile }) => {
  test.skip(isMobile, 'Mouse selection is exercised on the desktop project')
  await createConversation(page, vista)
  const input = page.locator('.sender-card textarea')
  await input.fill('Keep this question.')
  await selectText(page.locator('article.assistant [data-quote-role]'), passage)
  const bounds = await page.evaluate(() => {
    const selection = window.getSelection()!
    const rect = selection.getRangeAt(0).getBoundingClientRect()
    selection.removeAllRanges()
    return { left: rect.left, right: rect.right, top: rect.top, height: rect.height }
  })
  await page.mouse.move(bounds.left + 1, bounds.top + bounds.height / 2)
  await page.mouse.down()
  await page.mouse.move(bounds.right - 1, bounds.top + bounds.height / 2, { steps: 12 })
  await page.mouse.up()
  const selected = await page.evaluate(() => window.getSelection()?.toString() || '')
  expect(selected.length).toBeGreaterThan(10)
  expect(passage).toContain(selected)
  await quoteSelected(page, false)
  await expect(page.locator('.sender-quote p')).toHaveText(selected)
  await expect(input).toHaveValue('Keep this question.')
})
