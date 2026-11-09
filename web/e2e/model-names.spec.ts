import { closeSettings } from './settings-navigation'
import { test, expect } from './fixtures'

test('catalog names appear in both selectors while raw model IDs remain searchable and usable', async ({ page, vista }, testInfo) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
  const catalog = await vista.api('/models')
  const model = catalog.models.find((item: { key: string }) => item.key === catalog.default)
  expect(model.id).toBe(vista.modelId)
  expect(model.modelName).toBeTruthy()
  expect(model.modelName).not.toBe(model.id)
  const label = `${model.name} · ${model.modelName}`
  const authHeaders: string[] = []
  page.on('request', request => {
    if (new URL(request.url()).pathname.startsWith('/api/') && request.headers().authorization) {
      authHeaders.push(request.headers().authorization!)
    }
  })
  const checkSearch = async (selector: ReturnType<typeof page.locator>, option: string) => {
    await selector.click()
    const input = selector.locator('input')
    for (const query of [model.id, model.modelName]) {
      await input.fill(query)
      const dropdown = page.locator('.ui-select-dropdown:visible')
      const group = dropdown.getByRole('group', { name: model.name, exact: true })
      await expect(group).toBeVisible()
      await expect(group.locator('.ui-select-group-label')).toHaveText(model.name)
      await expect(group.locator('.ui-select-item-option-content')).toHaveText([option])
      const lines = await group.locator('.ui-select-item-option-content').evaluate(element => {
        const range = document.createRange()
        range.selectNodeContents(element)
        return range.getClientRects().length
      })
      expect(lines, 'The short model name stays on one line in the expanded picker').toBe(1)
      const heading = await group.locator('.ui-select-group-label').boundingBox()
      const row = await group.getByRole('option').boundingBox()
      expect(heading!.y + heading!.height).toBeLessThanOrEqual(row!.y + 1)
      await expect(dropdown.locator('.ui-select-option-group')).toHaveCount(0)
    }
    await page.locator('.ui-select-dropdown:visible .ui-select-item-option').click()
  }
  await page.goto(vista.origin + '/settings/models')
  const defaultSelector = page.locator('.default-model-setting .ui-select')
  await expect(defaultSelector).toContainText(label)
  await checkSearch(defaultSelector, model.modelName)
  await expect(defaultSelector).toContainText(label)
  await page.reload()
  await expect(defaultSelector).toContainText(label)
  await closeSettings(page)
  const sender = page.locator('.sender-card')
  const picker = sender.locator('.composer-model')
  const viewport = page.viewportSize()!
  const selectedLabel = viewport.width <= 830 ? model.modelName : label
  await expect(picker.locator('.ui-select-selection-item')).toHaveText(selectedLabel)
  await expect(picker.locator('input')).toHaveValue(selectedLabel)
  if (viewport.width <= 830) {
    const size = await picker.evaluate(element => {
      const text = document.createRange()
      text.selectNodeContents(element.querySelector('.ui-select-selection-item')!)
      const style = getComputedStyle(element.querySelector('.ui-select-selector')!)
      return { actual: element.getBoundingClientRect().width, natural: text.getBoundingClientRect().width
        + parseFloat(style.paddingLeft) + parseFloat(style.paddingRight)
        + parseFloat(style.borderLeftWidth) + parseFloat(style.borderRightWidth) }
    })
    expect(Math.abs(size.actual - size.natural), 'A short model name does not stretch to fill the options row').toBeLessThanOrEqual(2)
  }
  async function footerRows() {
    return sender.evaluate(element => {
      const options = element.querySelector('.sender-options')!.getBoundingClientRect()
      const actions = element.querySelector('.sender-actions')!.getBoundingClientRect()
      return { optionsCenter: options.y + options.height / 2, actionsCenter: actions.y + actions.height / 2,
        optionsBottom: options.bottom, actionsTop: actions.top }
    })
  }
  // The labeled Send action needs more space than its former icon-only form.
  // Verify the fitting row separately from the narrow-screen wrapping case.
  if (viewport.width <= 830) await page.setViewportSize({ ...viewport, width: Math.max(480, viewport.width) })
  let rows = await footerRows()
  expect(Math.abs(rows.optionsCenter - rows.actionsCenter), 'Options and actions share a row when they fit').toBeLessThanOrEqual(1)
  if (viewport.width <= 830) {
    await page.setViewportSize({ ...viewport, width: 320 })
    rows = await footerRows()
    expect(rows.actionsTop, 'Actions wrap below the options when there is insufficient space').toBeGreaterThanOrEqual(rows.optionsBottom)
    await page.setViewportSize(viewport)
  }
  // The picker option displays the catalog model name; the provider label is
  // kept in the group heading and in the selected desktop control.
  await picker.click()
  await page.screenshot({ path: testInfo.outputPath('composer-model-options.png'), animations: 'disabled' })
  await page.keyboard.press('Escape')
  await checkSearch(picker, model.modelName)
  await expect(picker.locator('.ui-select-selection-item')).toHaveText(selectedLabel)
  await page.setViewportSize({ ...viewport, width: viewport.width <= 830 ? 1024 : 390 })
  await expect(picker.locator('.ui-select-selection-item')).toHaveText(viewport.width <= 830 ? label : model.modelName)
  await expect(picker.locator('input')).toHaveValue(viewport.width <= 830 ? label : model.modelName)
  await page.setViewportSize(viewport)
  await expect(picker.locator('.ui-select-selection-item')).toHaveText(selectedLabel)
  await sender.locator('textarea').fill('Hi')
  await sender.getByRole('button', { name: 'Send', exact: true }).click()
  const reply = page.locator('article.assistant').filter({ hasText: 'Mock reply: Hi' })
  await expect(reply.locator('.reply-footer .message-model-name')).toContainText(label)
  const session = new URL(page.url()).pathname
  const messages = await vista.api(`${session}/messages`)
  expect(messages.find((item: { role: string }) => item.role === 'assistant').modelId).toBe(model.id)
  await page.reload()
  await expect(picker.locator('.ui-select-selection-item')).toHaveText(selectedLabel)
  expect(new URL(page.url()).hash).toBe('')
  expect(await page.evaluate(() => sessionStorage.getItem('vista-token'))).toBeNull()
  expect(authHeaders).toEqual([])
  await page.screenshot({ path: testInfo.outputPath('catalog-model-name.png'), animations: 'disabled' })
  if (viewport.width <= 830) {
    const longName = 'Model with a very long name '.repeat(8).trim()
    await page.route('**/api/models', route => route.fulfill({ json: {
      ...catalog, models: catalog.models.map((item: { key: string }) => item.key === model.key ? { ...item, modelName: longName } : item),
    } }))
    await page.reload()
    await expect(picker.locator('.ui-select-selection-item')).toHaveText(longName)
    const layout = await picker.evaluate(element => {
      const label = element.querySelector('.ui-select-selection-item')!
      const model = element.getBoundingClientRect()
      const reasoning = document.querySelector('.composer-reasoning')!.getBoundingClientRect()
      return { clipped: label.scrollWidth > label.clientWidth, right: model.right, reasoningLeft: reasoning.left,
        pageWidth: document.documentElement.scrollWidth, viewportWidth: innerWidth }
    })
    expect(layout.clipped, 'Long names are ellipsized within the available space').toBe(true)
    expect(layout.right).toBeLessThanOrEqual(layout.reasoningLeft)
    expect(layout.pageWidth).toBeLessThanOrEqual(layout.viewportWidth)
    await page.screenshot({ path: testInfo.outputPath('long-model-name.png'), animations: 'disabled' })
  }
})
