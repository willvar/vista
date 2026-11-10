import en from './locales/en.ts'
import zhCN from './locales/zh-CN.ts'
import zhTW from './locales/zh-TW.ts'
import zhHK from './locales/zh-HK.ts'
import zhMO from './locales/zh-MO.ts'
import zhSG from './locales/zh-SG.ts'
import zhMY from './locales/zh-MY.ts'
import es from './locales/es.ts'
import de from './locales/de.ts'
import ja from './locales/ja.ts'
import ko from './locales/ko.ts'
import fr from './locales/fr.ts'
import ptBR from './locales/pt-BR.ts'
import ru from './locales/ru.ts'
import { languages, type Catalog, type Language } from './languages.ts'
import { regionalOverrides } from './regional-overrides.ts'

export const catalogs = {
  en, 'en-GB': { ...en, ...regionalOverrides['en-GB'] },
  'zh-CN': zhCN, 'zh-TW': zhTW, 'zh-HK': zhHK, 'zh-MO': zhMO, 'zh-SG': zhSG, 'zh-MY': zhMY,
  es, 'es-419': { ...es, ...regionalOverrides['es-419'] },
  de, 'de-CH': { ...de, ...regionalOverrides['de-CH'] }, ja, ko, fr,
  'pt-BR': ptBR, 'pt-PT': { ...ptBR, ...regionalOverrides['pt-PT'] }, ru,
} satisfies Record<Catalog, Record<keyof typeof en, string>>

// Region aliases reference the same dictionaries, not copies of their strings.
export const messages = Object.fromEntries(languages.map(language => [language.value, catalogs[language.catalog]])) as Record<Language, Record<keyof typeof en, string>>

export { pluralRules } from './plurals.ts'
