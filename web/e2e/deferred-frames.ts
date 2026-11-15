import type { Locator } from '@playwright/test'

// Hold frames requested by a click handler without pausing Playwright's own
// actionability frames. Tests can finish later input before opening work runs.
export async function deferClickFrames(control: Locator) {
  await control.evaluate(element => {
    const request = window.requestAnimationFrame.bind(window)
    const cancel = window.cancelAnimationFrame.bind(window)
    const pending = new Map<number, FrameRequestCallback>()
    let clicking = false
    element.addEventListener('click', () => { clicking = true }, true)
    element.addEventListener('click', () => { clicking = false })
    window.requestAnimationFrame = callback => {
      if (!clicking) return request(callback)
      const id = request(() => {})
      pending.set(id, callback)
      return id
    }
    window.cancelAnimationFrame = id => { pending.delete(id); cancel(id) }
    Object.assign(element, {
      releaseClickFrames() {
        window.requestAnimationFrame = request
        window.cancelAnimationFrame = cancel
        const callbacks = [...pending.values()]
        pending.clear()
        callbacks.forEach(callback => callback(performance.now()))
        return callbacks.length
      },
    })
  })
  return () => control.evaluate(element => Reflect.get(element, 'releaseClickFrames')())
}
