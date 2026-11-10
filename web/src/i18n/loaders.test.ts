import assert from 'node:assert/strict'
import test from 'node:test'
import { messages } from './catalogs.ts'
import { languages } from './languages.ts'
import { loadLocaleMessages } from './loaders.ts'

test('lazy language messages match every validated full catalog', async () => {
  for (const language of languages) {
    assert.deepEqual(await loadLocaleMessages(language.value), messages[language.value], language.value)
  }
})

test('concurrent and subsequent language loads reuse the same request and dictionary', async () => {
  const first = loadLocaleMessages('ja')
  assert.strictEqual(loadLocaleMessages('ja'), first)
  const dictionary = await first
  assert.strictEqual(await loadLocaleMessages('ja'), dictionary)
})
