import type { Page } from '@playwright/test'
import { test, expect } from './fixtures'

test.use({ imageModel: true })

async function imageFiles(page: Page, count: number) {
  const images = await page.evaluate(count => Array.from({ length: count }, (_, index) => {
    const canvas = document.createElement('canvas')
    canvas.width = 480
    canvas.height = 320
    const context = canvas.getContext('2d')!
    context.fillStyle = ['#567ace', '#31a98b', '#b978a8', '#cf9440'][index % 4]!
    context.fillRect(0, 0, canvas.width, canvas.height)
    context.fillStyle = 'white'
    context.font = '48px sans-serif'
    context.fillText(`Image ${index + 1}`, 120, 175)
    return canvas.toDataURL('image/png').split(',')[1]!
  }), count)
  return images.map((data, index) => ({ name: `sample-${index + 1}.png`, mimeType: 'image/png', buffer: Buffer.from(data, 'base64') }))
}

async function expectNoPageOverflow(page: Page) {
  const geometry = await page.evaluate(() => ({
    width: innerWidth, height: innerHeight,
    pageWidth: document.documentElement.scrollWidth,
    pageHeight: document.documentElement.scrollHeight,
  }))
  expect(geometry.pageWidth).toBeLessThanOrEqual(geometry.width + 2)
  expect(geometry.pageHeight).toBeLessThanOrEqual(geometry.height + 2)
}

test('multiple images send once with empty-text Enter and render automatically with a zoom preview', async ({ page, vista }, testInfo) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
  await page.reload()
  const files = await imageFiles(page, 4)
  const sender = page.locator('.sender-card')
  const textarea = sender.locator('textarea')
  const messagePosts: unknown[] = []
  let cancellationRequests = 0
  const release = vista.pauseModelReplies()
  page.on('request', request => {
    if (request.method() === 'POST' && /\/api\/sessions\/[^/]+\/messages$/.test(new URL(request.url()).pathname)) messagePosts.push(request.postDataJSON())
    if (request.method() === 'POST' && /\/api\/sessions\/[^/]+\/cancel$/.test(new URL(request.url()).pathname)) cancellationRequests++
  })

  // Select three together, then paste another; neither path should cap the draft at two.
  await sender.locator('input[type=file]').setInputFiles(files.slice(0, 3))
  await expect(sender.locator('.sender-attachment')).toHaveCount(3)
  await textarea.evaluate((element, image) => {
    const transfer = new DataTransfer()
    const bytes = Uint8Array.from(atob(image.data), character => character.charCodeAt(0))
    transfer.items.add(new File([bytes], image.name, { type: 'image/png' }))
    element.dispatchEvent(new ClipboardEvent('paste', { bubbles: true, cancelable: true, clipboardData: transfer }))
  }, { name: files[3]!.name, data: files[3]!.buffer.toString('base64') })
  await expect(sender.locator('.sender-attachment')).toHaveCount(4)
  await expect(sender.getByRole('button', { name: 'Send', exact: true })).toBeEnabled()

  await textarea.press('Shift+Enter')
  await expect(textarea).toHaveValue('\n')
  await textarea.fill('')
  await textarea.dispatchEvent('keydown', { key: 'Enter', code: 'Enter', isComposing: true })
  await textarea.dispatchEvent('keydown', { key: 'Enter', code: 'Enter', keyCode: 229 })
  await textarea.dispatchEvent('compositionstart')
  await textarea.press('Enter')
  await textarea.dispatchEvent('compositionend')
  expect(messagePosts).toHaveLength(0)
  await textarea.fill('')
  await page.waitForTimeout(220)
  await textarea.press('Enter')

  await expect(sender.locator('.sender-attachment')).toHaveCount(0)
  expect(messagePosts).toHaveLength(1)
  expect(messagePosts[0]).toMatchObject({ content: '', images: files.map(() => ({ mediaType: 'image/png' })) })
  await expect.poll(() => vista.modelRequests.length).toBe(1)
  const userInput = vista.modelRequests[0]!.messages!.find(message => message.role === 'user')!
  expect(Array.isArray(userInput.content)).toBe(true)
  const images = (userInput.content as { type: string; image_url?: { url: string } }[]).filter(part => part.type === 'image_url')
  expect(images).toHaveLength(4)
  expect(images.map(image => image.image_url!.url)).toEqual(files.map(file => `data:image/png;base64,${file.buffer.toString('base64')}`))

  const thumbnails = page.locator('article.user .message-images img')
  await expect(thumbnails).toHaveCount(4)
  await expect.poll(() => thumbnails.evaluateAll(items => items.every(item => (item as HTMLImageElement).complete && (item as HTMLImageElement).naturalWidth > 0))).toBe(true)
  const thumbnailWidths = await thumbnails.evaluateAll(items => items.map(image => ({
    image: image.getBoundingClientRect().width,
    button: image.closest('button')!.getBoundingClientRect().width,
  })))
  for (const widths of thumbnailWidths) expect(Math.abs(widths.button - widths.image)).toBeLessThanOrEqual(2)
  await expect(page.getByRole('button', { name: /^View image/ })).toHaveCount(0)
  await thumbnails.first().click()
  await expect(page.locator('.medium-zoom-overlay')).toBeVisible()
  await expect(page.locator('body > .medium-zoom-image--opened')).toBeVisible()
  await expectNoPageOverflow(page)
  await page.screenshot({ path: testInfo.outputPath('image-preview.png'), animations: 'disabled' })
  await page.keyboard.press('Escape')
  await expect(page.locator('.medium-zoom-overlay')).toHaveCount(0)
  expect(cancellationRequests).toBe(0)
  await thumbnails.nth(1).click()
  await expect(page.locator('.medium-zoom-overlay')).toBeVisible()
  await page.locator('.medium-zoom-overlay').click({ position: { x: 8, y: 8 } })
  await expect(page.locator('.medium-zoom-overlay')).toHaveCount(0)
  release()
  await expect(page.locator('article.assistant')).toContainText('Mock reply: Hi')

  await page.reload()
  await expect(thumbnails).toHaveCount(4)
  await expect.poll(() => thumbnails.evaluateAll(items => items.every(item => (item as HTMLImageElement).complete && (item as HTMLImageElement).naturalWidth > 0))).toBe(true)
  expect(vista.modelRequests).toHaveLength(1)
  await expectNoPageOverflow(page)
  await page.screenshot({ path: testInfo.outputPath('image-conversation-refreshed.png'), animations: 'disabled' })

  await textarea.fill('Continue without rereading the images')
  await textarea.press('Enter')
  await expect.poll(() => vista.modelRequests.length).toBe(2)
  const history = vista.modelRequests[1]!.messages!
  expect(history.every(message => typeof message.content === 'string')).toBe(true)
  expect(JSON.stringify(history)).toContain('use read_resource')
  await expect(thumbnails).toHaveCount(4)
  await expect(page.locator('article.assistant')).toHaveCount(2)
  await expect(page.locator('article.assistant').last()).toContainText('Mock reply: Hi')
})

