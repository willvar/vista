// Only expose countries/regions with maintained wording variants.
// A formatting difference alone does not create another region option.
// The first region remains the default when selecting a language manually.
export const regions = {
  en: ['US', 'GB'],
  'zh-Hans': ['CN', 'SG', 'MY'],
  'zh-Hant': ['TW', 'HK', 'MO'],
  es: ['ES', 'MX'],
  de: ['DE', 'CH'],
  ja: ['JP'],
  ko: ['KR'],
  fr: ['FR'],
  pt: ['BR', 'PT'],
  ru: ['RU'],
} as const
