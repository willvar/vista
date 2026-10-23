import { closeSettings, openSettingsSection } from './settings-navigation'
import { test, expect } from './fixtures'
import type { Page } from '@playwright/test'
import { messages } from '../src/i18n/catalogs.ts'
import { languages, type Language } from '../src/i18n/languages.ts'

const cases: { browser: string; locale: Language; reply: string }[] = [
  { browser: 'en-US', locale: 'en', reply: 'English' },
  { browser: 'zh-CN', locale: 'zh-CN', reply: 'Simplified Chinese' },
  { browser: 'zh-TW', locale: 'zh-TW', reply: 'Traditional Chinese (Taiwan)' },
  { browser: 'zh-HK', locale: 'zh-HK', reply: 'Traditional Chinese (Hong Kong)' },
  { browser: 'zh-MO', locale: 'zh-MO', reply: 'Traditional Chinese (Macau)' },
  { browser: 'zh-SG', locale: 'zh-SG', reply: 'Simplified Chinese (Singapore)' },
  { browser: 'zh-MY', locale: 'zh-MY', reply: 'Simplified Chinese (Malaysia)' },
  { browser: 'es-MX', locale: 'es-MX', reply: 'Spanish (Mexico)' },
  { browser: 'de-DE', locale: 'de', reply: 'German' },
  { browser: 'ja-JP', locale: 'ja', reply: 'Japanese' },
  { browser: 'ko-KR', locale: 'ko', reply: 'Korean' },
  { browser: 'fr-FR', locale: 'fr', reply: 'French' },
  { browser: 'pt-PT', locale: 'pt-PT', reply: 'European Portuguese' },
  { browser: 'ru-RU', locale: 'ru', reply: 'Russian' },
  { browser: 'en-GB', locale: 'en-GB', reply: 'British English' },
  { browser: 'de-CH', locale: 'de-CH', reply: 'Swiss Standard German' },
]

// Node and Chrome may ship different CLDR names for the same region.
function displayRegions(page: Page, locale: string, regions: readonly string[]) {
  return page.evaluate(({ locale, regions }) => {
    const names = new Intl.DisplayNames([locale], { type: 'region' })
    return regions.map(region => names.of(region)!)
  }, { locale, regions })
}

test('Chinese region selection keeps script groups, fixed defaults and the saved regional catalog', async ({ page, vista }, testInfo) => {
  await page.goto(vista.origin + '/settings/general')
  const picker = page.locator('.language-setting .ant-select')
  const region = page.locator('.region-setting .ant-select')
  const options = page.locator('.ant-select-dropdown:visible .ant-select-item-option')
  for (const [script, locales] of [
    ['zh-Hans', ['zh-CN', 'zh-SG', 'zh-MY']],
    ['zh-Hant', ['zh-TW', 'zh-HK', 'zh-MO']],
  ] as const) {
    await picker.click()
    await picker.locator('input').fill(script)
    await options.click()
    await expect(page.locator('html')).toHaveAttribute('lang', locales[0])
    for (const locale of locales) {
      const current = await page.locator('html').getAttribute('lang')
      const variant = languages.find(language => language.value === locale)!
      const [regionName] = await displayRegions(page, current!, [variant.region])
      await region.click()
      await options.getByText(regionName!, { exact: true }).click()
      await expect(page.locator('html')).toHaveAttribute('lang', locale)
      await expect(picker).toContainText(variant.label)
      await expect(page.locator('.model-selection-setting p')).toHaveText(messages[locale].selectionModeSettingsHint)
      await expect.poll(() => page.evaluate(() => localStorage.getItem('vista-language'))).toBe(locale)
      await page.reload()
      await expect(page.locator('html')).toHaveAttribute('lang', locale)
      await expect(region).toContainText((await displayRegions(page, locale, [variant.region]))[0]!)
      await page.screenshot({ path: testInfo.outputPath(`${locale}-manual-region.png`), animations: 'disabled' })
    }
  }
})

test('a language with one region hides the region picker and keeps its selection after reload', async ({ page, vista }, testInfo) => {
  await page.goto(vista.origin + '/settings/general')
  const picker = page.locator('.language-setting .ant-select')
  const options = page.locator('.ant-select-dropdown:visible .ant-select-item-option')
  for (const [search, locale] of [['French', 'fr'], ['Russian', 'ru'], ['Korean', 'ko'], ['Japanese', 'ja']] as const) {
    await picker.click()
    await picker.locator('input').fill(search)
    await options.click()
    await expect(page.locator('html')).toHaveAttribute('lang', locale)
    await expect(page.locator('.region-setting')).toHaveCount(0)
    await expect(page.locator('.locale-settings')).toHaveClass(/locale-settings-auto/)
    await page.reload()
    await expect(page.locator('html')).toHaveAttribute('lang', locale)
    await expect(picker).toContainText(languages.find(language => language.value === locale)!.label)
    await expect(page.locator('.region-setting')).toHaveCount(0)
  }
  await page.screenshot({ path: testInfo.outputPath('single-region-language.png'), animations: 'disabled' })
  await picker.click()
  await picker.locator('input').fill('English')
  await options.click()
  await expect(page.locator('html')).toHaveAttribute('lang', 'en')
  await expect(page.locator('.region-setting')).toBeVisible()
})

