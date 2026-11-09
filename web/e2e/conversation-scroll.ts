import { expect, type Page } from '@playwright/test'

// Seek through the same native scroll range a reader uses. Do not expose a
// test-only product API or require virtualized rows to remain in the DOM.
export async function scrollToConversationEntry(page: Page, selector: string, sequence: number) {
  const scroller = page.locator('.messages'), target = scroller.locator(selector)
  let low = 0, high = 1, fraction = sequence === Infinity ? 1 : 0, previousStart = -1
  await scroller.dispatchEvent('wheel', { deltaY: -1 })
  for (let attempt = 0; attempt < 80; attempt++) {
    if (await target.count()) break
    const start = Number(await scroller.getAttribute('data-history-start'))
    if (start !== previousStart) { low = 0; high = 1; previousStart = start }
    if (sequence < start) fraction = 0
    await scroller.evaluate((element, fraction) => {
      element.scrollTop = (element.scrollHeight - element.clientHeight) * fraction
      element.dispatchEvent(new Event('scroll'))
    }, fraction)
    await page.waitForTimeout(180)
    if (await target.count()) break
    if (sequence < start) {
      await expect.poll(async () => Number(await scroller.getAttribute('data-history-start')), { timeout: 20_000 }).toBeLessThan(start)
      continue
    }
    const mounted = await scroller.locator('[data-timeline-sequence]').evaluateAll(rows => rows.map(row => Number((row as HTMLElement).dataset.timelineSequence)))
    if (!mounted.length) continue
    if (Math.max(...mounted) < sequence) low = fraction
    else high = fraction
    fraction = (low + high) / 2
  }
  await expect(target, `Conversation entry ${selector} remains reachable`).toHaveCount(1)
  await target.scrollIntoViewIfNeeded()
  await page.waitForTimeout(180)
  await expect(target).toBeVisible()
  return target
}
