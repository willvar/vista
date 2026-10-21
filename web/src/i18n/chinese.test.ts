import assert from 'node:assert/strict'
import test from 'node:test'
import { convertMessage, generateChineseCatalogs } from '../../scripts/generate-chinese.ts'
import { messages } from './catalogs.ts'

test('static Chinese regional catalogs match the Simplified source and reviewed overrides', () => {
  for (const [locale, catalog] of Object.entries(generateChineseCatalogs())) {
    assert.deepEqual(messages[locale as keyof typeof messages], catalog, `${locale}: run npm run generate:i18n`)
    assert.doesNotMatch(catalog.mcpTokenHint, /頭髮/)
  }
  assert.equal(messages['zh-TW'].settings, '設定')
  assert.equal(messages['zh-HK'].settings, '設置')
  assert.equal(messages['zh-TW'].reset, '重設')
})

test('Chinese generation preserves placeholders, inline code and URLs verbatim', () => {
  const text = 'before {name} `config/文件.json` https://example.com/设置?name=value after'
  assert.equal(convertMessage(text, part => part.toUpperCase()),
    'BEFORE {name} `config/文件.json` https://example.com/设置?name=value AFTER')
})