test('regional wording and formatting choices survive manual selection and reload', async ({ page, vista }, testInfo) => {
  await page.route('**/api/model-catalog', async route => {
    const response = await route.fetch()
    await route.fulfill({ response, json: { ...await response.json(), updatedAt: '2026-10-20T03:15:42Z' } })
  })
  await page.goto(vista.origin + '/settings/general')
  const picker = page.locator('.language-setting .ant-select')
  const region = page.locator('.region-setting .ant-select')
  const options = page.locator('.ant-select-dropdown:visible .ant-select-item-option')
  for (const [search, initial, selected] of [
    ['English', 'en', 'en-GB'], ['Português', 'pt-BR', 'pt-PT'],
    ['Deutsch', 'de', 'de-CH'], ['Español', 'es', 'es-MX'],
  ] as const) {
    await picker.click()
    await picker.locator('input').fill(search)
    await options.click()
    await expect(page.locator('html')).toHaveAttribute('lang', initial)
    const variant = languages.find(language => language.value === selected)!
    const [label] = await displayRegions(page, initial, [variant.region])
    await region.click()
    await region.locator('input').fill(label!)
    await options.getByText(label!, { exact: true }).click()
    await expect(page.locator('html')).toHaveAttribute('lang', selected)
    await expect(page.locator('.settings-trigger')).toHaveText(messages[selected].settings)
    await page.reload()
    await expect(page.locator('html')).toHaveAttribute('lang', selected)
    await expect(region).toContainText((await displayRegions(page, selected, [variant.region]))[0]!)
    await expect.poll(() => page.evaluate(() => localStorage.getItem('vista-language'))).toBe(selected)
    const expected = await page.evaluate(locale => new Intl.DateTimeFormat(locale, {
      year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23',
    }).format(new Date('2026-10-20T03:15:42Z')), selected)
    await openSettingsSection(page, 'about')
    await expect(page.locator('.omp-catalog-updated')).toContainText(expected)
    await openSettingsSection(page, 'general')
    await page.screenshot({ path: testInfo.outputPath(`${selected}-manual.png`), animations: 'disabled' })
  }
})

