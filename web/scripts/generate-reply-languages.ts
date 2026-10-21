import { readFileSync, writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { languages } from '../src/i18n/languages.ts'

// Generate the backend whitelist from the same registry as the UI, offline.
export function replyLanguageNames() {
  const regions = new Intl.DisplayNames(['en'], { type: 'region', style: 'short' })
  const defaults: Record<string, string> = {
    en: 'English', 'zh-CN': 'Simplified Chinese', es: 'Spanish', de: 'German', ja: 'Japanese', ko: 'Korean', fr: 'French', ru: 'Russian',
    'pt-BR': 'Brazilian Portuguese', 'en-GB': 'British English', 'pt-PT': 'European Portuguese', 'de-CH': 'Swiss Standard German',
  }
  return Object.fromEntries(languages.map(language => [language.value,
    defaults[language.value] ?? `${language.reply} (${language.value === 'zh-MO' ? 'Macau' : regions.of(language.region)})`,
  ]))
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const path = new URL('../../internal/workerproto/language_names.json', import.meta.url)
  const expected = JSON.stringify(replyLanguageNames(), null, 2) + '\n'
  if (process.argv.includes('--check')) {
    let current = ''
    try { current = readFileSync(path, 'utf8') } catch { /* Missing output is stale. */ }
    if (current !== expected) throw new Error('Reply language registry is stale; run npm run generate:i18n')
  } else {
    writeFileSync(path, expected)
  }
}
