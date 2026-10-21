import { createI18n } from 'vue-i18n'
import { messages, pluralRules } from './i18n/catalogs.ts'
import { browserLanguage, isLanguagePreference, type LanguagePreference } from './i18n/languages.ts'

export { browserLanguage, type LanguagePreference } from './i18n/languages.ts'
const storageKey = 'vista-language'

export function readLanguagePreference(): LanguagePreference {
  const cookie = document.cookie.split('; ').find(item => item.startsWith(storageKey + '='))?.slice(storageKey.length + 1)
  if (isLanguagePreference(cookie)) return cookie
  try {
    const saved = localStorage.getItem(storageKey)
    if (isLanguagePreference(saved)) return saved
  } catch { /* Use the client preference when storage is unavailable. */ }
  return 'auto'
}

function effectiveLanguage(preference: LanguagePreference) {
  return preference === 'auto' ? browserLanguage(navigator.languages?.length ? navigator.languages : [navigator.language]) : preference
}

export const i18n = createI18n({ legacy: false, locale: effectiveLanguage(readLanguagePreference()), fallbackLocale: 'en', messages, pluralRules })
document.documentElement.lang = i18n.global.locale.value

export function setLanguagePreference(preference: LanguagePreference) {
  const language = effectiveLanguage(preference)
  i18n.global.locale.value = language
  document.documentElement.lang = language
  try { localStorage.setItem(storageKey, preference) } catch { /* The selection still works for this page. */ }
  document.cookie = `${storageKey}=${encodeURIComponent(preference)}; Path=/; Max-Age=31536000; SameSite=Lax`
}

window.addEventListener('languagechange', () => {
  if (readLanguagePreference() === 'auto') {
    const language = effectiveLanguage('auto')
    i18n.global.locale.value = language
    document.documentElement.lang = language
  }
})
