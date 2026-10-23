import assert from 'node:assert/strict'
import test from 'node:test'
import { createI18n } from 'vue-i18n'
import { readFileSync } from 'node:fs'
import { catalogs, messages, pluralRules } from './catalogs.ts'
import { browserLanguage, isLanguagePreference, languageInfo, languages } from './languages.ts'
import { regionalOverrides } from './regional-overrides.ts'
import { regions } from './regions.ts'
import { replyLanguageNames } from '../../scripts/generate-reply-languages.ts'
import { formatDateTime } from '../ui/datetime.ts'
import { validateCatalogs } from './validate.ts'

test('every language has a complete, valid catalog with matching placeholders', () => {
  assert.deepEqual(Object.keys(messages).sort(), languages.map(language => language.value).sort())
  assert.equal(languages.length, 18)
  assert.equal(new Set(languages.map(language => language.value)).size, 18)
  assert.equal(Object.keys(catalogs).length, 18)
  assert.deepEqual(validateCatalogs(catalogs), [])
  for (const language of languages) {
    assert.strictEqual(messages[language.value], catalogs[language.catalog])
    assert.doesNotThrow(() => new Intl.DateTimeFormat(language.value).format(new Date()))
  }
  for (const [group, entries] of Object.entries(regions)) {
    assert.equal(new Set(entries).size, entries.length, group)
    assert.ok(entries.every(region => /^[A-Z]{2}$/.test(region)), group)
    const variants = languages.filter(language => language.language === group)
    assert.equal(new Set(variants.map(language => language.catalog)).size, variants.length, `${group}: region options require distinct wording variants`)
  }
})

test('regional catalogs share base wording and only retain actual regional differences', () => {
  assert.equal(messages['en-GB'].mcpLoginCanceled, 'Authorisation cancelled.')
  assert.equal(messages.en.mcpLoginCanceled, 'Authorization canceled.')
  assert.equal(messages['pt-PT'].saveChanges, 'Guardar alterações')
  assert.equal(messages['pt-BR'].saveChanges, 'Salvar alterações')
  assert.equal(messages['es-MX'].settings, 'Configuración')
  assert.equal(messages.es.settings, 'Ajustes')
  assert.equal(messages['de-CH'].close, 'Schliessen')
  assert.equal(messages.de.close, 'Schließen')
  assert.ok(Object.values(messages['de-CH']).every(text => !text.includes('ß')))
  assert.equal(messages['en-GB'].settings, messages.en.settings)
  for (const [variant, overrides] of Object.entries(regionalOverrides)) {
    const base = variant === 'pt-PT' ? catalogs['pt-BR'] : catalogs[variant.split('-')[0] as 'en' | 'de' | 'es']
    for (const [key, text] of Object.entries(overrides)) assert.notEqual(text, base[key as keyof typeof base], `${variant}.${key} is redundant`)
  }
  // Protocol tokens stay unchanged even when the surrounding text is localized.
  for (const catalog of Object.values(catalogs)) assert.ok(catalog.mcpTokenHint.includes('Authorization: Bearer'))
})

test('all regions have matching backend reply languages without independent translation files', () => {
  const names = JSON.parse(readFileSync(new URL('../../../internal/replylanguage/language_names.json', import.meta.url), 'utf8'))
  assert.deepEqual(names, replyLanguageNames())
  assert.deepEqual(Object.keys(names).sort(), languages.map(language => language.value).sort())
  assert.equal(names['en-GB'], 'British English')
  assert.equal(names['pt-PT'], 'European Portuguese')
  assert.equal(names['de-CH'], 'Swiss Standard German')
  assert.equal(names['es-MX'], 'Spanish (Mexico)')
})

test('wording variants also select regional date and number formatting', () => {
  const utc = '2026-10-20T03:15:42Z'
  assert.notEqual(formatDateTime(utc, 'en-GB'), formatDateTime(utc, 'en'))
  assert.notEqual(new Intl.NumberFormat('de-CH').format(12345.6), new Intl.NumberFormat('de').format(12345.6))
  assert.equal(languageInfo('pt-PT').region, 'PT')
})

test('catalog validation rejects missing, extra, empty, malformed and mismatched messages', () => {
  const errors = validateCatalogs({ en: { a: 'Hello {name}', b: 'Save', c: 'Code', d: 'Done' },
    test: { a: 'Bonjour {nom}', c: '', d: 'Unclosed {name', extra: 'Extra' } })
  for (const problem of ['missing translation', 'unknown key', 'empty translation', 'expected placeholders', 'Unterminated closing brace']) {
    assert.ok(errors.some(error => error.includes(problem)), `${problem}: ${errors}`)
  }
})

