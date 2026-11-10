import type { Page } from '@playwright/test'

// Wait for observable layout work, rather than assuming it always takes 350 ms.
// Infinite progress animations must not hold up geometry checks.
export async function settleLayout(page: Page) {
  await page.evaluate(async () => {
    const frame = () => new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
    await document.fonts.ready
    await frame(); await frame()
    await Promise.all(document.getAnimations().filter(animation => animation.playState === 'running'
      && animation.effect?.getTiming().iterations !== Infinity).map(animation => animation.finished.catch(() => {})))
    await frame(); await frame()
  })
}
