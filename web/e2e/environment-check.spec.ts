import { test, expect } from './fixtures'

test('checks on request, shows streamed steps and distinguishes an unverified result', { tag: '@responsive' }, async ({ page, vista }) => {
  await page.addInitScript(() => {
    const fetchOriginal = window.fetch.bind(window)
    let calls = 0
    const fixtureEvent = 'environment-check-fixture'
    window.fetch = async (input, init) => {
      const path = new URL(typeof input === 'string' ? input : input instanceof URL ? input : input.url, location.href).pathname
      if (path !== '/api/environment-check') return fetchOriginal(input, init)
      if (init?.method !== 'POST' || init.body != null || new Headers(init.headers).get('Accept') !== 'text/event-stream') {
        throw new Error('Expected one POST with a streamed response and no request body')
      }
      document.documentElement.dataset.environmentChecks = String(++calls)
      const encoder = new TextEncoder()
      let removeListener = () => {}
      const body = new ReadableStream<Uint8Array>({
        start(sink) {
          const send = (update: object) => {
            sink.enqueue(encoder.encode(`data: ${JSON.stringify(update)}\n\n`))
            if ('report' in update) {
              removeListener()
              sink.close()
            }
          }
          send({ step: 'address' })
          if (calls === 1 || calls === 4) {
            const onUpdate = (event: Event) => send((event as CustomEvent<object>).detail)
            window.addEventListener(fixtureEvent, onUpdate)
            removeListener = () => window.removeEventListener(fixtureEvent, onUpdate)
          } else if (calls === 2) {
            send({ step: 'port' })
            send({ step: 'callback' })
            send({ report: { results: [
              { family: 'IPv4', status: 'no_callback', attemptedPorts: [8443] },
              { family: 'IPv6', status: 'no_address' },
            ] } })
          } else {
            sink.close() // Disconnect without a report.
          }
        },
        cancel() {
          removeListener()
          document.documentElement.dataset.environmentCheckCancelled = String(init?.signal?.aborted)
        },
      })
      return new Response(body, { headers: { 'Content-Type': 'text/event-stream' } })
    }
  })
  await page.goto(`${vista.origin}/settings/about`)
  const actionName = 'Check coordinator service readiness'
  const button = page.locator('.settings-page .workspace-modal-header .ui-modal-header-actions').getByRole('button', { name: actionName })
  const root = page.locator('html')
  await expect(button).toBeVisible()
  await expect(button.locator('.tabler-icon-thinking-high')).toBeVisible()
  await expect(page.getByRole('button', { name: actionName })).toHaveCount(1)
  await expect(page.getByText('Checks only the first available common TCP port', { exact: false })).toHaveCount(0)
  await expect(root).not.toHaveAttribute('data-environment-checks')
  const update = (frame: object) => page.evaluate(detail => window.dispatchEvent(new CustomEvent('environment-check-fixture', { detail })), frame)
  await button.click()
  const loading = page.getByRole('dialog', { name: 'Check coordinator service readiness' })
  const progress = loading.getByRole('progressbar')
  await expect(progress).toHaveAccessibleName('Step 1 of 3: Finding a public IP address')
  await expect(progress).toHaveAttribute('aria-valuemin', '0')
  await expect(progress).toHaveAttribute('aria-valuemax', '3')
  await expect(progress).toHaveAttribute('aria-valuenow', '0')
  await update({ step: 'port' })
  await expect(progress).toHaveAccessibleName('Step 2 of 3: Finding an available test port')
  await expect(progress).toHaveAttribute('aria-valuenow', '1')
  await update({ step: 'callback' })
  await expect(progress).toHaveAccessibleName('Step 3 of 3: Waiting for an incoming connection')
  await expect(progress).toHaveAttribute('aria-valuenow', '2')
  const cancel = loading.locator('.ui-modal-footer').getByRole('button', { name: 'Cancel' })
  await expect(cancel).toBeEnabled()
  const [barBounds, cancelBounds] = await Promise.all([progress.boundingBox(), cancel.boundingBox()])
  expect(barBounds && cancelBounds).toBeTruthy()
  expect(cancelBounds!.y - (barBounds!.y + barBounds!.height)).toBeGreaterThanOrEqual(16)
  await update({ report: { results: [
    { family: 'IPv4', status: 'no_listener' },
    { family: 'IPv6', status: 'verified', port: 8080, attemptedPorts: [8080] },
  ] } })
  const success = page.getByRole('dialog', { name: 'Can serve as the coordinator' })
  await expect(success).toBeVisible()
  await expect(success.getByText('IPv6 — Public access verified on TCP port 8080', { exact: true })).toBeVisible()
  await expect(success.getByRole('progressbar')).toHaveCount(0)
  await expect(success.getByText('Ports tested: 8080', { exact: true })).toHaveCount(0)
  await expect(success.getByText('This check does not verify Vista’s management port', { exact: false })).toHaveCount(0)
  await expect(success.getByText('The connecting device needs IPv6 support.')).toBeVisible()
  await success.getByRole('button', { name: 'Close', exact: true }).last().click()
  await expect(success).toBeHidden()
  await expect(button).toBeVisible()
  await button.click()
  const unconfirmed = page.getByRole('dialog', { name: 'Coordinator readiness not yet verified' })
  await expect(unconfirmed).toBeVisible()
  const ipv4 = unconfirmed.getByRole('listitem').filter({ hasText: 'IPv4 — No verified incoming request' })
  await expect(ipv4.getByText('Ports tested: 8443', { exact: true })).toBeVisible()
  await expect(unconfirmed.getByText('Skipped (could not bind)')).toHaveCount(0)
  await expect(ipv4.getByText(/3306|15888|15889/)).toHaveCount(0)
  const ipv6 = unconfirmed.getByRole('listitem').filter({ hasText: 'IPv6 — No candidate public address found' })
  await expect(ipv6.getByText('No ports tested', { exact: true })).toBeVisible()
  await expect(root).toHaveAttribute('data-environment-checks', '2')
  await unconfirmed.getByRole('button', { name: 'Close', exact: true }).last().click()

  await button.click()
  await expect(unconfirmed.getByRole('status')).toBeVisible()
  await expect(unconfirmed.getByRole('listitem')).toHaveCount(0)
  await expect(unconfirmed.getByRole('progressbar')).toHaveCount(0)
  await unconfirmed.getByRole('button', { name: 'Close', exact: true }).last().click()

  await button.click()
  await expect(progress).toHaveAccessibleName('Step 1 of 3: Finding a public IP address')
  await expect(progress).toHaveAttribute('aria-valuenow', '0')
  await loading.getByRole('button', { name: 'Cancel' }).click()
  await expect(loading).toBeHidden()
  await expect(root).toHaveAttribute('data-environment-check-cancelled', 'true')
  await expect(root).toHaveAttribute('data-environment-checks', '4')
})
