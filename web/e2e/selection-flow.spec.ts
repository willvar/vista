import { test, expect } from './fixtures'

test('response options fit the viewport and keep the composer clear', { tag: ['@smoke', '@responsive'] }, async ({ page, vista }, testInfo) => {
  const catalog = await vista.api('/models')
  const model = catalog.models.find((item: { key: string }) => item.key === catalog.default)
  const choices: string[] = model.reasoningOptions
  await page.reload()
  for (const location of ['homepage', 'conversation']) {
    if (location === 'conversation') {
      const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
      await page.goto(`${vista.origin}/sessions/${session.id}`)
    }
    const toolbar = page.locator(location === 'homepage' ? '.welcome-toolbar' : '.conversation-header')
    const trigger = toolbar.getByRole('button', { name: 'Response options', exact: true })
    const panel = page.getByRole('dialog', { name: 'Response options', exact: true })
    const reasoning = panel.getByRole('group', { name: 'Reasoning effort', exact: true })
    const sender = page.locator('.sender-card')
    const draft = sender.locator('textarea')
    const small = page.viewportSize()!.width <= 830
    await draft.fill('Draft stays ready while adjusting the next response.')
    await expect(sender.getByRole('group', { name: 'Reasoning effort' })).toHaveCount(0)
    await expect(panel).toBeHidden()
    if (small) await trigger.tap()
    else await trigger.press('Enter')
    await expect(panel).toBeVisible()
    await expect(trigger).toHaveAttribute('aria-expanded', 'true')
    await expect.poll(async () => {
      const [anchor, popup] = await Promise.all([trigger.boundingBox(), panel.boundingBox()])
      const viewport = page.viewportSize()!
      return !!anchor && !!popup && popup.x >= 0 && popup.x + popup.width <= viewport.width
        && popup.y >= anchor.y + anchor.height - 1 && popup.y + popup.height <= viewport.height
        && Math.abs(popup.x + popup.width - anchor.x - anchor.width) <= 2
    }).toBe(true)
    if (small) {
      await expect(reasoning.getByRole('combobox', { name: 'Reasoning effort', exact: true })).toBeVisible()
      await expect(reasoning.getByRole('slider')).toHaveCount(0)
      await draft.tap()
      await expect(panel).toBeHidden()
      await expect(draft).toHaveValue('Draft stays ready while adjusting the next response.')
      continue
    }
    const slider = reasoning.getByRole('slider', { name: 'Reasoning effort', exact: true })
    await page.keyboard.press('Tab')
    await expect(slider).toBeFocused()
    for (const [key, index] of [['Home', 0], ['End', choices.length - 1], ['ArrowLeft', choices.length - 2]] as const) {
      await slider.press(key)
      await expect(slider).toHaveAttribute('aria-valuenow', String(index))
      await expect.poll(() => reasoning.evaluate((element, selected) => {
        const rail = element.querySelector('.ui-slider-rail')!.getBoundingClientRect()
        const thumb = element.querySelector('.ui-slider-handle')!.getBoundingClientRect()
        const range = element.querySelector('.ui-slider-track')!.getBoundingClientRect()
        const marks = [...element.querySelectorAll('.ui-slider-dot')]
        const centerY = rail.y + rail.height / 2
        const selectedX = rail.x + rail.width * selected / (marks.length - 1)
        return Math.max(
          Math.abs(thumb.x + thumb.width / 2 - selectedX),
          Math.abs(thumb.y + thumb.height / 2 - centerY),
          Math.abs(range.right - selectedX),
          ...marks.flatMap((mark, index) => {
            const rect = mark.getBoundingClientRect()
            return [Math.abs(rect.x + rect.width / 2 - (rail.x + rail.width * index / (marks.length - 1))), Math.abs(rect.y + rect.height / 2 - centerY)]
          }),
        )
      }, index)).toBeLessThan(0.6)
    }
    for (const colorScheme of ['light', 'dark'] as const) {
      await page.emulateMedia({ colorScheme })
      await page.screenshot({ path: testInfo.outputPath(`reasoning-${location}-${colorScheme}.png`), animations: 'disabled' })
    }
    await page.keyboard.press('Escape')
    await expect(panel).toBeHidden()
    await expect(trigger).toBeFocused()
    await expect(draft).toHaveValue('Draft stays ready while adjusting the next response.')
  }
})

test.describe('explicit reasoning budgets and toggles', () => {
  test.use({ budgetModel: true, replyWithPrompt: true })

  test('saved controls survive reload and reach provider requests and recorded results', async ({ page, vista }) => {
    const session = await vista.api('/sessions', { method: 'POST', body: '{}' })
    const catalog = await vista.api('/models')
    const model = catalog.models.find((item: { key: string }) => item.key === catalog.default)
    const choices: string[] = model.reasoningOptions
    expect(choices).toContain('budget:8192')
    expect(choices.slice(0, 2)).toEqual(['off', 'on'])
    expect(choices).not.toContain('high')
    await page.goto(`${vista.origin}/sessions/${session.id}`)
    const trigger = page.getByRole('button', { name: 'Response options', exact: true })
    const picker = page.getByRole('dialog', { name: 'Response options', exact: true }).getByRole('group', { name: 'Reasoning effort', exact: true })
    const choose = async (choice: string, label: string) => {
      await trigger.click()
      if (page.viewportSize()!.width <= 830) {
        await picker.getByRole('combobox', { name: 'Reasoning effort', exact: true }).click()
        await page.locator('.ui-select-dropdown:visible').getByText(label, { exact: true }).click()
      } else {
        const slider = picker.getByRole('slider', { name: 'Reasoning effort', exact: true })
        await slider.press('Home')
        for (let index = 0; index < choices.indexOf(choice); index++) await slider.press('ArrowRight')
        await expect(slider).toHaveAttribute('aria-valuetext', label)
      }
      await expect.poll(async () => (await vista.api('/preferences')).reasoningByModelId[model.id]).toBe(choice)
    }
    for (const [choice, label, wire] of [
      ['budget:8192', '8192 tokens', { max_tokens: 8192 }],
      ['off', 'Off', { enabled: false }],
      ['on', 'On', { enabled: true }],
    ] as const) {
      await choose(choice, label)
      await page.reload()
      await trigger.click()
      await expect(picker).toContainText(label)
      await page.keyboard.press('Escape')
      await expect(picker).toBeHidden()
      await page.locator('.sender-card textarea').fill(`Use ${choice}`)
      await page.locator('.sender-card textarea').press('Control+Enter')
      await expect(page.locator('article.assistant').last()).toContainText(`Mock reply: Use ${choice}`)
      await expect(page.getByRole('button', { name: 'Send', exact: true })).toBeVisible()
      expect(vista.modelRequests.at(-1)?.reasoning).toEqual(wire)
      expect(vista.modelRequests.at(-1)?.messages).toEqual(expect.arrayContaining([
        expect.objectContaining({ role: 'user', content: expect.arrayContaining([
          expect.objectContaining({ type: 'text', text: `Use ${choice}` }),
        ]) }),
      ]))
      await expect.poll(async () => {
        const messages = await vista.api(`/sessions/${session.id}/messages`)
        return messages.filter((message: { role: string }) => message.role === 'assistant').at(-1)?.modelResults?.at(-1)?.reasoningEffort
      }).toBe(choice)
    }
  })
})