test('browser locales honor preference order, scripts and regional variants', () => {
  const cases: [string[], string][] = [
    [['zh-TW'], 'zh-TW'], [['zh-HK'], 'zh-HK'], [['zh-MO'], 'zh-MO'], [['zh-Hant'], 'zh-TW'],
    [['zh-Hant-CN'], 'zh-TW'], [['zh-Hant-HK'], 'zh-HK'], [['zh-Hant-MO'], 'zh-MO'],
    [['zh-Hans-TW'], 'zh-CN'], [['zh-Hans-HK'], 'zh-CN'], [['zh-Hans-MO'], 'zh-CN'], [['zh-Hans'], 'zh-CN'], [['zh'], 'zh-CN'],
    [['zh-CN'], 'zh-CN'], [['zh-SG'], 'zh-SG'], [['zh-MY'], 'zh-MY'], [['zh-Hans-SG'], 'zh-SG'],
    [['ZH-hant-hk'], 'zh-HK'], [['zh-Hant-SG'], 'zh-TW'], [['zh-u-ca-chinese'], 'zh-CN'],
    [['zh-Hant-MY'], 'zh-TW'],
    [['pt-PT'], 'pt-PT'], [['pt-BR'], 'pt-BR'], [['pt-AO'], 'pt-BR'], [['pt'], 'pt-BR'],
    [['es-MX'], 'es-MX'], [['es-419'], 'es'], [['de-AT'], 'de'], [['de-CH'], 'de-CH'], [['ja-JP'], 'ja'], [['fr-CA'], 'fr'], [['ru-RU'], 'ru'],
    [['ko'], 'ko'], [['ko-KR'], 'ko'], [['ko-KP'], 'ko'], [['ko-CN'], 'ko'],
    [['ko-KR', 'fr-FR', 'en-US'], 'ko'], [['fr-FR', 'ko-KR'], 'fr'],
    [['en-GB', 'zh-TW'], 'en-GB'], [['en-001'], 'en'], [['en-150'], 'en'], [['en-AU'], 'en'], [['zh-TW', 'zh-CN'], 'zh-TW'], [['zh-MY', 'zh-CN'], 'zh-MY'],
    [['en-XX'], 'en'], [['fr-XX'], 'fr'],
    [['it-IT', 'ko-KR'], 'ko'], [['it-IT', 'nl-NL'], 'en'], [[], 'en'],
    [['invalid_locale', 'zh-HK'], 'zh-HK'],
  ]
  for (const [preferences, expected] of cases) assert.equal(browserLanguage(preferences), expected, String(preferences))
  for (const language of languages) {
    assert.equal(isLanguagePreference(language.value), true)
    assert.equal(browserLanguage([language.value]), language.value)
  }
  for (const value of ['zh-Hant', 'zh-Hans', 'fr-FR', 'ko-KR', 'en-001', 'en-150', 'es-419', 'fr-CA', 'pt-AO', 'ko-KP', 'en-XX', 'en-US-extra', 'browser', 'unknown', undefined]) assert.equal(isLanguagePreference(value), false)
  assert.equal(isLanguagePreference('auto'), true)
})

test('plural forms retain counts and select Russian one, few and many forms', () => {
  const i18n = createI18n({ legacy: false, locale: 'ru', fallbackLocale: 'en', messages, pluralRules })
  for (const [count, ending] of [[0, 'файлов'], [1, 'файл'], [2, 'файла'], [5, 'файлов'], [11, 'файлов'], [21, 'файл'], [22, 'файла'], [111, 'файлов']] as const) {
    const label = i18n.global.t('resourceStorageCount', { count })
    if (count === 0) assert.equal(label, 'Нет файлов')
    else assert.equal(label, `${count} ${ending}`)
    assert.ok(i18n.global.t('resourceStorageFreed', { count, size: '2 MiB' }).includes('2 MiB'))
  }
  i18n.global.locale.value = 'en'
  assert.equal(i18n.global.t('resourceStorageCount', { count: 0 }), '0 files')
  assert.equal(i18n.global.t('resourceStorageCount', { count: 1 }), '1 file')
  assert.equal(i18n.global.t('resourceStorageCount', { count: 2 }), '2 files')
  for (const locale of ['zh-CN', 'zh-TW', 'zh-HK', 'zh-MO', 'zh-SG', 'zh-MY', 'ja', 'ko'] as const) {
    i18n.global.locale.value = locale
    assert.ok(i18n.global.t('resourceStorageCount', { count: 3 }).includes('3'))
  }
})
