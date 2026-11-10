import { test, expect } from './fixtures'

test.beforeEach(async ({ vista }) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ feedbackInvitationSeen: true }) })
})

test('composer retains its text padding and borderless model search states', { tag: '@responsive' }, async ({ page, vista }) => {
  await page.goto(vista.origin)
  const input = page.locator('.sender-card textarea')
  await expect(input).toBeEnabled()
  const padding = await input.evaluate(element => ({ top: getComputedStyle(element).paddingTop, bottom: getComputedStyle(element).paddingBottom }))
  expect(padding).toEqual(page.viewportSize()!.height <= 520 ? { top: '0px', bottom: '0px' } : { top: '6px', bottom: '6px' })
  const model = page.locator('.composer-model')
  const label = await model.locator('.ui-select-selection-item').innerText()
  await model.hover()
  await model.click()
  await expect(page.locator('.ui-select-dropdown:visible')).toBeVisible()
  await expect(model.getByRole('combobox')).toHaveAttribute('placeholder', label)
  expect(await model.locator('.ui-select-selector').evaluate(element => getComputedStyle(element).borderColor)).toBe('rgba(0, 0, 0, 0)')
  await model.getByRole('combobox').fill('No matching model')
  await expect(page.locator('.ui-select-empty:visible')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(model).toContainText(label)
})

test('ordinary selects reject typing without filtering and still support keyboard selection', async ({ page, vista }) => {
  for (const [route, ids] of [
    ['general', ['model-selection-mode']],
    ['about', ['user-agent-config-preset-input', 'capacity-preset-preset-input']],
    ['statistics', ['statistics-period']],
  ] as const) {
    await page.goto(`${vista.origin}/settings/${route}`)
    for (const id of ids) {
      const select = page.locator(`#${id}`)
      await expect(select).toBeEnabled()
      await expect(select).not.toBeEditable()
      const previous = await select.inputValue()
      await select.click()
      const options = page.locator('.ui-select-dropdown:visible').getByRole('option')
      const count = await options.count()
      expect(count).toBeGreaterThan(0)
      if (id === 'model-selection-mode') {
        await expect(page.locator('.ui-select-dropdown:visible').getByRole('option', { selected: true }))
          .toHaveAttribute('data-highlighted', '')
      }
      await select.pressSequentially('no matching option')
      await expect(options).toHaveCount(count)
      await expect(select).toHaveValue(previous)
      if (id === 'model-selection-mode') {
        const last = await options.last().innerText()
        await select.press('End')
        await expect(options.last()).toHaveAttribute('data-highlighted', '')
        await select.press('Enter')
        await expect(select).toHaveValue(last)
      } else await select.press('Escape')
    }
  }
})

test('dialog programmatic focus is quiet while its keyboard controls remain visible', async ({ page, vista }) => {
  await page.goto(vista.origin + '/settings/general')
  const dialog = page.locator('.workspace-modal .ui-modal-container')
  await expect(dialog).toBeFocused()
  expect(await dialog.evaluate(element => getComputedStyle(element).outlineStyle)).toBe('none')
  await page.keyboard.press('Tab')
  const focus = await page.evaluate(() => ({ inDialog: !!document.activeElement?.closest('.ui-modal-container'),
    outline: getComputedStyle(document.activeElement!).outlineStyle }))
  expect(focus.inDialog).toBe(true)
  expect(focus.outline).toBe('solid')
})

test('dark tooltips keep a dark surface and readable light text', async ({ page, vista, isMobile }) => {
  test.skip(isMobile, 'Hover tooltip colors are checked with a desktop pointer')
  await page.addInitScript(() => localStorage.setItem('vista-appearance', 'dark'))
  await page.goto(vista.origin)
  const sender = page.locator('.sender-card')
  await sender.locator('textarea').fill('Inspect tooltip colors')
  await sender.getByRole('button', { name: 'Send', exact: true }).click()
  const reply = page.locator('article.assistant').last()
  await expect(reply).toContainText('Mock reply: Hi')
  await reply.getByRole('button', { name: 'Copy reply', exact: true }).hover()
  const tooltip = page.locator('.ui-tooltip:visible')
  await expect(tooltip).toBeVisible()
  expect(await tooltip.evaluate(element => ({ background: getComputedStyle(element).backgroundColor, text: getComputedStyle(element).color })))
    .toEqual({ background: 'rgb(67, 69, 74)', text: 'rgb(249, 250, 251)' })
})

test('danger actions keep their warning colors before and during hover', async ({ page, vista }) => {
  await vista.api('/sessions', { method: 'POST', body: '{}' })
  await page.goto(vista.origin + '/sessions')
  await page.locator('.managed-session').first().getByRole('button', { name: 'Session menu', exact: true }).click()
  const remove = page.getByRole('menuitem', { name: 'Delete', exact: true })
  await expect(remove).toBeVisible()
  await expect(remove).toHaveClass(/ui-menu-item-danger/)
  const warning = await remove.evaluate(element => getComputedStyle(element).color)
  await remove.hover()
  expect(await remove.evaluate(element => getComputedStyle(element).color)).toBe(warning)
  await remove.click()
  const confirm = page.locator('.session-delete-modal').getByRole('button', { name: 'Delete', exact: true })
  await expect(confirm).toBeVisible()
  await confirm.hover()
  await expect.poll(() => confirm.evaluate(element => getComputedStyle(element).color)).toBe('rgb(255, 255, 255)')
  const colors = await confirm.evaluate(element => ({ color: getComputedStyle(element).color, background: getComputedStyle(element).backgroundColor }))
  expect(colors.background).not.toBe(colors.color)
  const [red, green, blue] = colors.background.match(/\d+(?:\.\d+)?/g)!.map(Number)
  expect(red).toBeGreaterThan(green!)
  expect(red).toBeGreaterThan(blue!)
  await page.keyboard.press('Escape')
  await expect(page.locator('.managed-session')).toHaveCount(1)
})

test('statistics tab marker follows selection, keyboard navigation and reduced motion', { tag: '@responsive' }, async ({ page, vista }) => {
  await vista.api('/preferences', { method: 'PATCH', body: JSON.stringify({ lastMode: 'auto' }) })
  await page.goto(vista.origin + '/settings/general')
  const stats = page.locator('.model-selection-stats')
  const marker = stats.locator('.ui-tabs-indicator')
  for (const reducedMotion of ['no-preference', 'reduce'] as const) {
    await page.emulateMedia({ reducedMotion })
    for (const name of ['Quality', 'Speed']) {
      const tab = stats.getByRole('tab', { name, exact: true })
      await tab.click()
      await expect(stats.getByRole('tabpanel', { name, exact: true })).toBeVisible()
      // Reduced motion keeps a 1ms transition for component cleanup events.
      const animated = await marker.evaluate(element => element.getAnimations().some(animation =>
        animation instanceof CSSTransition && Number(animation.effect?.getTiming().duration) > 1))
      expect(animated).toBe(reducedMotion === 'no-preference')
      await expect.poll(() => marker.evaluate(element => {
        const selected = element.parentElement!.querySelector('[aria-selected="true"]')!
        const m = element.getBoundingClientRect(), t = selected.getBoundingClientRect()
        return Math.max(Math.abs(m.x - t.x), Math.abs(m.width - t.width))
      })).toBeLessThan(1)
      const duration = await marker.evaluate(element => Math.max(...getComputedStyle(element).transitionDuration.split(',').map(parseFloat)))
      if (reducedMotion === 'reduce') expect(duration).toBeLessThanOrEqual(.001)
    }
  }
  await stats.getByRole('tab', { name: 'Speed', exact: true }).press('ArrowRight')
  await expect(stats.getByRole('tab', { name: 'Quality', exact: true })).toBeFocused()
  await expect(stats.getByRole('tabpanel', { name: 'Quality', exact: true })).toBeVisible()
})

test('password visibility has matching text, icon and input state', async ({ page, vista }) => {
  await page.goto(vista.origin + '/settings/models')
  await page.getByRole('button', { name: 'Connect provider', exact: true }).click()
  const form = page.locator('.provider-connection-modal .model-form')
  const close = page.locator('.provider-connection-modal').getByRole('button', { name: 'Close', exact: true })
  await expect(close).toHaveClass(/ui-btn-text/)
  await expect(close).toHaveAttribute('type', 'button')
  const provider = form.getByRole('combobox').first()
  await provider.click()
  await expect(provider).toHaveAttribute('aria-expanded', 'true')
  await provider.fill('Custom provider')
  await page.getByRole('option', { name: 'Custom provider', exact: true }).click()
  const password = form.locator('.ui-password input')
  await password.fill('test-secret')
  await expect(password).toHaveAttribute('type', 'password')
  await expect(form.locator('.ui-password .tabler-icon-eye-off')).toBeVisible()
  await form.getByRole('button', { name: 'Show value', exact: true }).click()
  await expect(password).toHaveAttribute('type', 'text')
  await expect(form.locator('.ui-password .tabler-icon-eye')).toBeVisible()
  await form.getByRole('button', { name: 'Hide value', exact: true }).press('Enter')
  await expect(password).toHaveAttribute('type', 'password')
  await expect(password).toHaveValue('test-secret')
})

test('a pending message withdrawal cannot be dismissed with Escape', async ({ page, vista }) => {
  await page.goto(vista.origin)
  const sender = page.locator('.sender-card textarea')
  await sender.fill('Withdraw after completion')
  await sender.press('Enter')
  await expect(page.locator('article.assistant').last()).toContainText('Mock reply: Hi')
  let release!: () => void
  const gate = new Promise<void>(resolve => { release = resolve })
  await page.route('**/api/sessions/*/messages/*/revert', async route => { await gate; await route.continue() })
  try {
    const user = page.locator('article.user').last()
    await user.hover()
    await user.getByRole('button', { name: 'Withdraw message', exact: true }).click()
    const dialog = page.getByRole('dialog', { name: 'Withdraw this message?', exact: true })
    await expect(dialog).toBeVisible()
    await dialog.getByRole('button', { name: 'Withdraw message', exact: true }).click()
    await expect(dialog.getByRole('button', { name: 'Cancel', exact: true })).toBeDisabled()
    await page.keyboard.press('Escape')
    await expect(dialog).toBeVisible()
    release()
    await expect(dialog).toHaveCount(0)
    await expect(sender).toHaveValue('Withdraw after completion')
  } finally { release() }
})

test('switches distinguish off and on in both themes and keep the native state synchronized', async ({ page, vista }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  for (const theme of ['light', 'dark']) {
    await page.goto(vista.origin)
    await page.evaluate(theme => localStorage.setItem('vista-appearance', theme), theme)
    await page.goto(vista.origin + '/settings/about')
    const input = page.getByRole('switch', { name: 'Debug mode', exact: true })
    const track = page.locator('.debug-mode-setting .ui-switch-track')
    const off = theme === 'light' ? 'rgba(0, 0, 0, 0.25)' : 'rgba(255, 255, 255, 0.25)'
    await expect(input).not.toBeChecked()
    await expect(track).toHaveCSS('background-color', off)
    await input.check()
    await expect(input).toBeChecked()
    await expect(input).toHaveAttribute('aria-checked', 'true')
    await expect.poll(() => track.evaluate(element => {
      const probe = document.createElement('span')
      probe.style.color = 'var(--primary)'
      element.append(probe)
      const primary = getComputedStyle(probe).color
      probe.remove()
      return getComputedStyle(element).backgroundColor === primary
    })).toBe(true)
    await input.uncheck()
    await expect(input).not.toBeChecked()
    await expect(input).toHaveAttribute('aria-checked', 'false')
    await expect(track).toHaveCSS('background-color', off)
  }
})

test.describe('inline execution messages', () => {
  test.use({ progressCalls: 1 })
  for (const reducedMotion of ['no-preference', 'reduce'] as const) {
    test(`tool headers toggle their result in place with ${reducedMotion} motion`, { tag: '@responsive' }, async ({ page, vista }) => {
      await page.emulateMedia({ reducedMotion })
      await page.goto(vista.origin)
      const input = page.locator('.sender-card textarea')
      await input.fill('Check execution details')
      await input.press('Enter')
      await expect(page.locator('article.assistant').last()).toContainText('Final progress answer.')
      await expect(page.getByRole('button', { name: 'Execution process', exact: true })).toHaveCount(0)
      const tool = page.locator('.ui-thought-chain-node').filter({ hasText: 'Run command' })
      await expect(tool.locator('.trace-step-content')).toHaveCount(0)
      const readFull = tool.locator('button.ui-thought-chain-node-header')
      await expect(readFull).toHaveAttribute('aria-expanded', 'false')
      await expect(tool.locator('.tool-output-entry')).toHaveCount(0)
      await readFull.click()
      await expect(tool.locator('.tool-output-entry')).toContainText('Tool result')
      const hideFull = readFull
      await expect(hideFull).toHaveAttribute('aria-expanded', 'true')
      await hideFull.click()
      await expect(tool.locator('.tool-output-entry')).toHaveCount(0)
      await expect(tool.locator('.trace-step-content')).toHaveCount(0)
    })
  }
})
