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
    const reasoning = panel.getByRole('combobox', { name: 'Reasoning effort', exact: true })
    const sender = page.locator('.sender-card')
    const draft = sender.locator('textarea')
    const small = page.viewportSize()!.width <= 830
    await draft.fill('Draft stays ready while adjusting the next response.')
    await expect(sender.getByRole('combobox', { name: 'Reasoning effort' })).toHaveCount(0)
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
    await expect(reasoning).toBeVisible()
    await expect(panel.getByRole('slider')).toHaveCount(0)
    if (small) await reasoning.tap()
    else {
      await page.keyboard.press('Tab')
      await expect(reasoning).toBeFocused()
      await reasoning.press('ArrowDown')
    }
    const list = page.locator('.ui-select-dropdown:visible')
    await expect(list.getByRole('option')).toHaveCount(choices.length)
    await page.keyboard.press('Escape')
    await expect(list).toBeHidden()
    await expect(panel).toBeVisible()
    if (small) await draft.tap()
    else {
      for (const colorScheme of ['light', 'dark'] as const) {
        await page.emulateMedia({ colorScheme })
        await page.screenshot({ path: testInfo.outputPath(`reasoning-${location}-${colorScheme}.png`), animations: 'disabled' })
      }
      await page.keyboard.press('Escape')
      await expect(trigger).toBeFocused()
    }
    await expect(panel).toBeHidden()
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
    const picker = page.getByRole('dialog', { name: 'Response options', exact: true }).getByRole('combobox', { name: 'Reasoning effort', exact: true })
    const choose = async (choice: string, label: string) => {
      await trigger.click()
      await picker.click()
      await page.locator('.ui-select-dropdown:visible').getByRole('option', { name: label, exact: true }).click()
      await expect(picker).toHaveValue(label)
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
      await expect(picker).toHaveValue(label)
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