test('queued image-only input restores all attachments to the draft', async ({ page, vista }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop', 'The same queue API is shared across layouts; the image workflow above covers all viewports.')
  const release = vista.pauseModelReplies()
  try {
    const sender = page.locator('.sender-card')
    await sender.locator('textarea').fill('Keep this request running while I add images')
    await sender.locator('textarea').press('Enter')
    await expect.poll(() => vista.modelRequests.length).toBe(1)
    const files = await imageFiles(page, 3)
    await sender.locator('input[type=file]').setInputFiles(files)
    await expect(sender.locator('.sender-attachment')).toHaveCount(3)
    await sender.getByRole('button', { name: 'Queue', exact: true }).click()
    await expect(page.locator('.pending-input')).toHaveCount(1)
    await expect(page.locator('.pending-input')).toContainText('3 image(s)')
    await expect(sender.locator('.sender-attachment')).toHaveCount(0)
    // A fourth draft image must coexist with the three restored from the queue.
    await sender.locator('input[type=file]').setInputFiles(files.slice(0, 1))
    await page.getByRole('button', { name: 'Move back to draft', exact: true }).click()
    await expect(page.locator('.pending-input')).toHaveCount(0)
    await expect(sender.locator('.sender-attachment')).toHaveCount(4)
    release()
    await expect(page.locator('article.assistant')).toContainText('Mock reply: Hi')
    expect(vista.modelRequests).toHaveLength(1)
  } finally {
    release()
  }
})
