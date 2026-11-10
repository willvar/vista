import { createI18n } from 'vue-i18n'
import { loadLocaleMessages } from './i18n/loaders.ts'
import { pluralRules } from './i18n/plurals.ts'
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

export const i18n = createI18n({ legacy: false, locale: 'en', fallbackLocale: 'en', messages: {}, pluralRules })
let languageRevision = 0

async function applyLanguage(preference: LanguagePreference, persist: boolean) {
  const revision = ++languageRevision
  // Save the choice before loading its catalog so a reload can retry that choice
  // even while the download is pending or has failed.
  if (persist) {
    try { localStorage.setItem(storageKey, preference) } catch { /* The selection still works for this page. */ }
    document.cookie = `${storageKey}=${encodeURIComponent(preference)}; Path=/; Max-Age=31536000; SameSite=Lax`
  }
  const language = effectiveLanguage(preference)
  const [fallback, messages] = await Promise.all([loadLocaleMessages('en'), loadLocaleMessages(language)])
  if (revision !== languageRevision) return
  i18n.global.setLocaleMessage('en', fallback)
  i18n.global.setLocaleMessage(language, messages)
  i18n.global.locale.value = language
  document.documentElement.lang = language
}

export function initializeLanguage() { return applyLanguage(readLanguagePreference(), false) }
export function setLanguagePreference(preference: LanguagePreference) { return applyLanguage(preference, true) }

window.addEventListener('languagechange', () => {
  if (readLanguagePreference() === 'auto') void initializeLanguage().catch(error => window.dispatchEvent(new CustomEvent('vista:asset-error', { detail: error })))
})
