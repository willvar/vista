import { regions } from './regions.ts'

export type Catalog = 'en' | 'en-GB' | 'zh-CN' | 'zh-TW' | 'zh-HK' | 'zh-MO' | 'zh-SG' | 'zh-MY'
  | 'es' | 'es-419' | 'de' | 'de-CH' | 'ja' | 'ko' | 'fr' | 'pt-BR' | 'pt-PT' | 'ru'

// Region choices represent wording variants and also select local formatting.
const groups = [
  { value: 'en', language: 'en', label: 'English', reply: 'English', aliases: '英语 英語 英文 English' },
  { value: 'zh-CN', language: 'zh-Hans', label: '简体中文', reply: 'Simplified Chinese', aliases: '简体中文 簡體中文 简中 簡中 Chinese Simplified zh-Hans 中国大陆 中國大陸' },
  { value: 'zh-TW', language: 'zh-Hant', label: '繁體中文', reply: 'Traditional Chinese', aliases: '繁體中文 繁体中文 繁中 正體中文 正体中文 Chinese Traditional zh-Hant Taiwan 台湾 台灣' },
  { value: 'es', language: 'es', label: 'Español', reply: 'Spanish', aliases: 'Spanish 西班牙语 西班牙語' },
  { value: 'de', language: 'de', label: 'Deutsch', reply: 'German', aliases: 'German 德语 德語' },
  { value: 'ja', language: 'ja', label: '日本語', reply: 'Japanese', aliases: 'Japanese 日语 日語 日文' },
  { value: 'ko', language: 'ko', label: '한국어', reply: 'Korean', aliases: 'Korean 한국어 조선말 조선어 韩语 韓語 韩国语 韓國語 朝鲜语 朝鮮語 ko-KR ko-KP' },
  { value: 'fr', language: 'fr', label: 'Français', reply: 'French', aliases: 'French 法语 法語' },
  { value: 'pt-BR', language: 'pt', label: 'Português', reply: 'Portuguese', aliases: 'Portuguese Brasil 巴西葡萄牙语 巴西葡萄牙語 葡语 葡語' },
  { value: 'ru', language: 'ru', label: 'Русский', reply: 'Russian', aliases: 'Russian 俄语 俄語 俄文' },
] as const

type Group = typeof groups[number]
export type Language = Group['value'] | `${Group['language']}-${string}` | 'zh-HK' | 'zh-MO' | 'zh-SG' | 'zh-MY'
export type LanguagePreference = 'auto' | Language

function catalogFor(group: Group, region: string): Catalog {
  switch (group.language) {
    case 'en': return region === 'GB' ? 'en-GB' : 'en'
    case 'pt': return region === 'BR' ? 'pt-BR' : 'pt-PT'
    case 'de': return region === 'CH' ? 'de-CH' : 'de'
    case 'es': return region === 'MX' ? 'es-419' : 'es'
    case 'zh-Hans': return region === 'SG' ? 'zh-SG' : region === 'MY' ? 'zh-MY' : 'zh-CN'
    case 'zh-Hant': return region === 'HK' ? 'zh-HK' : region === 'MO' ? 'zh-MO' : 'zh-TW'
    default: return group.value
  }
}

export const languages = groups.flatMap(group => regions[group.language].map((region, index) => {
  const prefix = group.language.startsWith('zh-') ? 'zh' : group.language
  const value: Language = index === 0 ? group.value : `${prefix}-${region}` as Language
  return { ...group, value, region, catalog: catalogFor(group, region) }
}))

export function languageInfo(value: string) {
  return languages.find(language => language.value === value) ?? languages[0]!
}

export function isLanguagePreference(value: unknown): value is LanguagePreference {
  return value === 'auto' || languages.some(language => language.value === value)
}

export function browserLanguage(preferences: readonly string[]): Language {
  for (const preference of preferences) {
    let parsed: Intl.Locale
    try { parsed = new Intl.Locale(preference) } catch { continue }
    const { language: base, script, region } = parsed
    // An explicit Chinese script takes priority over the region.
    const writing = base === 'zh'
      ? script === 'Hant' || (!script && ['TW', 'HK', 'MO'].includes(region ?? '')) ? 'zh-Hant' : 'zh-Hans'
      : base
    const variants = languages.filter(language => language.language === writing)
    if (variants.length) return variants.find(language => language.region === region)?.value ?? variants[0]!.value
  }
  return 'en'
}
