import assert from 'node:assert/strict'
import test from 'node:test'
import { createI18n } from 'vue-i18n'
import { messages, pluralRules } from '../i18n/catalogs.ts'
import { tokenScale } from './tokenScale.ts'

test('zero and invalid totals have no analogy; tiny positive totals never round to zero', () => {
  for (const value of [0, -1, NaN, Infinity]) assert.equal(tokenScale(value, 'en'), null)
  for (const value of [1, 50, 99]) {
    const scale = tokenScale(value, 'en')!
    assert.equal(scale.label, 'statisticsScaleLessThanPage')
    assert.equal(scale.formattedCount, '0.1')
  }
  assert.equal(tokenScale(100, 'en')!.label, 'statisticsScalePage')
  assert.equal(tokenScale(100, 'en')!.count, 0.1)
  assert.equal(tokenScale(1_000, 'en')!.count, 1)
})

test('references change at fixed text capacities instead of indefinitely counting pages', () => {
  for (const [locale, steps] of [
    ['zh-CN', [[1_000, 'page'], [5_200, 'kong-yiji'], [43_000, 'ah-q'], [1_700_000, 'red-chamber'], [120_000_000, 'china-encyclopedia']]],
    ['en', [[1_000, 'page'], [3_900, 'magi'], [53_000, 'alice'], [1_100_000, 'war-and-peace'], [88_000_000, 'britannica']]],
  ] as const) {
    for (const [index, [tokens, id]] of steps.entries()) {
      const scale = tokenScale(tokens, locale)!
      assert.equal(scale.reference.id, id)
      assert.equal(scale.count, 1)
      if (index) assert.equal(tokenScale(tokens - 1, locale)!.reference.id, steps[index - 1]![1])
    }
  }
  assert.equal(tokenScale(52_000, 'zh-CN')!.count, 1.2)
  assert.equal(tokenScale(52_000, 'zh-CN')!.reference.id, 'ah-q')
  assert.equal(tokenScale(88_000_000_000, 'en')!.count, 1_000)
  assert.ok(Number.isFinite(tokenScale(Number.MAX_SAFE_INTEGER, 'en')!.count))
})

test('Chinese variants share Chinese reference texts; other locales share fixed English editions', () => {
  for (const locale of ['zh-CN', 'zh-TW', 'zh-HK', 'zh-MO', 'zh-SG', 'zh-MY', 'zh', 'ZH-hant']) {
    assert.equal(tokenScale(52_000, locale)!.reference.id, 'ah-q')
  }
  for (const locale of ['en', 'en-GB', 'de-CH', 'es-MX', 'pt-PT', 'ja', 'ko', 'fr', 'ru', 'it']) {
    assert.equal(tokenScale(53_000, locale)!.reference.id, 'alice')
  }
  assert.equal(tokenScale(79_500, 'de')!.formattedCount, '1,5')
})

test('all supported locales render each analogy and localized numbers', () => {
  for (const locale of Object.keys(messages)) {
    const i18n = createI18n({ legacy: false, locale, messages, pluralRules })
    for (const tokens of [1, 50, 100, 1_000, 5_200, 43_000, 53_000, 1_700_000, 120_000_000, 240_000_000]) {
      const scale = tokenScale(tokens, locale)!
      const label = i18n.global.t(scale.label, { count: scale.formattedCount }, scale.count)
      assert.ok(label.includes(scale.formattedCount), `${locale}: ${label}`)
      assert.ok(!label.includes('statisticsScale'), `${locale}: ${label}`)
    }
  }
})
