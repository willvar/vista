import { test, expect } from './fixtures'

test('capacity presets and custom limits save, synchronize and preserve an unsaved editor', async ({ page, vista }, testInfo) => {
  await page.goto(vista.origin + '/settings/about')
  const region = page.getByRole('region', { name: 'Memory and capacity', exact: true })
  const save = region.getByRole('button', { name: 'Save changes', exact: true })
  const choose = async (label: string) => {
    await region.locator('#memory-preset').click()
    await page.locator('.ant-select-dropdown:visible .ant-select-item-option').filter({ hasText: new RegExp('^' + label + '$') }).click()
  }
  await expect(region.getByRole('status')).toContainText('/ 2048 MiB')
  await expect(save).toBeDisabled()
  await choose('1 GiB')
  await expect(region.locator('.memory-limits')).toContainText('768 MiB')
  await save.click()
  await expect(save).toBeDisabled()
  expect(await vista.api('/memory-config')).toEqual({ budgetMiB: 1024, workerMiB: 768, concurrentRuns: 2 })
  await choose('Custom')
  const budget = region.getByRole('spinbutton', { name: 'Memory budget', exact: true })
  const worker = region.getByRole('spinbutton', { name: 'Per-conversation limit', exact: true })
  const concurrent = region.getByRole('spinbutton', { name: 'Concurrent executions', exact: true })
  await budget.fill('3072')
  await worker.fill('1536')
  await concurrent.fill('3')
  // Another client commits a different budget. Usage must update over the shared
  // stream while the editor keeps all three of this client's pending values.
  await vista.api('/memory-config', { method: 'PUT', body: JSON.stringify({ budgetMiB: 4096, workerMiB: 2048, concurrentRuns: 8 }) })
  await expect(region.getByRole('status')).toContainText('/ 4096 MiB')
  await expect(budget).toHaveValue('3072')
  await expect(worker).toHaveValue('1536')
  await expect(concurrent).toHaveValue('3')
  await save.click()
  await expect(save).toBeDisabled()
  expect(await vista.api('/memory-config')).toEqual({ budgetMiB: 3072, workerMiB: 1536, concurrentRuns: 3 })
  await page.reload()
  await expect(budget).toHaveValue('3072')
  await expect(concurrent).toHaveValue('3')
  await region.scrollIntoViewIfNeeded()
  const bounds = await region.evaluate(element => {
    const rect = element.getBoundingClientRect()
    return { left: rect.left, right: rect.right, width: innerWidth, overflow: element.scrollWidth - element.clientWidth }
  })
  expect(bounds.left).toBeGreaterThanOrEqual(0)
  expect(bounds.right).toBeLessThanOrEqual(bounds.width)
  expect(bounds.overflow).toBeLessThanOrEqual(2)
  await page.screenshot({ path: testInfo.outputPath('capacity-settings.png'), animations: 'disabled' })
})
