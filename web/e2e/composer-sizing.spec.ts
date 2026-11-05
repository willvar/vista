import type { Locator } from '@playwright/test'
import { test, expect } from './fixtures'

async function resizeDraft(textarea: Locator, value: string) {
  return textarea.evaluate(async (element, value) => {
    const input = element as HTMLTextAreaElement
    const card = input.closest('.sender-card')!
    const before = input.getBoundingClientRect().height
    const cardBefore = card.getBoundingClientRect().height
    input.value = value
    input.dispatchEvent(new InputEvent('input', { bubbles: true, inputType: 'insertText' }))
    await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
    const transition = input.getAnimations().find(animation => animation instanceof CSSTransition && animation.transitionProperty === 'height')
    if (transition) {
      transition.pause()
      transition.currentTime = Number(transition.effect!.getTiming().duration) / 2
    }
    const middle = input.getBoundingClientRect().height
    const cardMiddle = card.getBoundingClientRect().height
    transition?.finish()
    await Promise.all(input.getAnimations().map(animation => animation.finished.catch(() => {})))
    return { before, middle, after: input.getBoundingClientRect().height, animated: !!transition, cardBefore, cardMiddle }
  }, value)
}

for (const reducedMotion of ['no-preference', 'reduce'] as const) {
  test(`composer grows and shrinks with ${reducedMotion} motion`, async ({ page, vista }) => {
    await page.emulateMedia({ reducedMotion })
    await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
    await page.reload()
    for (const location of ['homepage', 'conversation']) {
      if (location === 'conversation') {
        const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
        await page.goto(`${vista.origin}/sessions/${session.id}`)
      }
      const textarea = page.locator('.sender-card textarea')
      await expect(textarea).toBeEnabled()
      await textarea.evaluate(element => (element as HTMLTextAreaElement).blur())
      await page.locator('.sender-card .ui-sender-content').click({ position: { x: 24, y: 2 } })
      await expect(textarea).toBeFocused()
      await expect.poll(() => textarea.evaluate(element => element.style.height)).not.toBe('')
      const collapsed = (await textarea.boundingBox())!.height
      for (const value of ['Draft line\n'.repeat(10), 'One line', 'Draft line\n'.repeat(40), '']) {
        const size = await resizeDraft(textarea, value)
        expect(size.animated).toBe(reducedMotion === 'no-preference')
        if (reducedMotion === 'no-preference') {
          expect(size.middle).toBeGreaterThan(Math.min(size.before, size.after))
          expect(size.middle).toBeLessThan(Math.max(size.before, size.after))
          expect(Math.abs((size.cardMiddle - size.cardBefore) - (size.middle - size.before))).toBeLessThan(1)
        }
        if (value.includes('\n')) {
          const limit = await textarea.evaluate(element => {
            const style = getComputedStyle(element)
            return parseFloat(style.lineHeight) * (innerHeight <= 520 ? 4 : 8)
              + parseFloat(style.paddingTop) + parseFloat(style.paddingBottom)
              + parseFloat(style.borderTopWidth) + parseFloat(style.borderBottomWidth)
          })
          expect(Math.abs(size.after - limit)).toBeLessThan(1)
        } else expect(Math.abs(size.after - collapsed)).toBeLessThan(1)
      }
      if (location === 'conversation') {
        await resizeDraft(textarea, 'Send this\n'.repeat(4))
        await textarea.press('Enter')
        await expect(page.locator('article.assistant')).toBeVisible()
        await expect(textarea).toHaveValue('')
        await expect.poll(async () => Math.abs((await textarea.boundingBox())!.height - collapsed)).toBeLessThan(1)
      }
    }
  })
}
