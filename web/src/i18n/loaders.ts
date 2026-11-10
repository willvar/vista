import { languageInfo, type Catalog, type Language } from './languages.ts'
import type en from './locales/en.ts'

type Messages = Record<keyof typeof en, string>
type Loader = () => Promise<{ default: Messages }>

const loaders: Record<Catalog, Loader> = {
  en: () => import('./locales/en.ts'),
  'en-GB': () => regional('en', 'en-GB'),
  'zh-CN': () => import('./locales/zh-CN.ts'),
  'zh-TW': () => import('./locales/zh-TW.ts'),
  'zh-HK': () => import('./locales/zh-HK.ts'),
  'zh-MO': () => import('./locales/zh-MO.ts'),
  'zh-SG': () => import('./locales/zh-SG.ts'),
  'zh-MY': () => import('./locales/zh-MY.ts'),
  es: () => import('./locales/es.ts'),
  'es-419': () => regional('es', 'es-419'),
  de: () => import('./locales/de.ts'),
  'de-CH': () => regional('de', 'de-CH'),
  ja: () => import('./locales/ja.ts'),
  ko: () => import('./locales/ko.ts'),
  fr: () => import('./locales/fr.ts'),
  'pt-BR': () => import('./locales/pt-BR.ts'),
  'pt-PT': () => regional('pt-BR', 'pt-PT'),
  ru: () => import('./locales/ru.ts'),
}
const pending = new Map<Catalog, Promise<Messages>>()

function loadCatalog(catalog: Catalog): Promise<Messages> {
  let request = pending.get(catalog)
  if (!request) {
    request = loaders[catalog]().then(module => module.default).catch(error => { pending.delete(catalog); throw error })
    pending.set(catalog, request)
  }
  return request
}

async function regional(base: Catalog, variant: 'en-GB' | 'es-419' | 'de-CH' | 'pt-PT') {
  const [messages, { regionalOverrides }] = await Promise.all([loadCatalog(base), import('./regional-overrides.ts')])
  return { default: { ...messages, ...regionalOverrides[variant] } }
}

export function loadLocaleMessages(language: Language) {
  return loadCatalog(languageInfo(language).catalog)
}
