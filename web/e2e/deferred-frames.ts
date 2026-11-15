import type { Locator } from '@playwright/test'

// Hold frames requested during native dispatch without pausing Playwright's
// actionability frames. Pointer tracking runs on document capture, so observe
// it from window before the library receives the event.
async function deferEventFrames(control: Locator, type: 'click' | 'pointerdown') {
  await control.evaluate((element, type) => {
    const request = window.requestAnimationFrame.bind(window)
    const cancel = window.cancelAnimationFrame.bind(window)
    const pending = new Map<number, FrameRequestCallback>()
    let dispatching = false
    const target = type === 'pointerdown' ? window : element
    const start = () => { dispatching = true }
    const end = () => { dispatching = false }
    target.addEventListener(type, start, true)
    target.addEventListener(type, end)
    window.requestAnimationFrame = callback => {
      if (!dispatching) return request(callback)
      const id = request(() => {})
      pending.set(id, callback)
      return id
    }
    window.cancelAnimationFrame = id => { pending.delete(id); cancel(id) }
    Object.assign(element, {
      releaseEventFrames() {
        window.requestAnimationFrame = request
        window.cancelAnimationFrame = cancel
        target.removeEventListener(type, start, true)
        target.removeEventListener(type, end)
        const callbacks = [...pending.values()]
        pending.clear()
        callbacks.forEach(callback => callback(performance.now()))
        return callbacks.length
      },
    })
  }, type)
  return () => control.evaluate(element => Reflect.get(element, 'releaseEventFrames')())
}

export const deferClickFrames = (control: Locator) => deferEventFrames(control, 'click')
export const deferPointerDownFrames = (control: Locator) => deferEventFrames(control, 'pointerdown')
