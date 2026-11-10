import { expect, type Page } from '@playwright/test'

export async function scrollConversationTo(page: Page, fraction: number) {
  const scroller = page.locator('.messages')
  await expect(scroller).toHaveAttribute('aria-busy', 'false')
  await scroller.evaluate((element, value) => {
    // Input and its movement belong to the same gesture. Separate round trips
    // can let the input-idle timer expire before the scroll reaches the page.
    element.dispatchEvent(new WheelEvent('wheel', { deltaY: -1, bubbles: true, cancelable: true }))
    element.scrollTop = (element.scrollHeight - element.clientHeight) * value
    element.dispatchEvent(new Event('scroll'))
  }, fraction)
}

// Seek through the same native scroll range a reader uses. Do not expose a
// test-only product API or require virtualized rows to remain in the DOM.
export async function scrollToConversationEntry(page: Page, selector: string, sequence: number, from: 'start' | 'end' = 'start') {
  const scroller = page.locator('.messages'), target = scroller.locator(selector)
  // Read one consistent DOM snapshot instead of crossing the browser boundary
  // separately for presence, history bounds and the mounted sequence range.
  const inspect = () => scroller.evaluate((element, selector) => ({
    found: !!element.querySelector(selector),
    start: Number((element as HTMLElement).dataset.historyStart),
    mounted: [...element.querySelectorAll<HTMLElement>('[data-timeline-sequence]')].map(row => Number(row.dataset.timelineSequence)),
  }), selector)
  let state = await inspect()
  let low = 0, high = 1, fraction = from === 'end' || sequence === Infinity ? 1 : 0, previousStart = -1
  for (let attempt = 0; attempt < 80; attempt++) {
    if (state.found) break
    const { start } = state
    if (start !== previousStart) { low = 0; high = 1; previousStart = start }
    if (sequence < start) fraction = 0
    await scrollConversationTo(page, fraction)
    if (sequence < start) {
      // Loading older history already includes the input-idle and preparation
      // work; wait for that result rather than adding a fixed delay first.
      await expect.poll(async () => {
        state = await inspect()
        return state.found || state.start < start
      }, { timeout: 20_000, message: `Entry ${selector} appears or older history advances` }).toBe(true)
      continue
    }
    // Native wheel ownership lasts 180 ms, including gestures at an edge.
    await page.waitForTimeout(180)
    state = await inspect()
    if (state.found) break
    const { mounted } = state
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
