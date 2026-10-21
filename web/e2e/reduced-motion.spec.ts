import { closeSettings, openSettingsSection } from './settings-navigation'
import type { Page } from '@playwright/test'
import { test, expect } from './fixtures'

test.use({ imageModel: true })

async function expectReducedMotion(page: Page) {
  await expect.poll(() => page.evaluate(() => [...document.querySelectorAll('*')].flatMap(element =>
    ['', '::before', '::after'].flatMap(pseudo => {
      const style = getComputedStyle(element, pseudo || null)
      const long = (value: string) => value.split(',').some(time => parseFloat(time) > 0.0011)
      const transition = style.transitionProperty !== 'none' && (long(style.transitionDuration) || long(style.transitionDelay))
      const animation = style.animationName !== 'none' && (long(style.animationDuration) || long(style.animationDelay)
        || style.animationIterationCount.split(',').some(count => count.trim() === 'infinite' || parseFloat(count) > 1))
      return transition || animation ? [{ element: element.tagName, class: element.getAttribute('class'), pseudo,
        transition: style.transition, animation: style.animation }] : []
    }))), { message: 'Controls, portals and pseudo-elements must respect reduced motion' }).toEqual([])
}

test('system changes update existing controls, dropdowns and both modal levels without reloading', async ({ page, vista }) => {
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  await page.goto(vista.origin + '/settings/models')
  const settings = page.locator('.settings-page')
  const connect = settings.getByRole('button', { name: 'Connect provider', exact: true })
  const duration = () => connect.evaluate(element => Math.max(...getComputedStyle(element).transitionDuration.split(',').map(parseFloat)))
  // Focus-visible removes outline transitions independently of motion settings.
  await connect.evaluate(element => (element as HTMLButtonElement).blur())
  await expect.poll(duration).toBeGreaterThan(0.001)
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await expectReducedMotion(page)
  await connect.click()
  const inner = page.locator('.provider-connection-modal')
  await expect(inner).toBeVisible()
  await inner.locator('.model-form label').first().locator('.ant-select').click()
  const dropdown = page.locator('.ant-select-dropdown:visible')
  await expect(dropdown).toBeVisible()
  await expectReducedMotion(page)
  await page.keyboard.press('Escape')
  await expect(dropdown).toHaveCount(0)
  await inner.getByRole('button', { name: 'Close', exact: true }).click()
  await expect(inner).toHaveCount(0)
  await closeSettings(page)
  await expect(page).toHaveURL(vista.origin + '/')
  await expect(page.locator('.workspace-modal')).toHaveCount(0)

  await page.emulateMedia({ reducedMotion: 'no-preference' })
  await page.locator('.settings-trigger').evaluate((button: HTMLButtonElement) => button.click())
  await openSettingsSection(page, 'models')
  await connect.evaluate(element => (element as HTMLButtonElement).blur())
  await expect.poll(duration).toBeGreaterThan(0.001)
})

test('reduced-motion image previews keep their end events and close repeatedly', async ({ page, vista }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
  const data = await page.evaluate(() => {
    const canvas = document.createElement('canvas')
    canvas.width = 480
    canvas.height = 320
    const context = canvas.getContext('2d')!
    context.fillStyle = '#2563eb'
    context.fillRect(0, 0, canvas.width, canvas.height)
    return canvas.toDataURL('image/png').split(',')[1]!
  })
  const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
  await vista.api(`/sessions/${session.id}/messages`, { method: 'POST', body: JSON.stringify({ content: 'Inspect the image.', images: [{ mediaType: 'image/png', data }] }) })
  await expect.poll(async () => (await vista.api(`/sessions/${session.id}/messages`)).some((message: { role: string }) => message.role === 'assistant')).toBe(true)
  await page.goto(`${vista.origin}/sessions/${session.id}`)
  const thumbnail = page.locator('.message-image-thumbnail img')
  await expect.poll(() => thumbnail.evaluate((image: HTMLImageElement) => image.complete && image.naturalWidth > 0)).toBe(true)
  for (const close of ['escape', 'button']) {
    await thumbnail.click()
    await expect(page.locator('body > .medium-zoom-image--opened')).toBeVisible()
    await expectReducedMotion(page)
    if (close === 'escape') await page.keyboard.press('Escape')
    else await page.locator('.message-image-preview-close').click()
    await expect(page.locator('.medium-zoom-overlay, body > .medium-zoom-image--opened')).toHaveCount(0)
    await expect(page.locator('.message-image-thumbnail')).toBeFocused()
  }
})

test.describe('conversation motion', () => {
  test.use({ progressCalls: 1 })

  test('loading, traces, quoting, jump scrolling and the mobile drawer respect reduced motion', async ({ page, vista, isMobile }) => {
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
    await page.reload()
    const release = vista.pauseModelReplies(true)
    try {
      const input = page.locator('.sender-card textarea')
      await input.fill('Scrollable paragraph.\n\n'.repeat(60))
      await input.press('Enter')
      await expect(page.locator('.main-output')).toContainText('Progress 1.')
      await expect(page.getByRole('button', { name: 'Stop current execution', exact: true })).toBeEnabled()
      await expectReducedMotion(page)
      release()
      await expect(page.locator('article.assistant').last()).toContainText('Final progress answer.')
      await page.locator('.trace-overview-heading').click()
      await expect(page.locator('.trace-reasoning').first()).toBeVisible()
      await expectReducedMotion(page)
      await page.locator('.trace-overview-heading').click()
      await expect(page.locator('.trace-reasoning:visible')).toHaveCount(0)

      const source = page.locator('article.user [data-quote-role] p').first()
      await source.scrollIntoViewIfNeeded()
      await source.evaluate(element => {
        const range = document.createRange()
        range.selectNodeContents(element)
        const selection = window.getSelection()!
        selection.removeAllRanges()
        selection.addRange(range)
        document.dispatchEvent(new Event('selectionchange'))
      })
      await expect(page.locator('.message-quote-action')).toBeVisible()
      await expectReducedMotion(page)
      await page.keyboard.press('Escape')
      await page.locator('.jump-latest').click()
      expect(await page.locator('.messages').evaluate(element => element.scrollHeight - element.clientHeight - element.scrollTop)).toBeLessThanOrEqual(3)

      if (isMobile) {
        await page.locator('.mobile-sessions-trigger:visible').tap()
        await expect(page.locator('.sessions-panel')).toHaveClass(/mobile-open/)
        await expectReducedMotion(page)
        await page.locator('.mobile-sessions-close:visible').tap()
        await expect(page.locator('.mobile-sessions-backdrop')).toHaveCount(0)
      }
    } finally { release() }
  })
})
