import { test, expect } from './fixtures'

test('checks the current instance only on request and distinguishes an unverified result', { tag: '@responsive' }, async ({ page, vista }) => {
  let calls = 0
  await page.route('**/api/environment-check', async route => {
    calls++
    expect(route.request().method()).toBe('POST')
    expect(route.request().postData()).toBeNull()
    await route.fulfill({ json: {
      port: 5187,
      results: [{ family: 'IPv4', status: 'no_callback' }, { family: 'IPv6', status: calls === 1 ? 'verified' : 'probe_unavailable' }],
    } })
  })
  await page.goto(`${vista.origin}/settings/about`)
  const button = page.getByRole('button', { name: 'Check coordinator service readiness' })
  await expect(button).toBeVisible()
  expect(calls).toBe(0)
  await button.click()
  const success = page.getByRole('dialog', { name: 'Can serve as the coordinator' })
  await expect(success).toBeVisible()
  await expect(success.getByText('IPv6 — Public access verified', { exact: true })).toBeVisible()
  await expect(success.getByText('The connecting device needs IPv6 support.')).toBeVisible()
  await success.getByRole('button', { name: 'Close', exact: true }).last().click()
  await expect(success).toBeHidden()
  await expect(button).toBeVisible()
  await button.click()
  const unconfirmed = page.getByRole('dialog', { name: 'Coordinator readiness not yet verified' })
  await expect(unconfirmed).toBeVisible()
  await expect(unconfirmed.getByText('IPv4 — No verified incoming request')).toBeVisible()
  await expect(unconfirmed.getByText('IPv6 — No usable result from public probes')).toBeVisible()
  expect(calls).toBe(2)
})