for (const { browser, locale, reply } of cases) {
  test.describe(`UI language ${locale}`, () => {
    test.use({ locale: browser })
    test('browser detection, searchable override, settings layout and reply language', async ({ page, vista }, testInfo) => {
      const text = messages[locale]
      const errors: string[] = []
      page.on('pageerror', error => errors.push(error.message))
      await expect(page.locator('html')).toHaveAttribute('lang', locale)
      await expect(page.locator('.welcome h2')).toHaveText(text.welcomeHeading)
      await expect(page.locator('.welcome-toolbar')).not.toContainText(text.newConversation)
      await expect(page.locator('.welcome-intro > p')).toHaveCount(0)
      await page.goto(vista.origin + '/settings/general')
      await expect(page.locator('.page-heading h2')).toHaveText(text.appearance)
      const picker = page.locator('.language-setting .ant-select')
      const region = page.locator('.region-setting .ant-select')
      await expect(page.locator('.region-setting')).toHaveCount(0)
      await picker.click()
      const options = page.locator('.ant-select-dropdown:visible .ant-select-item-option')
      // Ant Select virtualizes long lists; query its options by filtering.
      if (locale === 'ko') {
        for (const alias of ['韩语', '朝鲜语', '조선말', '조선어']) {
          await picker.locator('input').fill(alias)
          await expect(options).toHaveText(['한국어'])
        }
      }
      if (locale === 'pt-BR') {
        const variant = languages.find(language => language.value === locale)!
        await picker.locator('input').fill(variant.label)
        await options.click()
        await expect(region.getByRole('combobox')).toBeEnabled()
        await expect(region).toContainText((await displayRegions(page, locale, [variant.region]))[0]!)
        await page.screenshot({ path: testInfo.outputPath(`${locale}-manual-region-light.png`), animations: 'disabled' })
        await picker.click()
      }
      const override = locale === 'en' ? 'zh-TW' : 'en'
      await picker.locator('input').fill(override === 'en' ? 'English' : '繁体')
      await expect(options).toHaveText([languages.find(language => language.value === override)!.label])
      await options.click()
      await expect(page.locator('html')).toHaveAttribute('lang', override)
      const variant = languages.find(language => language.value === override)!
      const [selectedRegion] = await displayRegions(page, override, [variant.region])
      await expect(region.getByRole('combobox')).toBeEnabled()
      await expect(region).toContainText(selectedRegion)
      await region.click()
      // Filter using the current locale's region name.
      await region.locator('input').fill(selectedRegion!)
      await options.getByText(selectedRegion!, { exact: true }).click()
      const [languageBox, regionBox] = await Promise.all([picker.boundingBox(), region.boundingBox()])
      expect(regionBox!.x).toBeGreaterThanOrEqual(languageBox!.x + languageBox!.width)
      expect(regionBox!.y).toBe(languageBox!.y)
      await page.reload()
      await expect(page.locator('html')).toHaveAttribute('lang', override)
      await expect(region).toContainText(selectedRegion)
      await picker.click()
      await picker.locator('input').fill('auto')
      await options.click()
      await expect(page.locator('.region-setting')).toHaveCount(0)
      await page.reload()
      await expect(page.locator('html')).toHaveAttribute('lang', locale)
      await expect(picker).toContainText(text.automaticLanguage)
      await expect(page.locator('.region-setting')).toHaveCount(0)

      const checkLayout = async () => {
        const bounds = await page.locator('.settings-page').evaluate(element => ({
          page: document.documentElement.scrollWidth - innerWidth,
          body: element.scrollWidth - element.clientWidth,
          content: [...element.querySelectorAll<HTMLElement>('.settings-content')].map(item => item.scrollWidth - item.clientWidth),
          left: element.getBoundingClientRect().left, right: element.getBoundingClientRect().right, width: innerWidth,
        }))
        expect(bounds.page).toBeLessThanOrEqual(2)
        expect(bounds.body).toBeLessThanOrEqual(2)
        expect(bounds.content.every(width => width <= 2)).toBe(true)
        expect(bounds.left).toBeGreaterThanOrEqual(-1)
        expect(bounds.right).toBeLessThanOrEqual(bounds.width + 1)
      }
      for (const theme of ['light', 'dark'] as const) {
        await page.emulateMedia({ colorScheme: theme })
        await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
        await checkLayout()
        await page.screenshot({ path: testInfo.outputPath(`${locale}-general-${theme}.png`), animations: 'disabled' })
      }
      for (const [path, label, action] of [
        ['models', text.modelConnections, text.connectProvider],
        ['mcp', text.mcpSettings, text.mcpAdd],
        ['skills', text.skillsSettings, text.refresh],
        ['storage', text.storageSettings, text.resourceStorageRefresh],
        ['about', text.aboutSettings, text.modelCatalogCheck],
      ]) {
        await openSettingsSection(page, path!)
        await expect(page.locator('.page-heading h2')).toHaveText(label!)
        const button = page.getByRole('button', { name: action!, exact: true })
        await expect(button).toHaveAttribute('aria-label', action!)
        await expect(button).toBeEnabled()
        await expect(page.locator('.settings-content').getByRole('button', { name: action!, exact: true })).toHaveCount(0)
        const bounds = await button.boundingBox()
        const close = await page.locator('.modal-close').boundingBox()
        if (close) {
          expect(close.x - (bounds!.x + bounds!.width)).toBeGreaterThanOrEqual(0)
          expect(close.x - (bounds!.x + bounds!.width)).toBeLessThanOrEqual(16)
        }
        await checkLayout()
        expect(new URL(page.url()).pathname).toBe(`/settings/${path}`)
        if (locale === 'zh-CN' || locale === 'ko') await page.screenshot({ path: testInfo.outputPath(`${locale}-${path}-dark.png`), animations: 'disabled' })
      }
      await openSettingsSection(page, 'search')
      const orders = page.locator('.web-order-sections')
      const preview = page.locator('.web-preview-section')
      await expect(orders.locator('.web-order-form')).toHaveCount(2)
      await expect(preview.locator('figure')).toHaveCount(1)
      await expect(preview.locator('.web-preview-form')).toHaveCount(2)
      await expect(preview.locator('figure')).not.toContainText(/[↘↗→]/)
      await checkLayout()
      await page.screenshot({ path: testInfo.outputPath(`${locale}-search-dark.png`), animations: 'disabled' })
      // A nested modal uses the chosen locale too.
      await page.locator('.search-order-form [data-provider="exa"] button').first().click()
      const dialog = page.getByRole('dialog', { name: text.editSearchKey.replace('{name}', 'Exa'), exact: true })
      const cancel = dialog.getByRole('button', { name: text.cancel === '取消' ? /^取\s*消$/ : text.cancel, exact: true })
      await expect(cancel).toBeVisible()
      await expect(dialog.getByRole('button', { name: text.saveChanges, exact: true })).toBeVisible()
      await page.screenshot({ path: testInfo.outputPath(`${locale}-key-dark.png`), animations: 'disabled' })
      await cancel.click()
      await closeSettings(page)
      const sender = page.locator('.sender-card textarea')
      await sender.fill('Check language metadata')
      await sender.press('Enter')
      await expect.poll(() => vista.modelRequests.length).toBeGreaterThan(0)
      const request = vista.modelRequests[0]!
      const prompt = String(request.messages?.find(message => message.role === 'system')?.content)
      expect(prompt).toContain(`Default to replies in ${reply}; follow the user's explicit language requests.`)
      expect(request.messages?.filter(message => message.role === 'user').at(-1)?.content).toBe('Check language metadata')
      expect(errors).toEqual([])
    })
  })
}
