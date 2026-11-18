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
  // separately for presence, history bounds and the visible sequence range.
  const inspect = () => scroller.evaluate((element, selector) => {
    const box = element.querySelector(selector)?.getBoundingClientRect(), bounds = element.getBoundingClientRect()
    const span = Math.max(1, element.scrollHeight - element.clientHeight)
    const visible = !!box && box.bottom > bounds.top && box.top < bounds.bottom
    return { visible, position: box && !visible ? Math.min(1, Math.max(0,
      (element.scrollTop + box.top - bounds.top) / span)) : undefined,
      fraction: element.scrollTop / span,
      start: Number((element as HTMLElement).dataset.historyStart),
      count: Number(element.querySelector('.message-space')?.getAttribute('data-row-count')),
      // The frozen reader and the prepared destination can both be mounted.
      // Only rows intersecting the viewport can tell us where we landed.
      sequences: [...element.querySelectorAll<HTMLElement>('[data-timeline-sequence]')].flatMap(row => {
        const rect = row.getBoundingClientRect()
        return rect.bottom > bounds.top && rect.top < bounds.bottom ? [Number(row.dataset.timelineSequence)] : []
      }) }
  }, selector)
  let state = await inspect()
  let low = 0, high = 1, fraction = from === 'end' || sequence === Infinity ? 1 : 0, previousStart = -1
  for (let attempt = 0; attempt < 80; attempt++) {
    if (state.visible) break
    const { start, count } = state
    if (start !== previousStart) { low = 0; high = 1; previousStart = start }
    const older = state.position === undefined && sequence < start
    if (state.position !== undefined) fraction = state.position
    else if (older) fraction = 0
    await scrollConversationTo(page, fraction)
    if (older) {
      // Loading older history already includes the input-idle and preparation
      // work; wait for that result rather than adding a fixed delay first.
      await expect.poll(async () => {
        state = await inspect()
        return state.visible || state.position !== undefined || state.start < start
      }, { timeout: 20_000, message: `Entry ${selector} appears or older history advances` }).toBe(true)
      continue
    }
    // Native wheel ownership lasts 180 ms, including gestures at an edge.
    await page.waitForTimeout(180)
    await expect(scroller).toHaveAttribute('aria-busy', 'false')
    state = await inspect()
    if (state.visible) break
    if (count !== state.count) { low = 0; high = 1 }
    const { sequences } = state
    if (!sequences.length) continue
    if (Math.max(...sequences) < sequence) low = Math.max(low, state.fraction)
    else high = Math.min(high, state.fraction)
    if (high - low < .0001) {
      // Late measurement can move a key past an earlier fractional bound.
      low = 0; high = 1
    }
    fraction = (low + high) / 2
  }
  await expect(target, `Conversation entry ${selector} remains reachable`).toBeInViewport()
  return target
}
