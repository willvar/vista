import { languages } from './languages.ts'

function russianPlural(choice: number) {
  const n = Math.abs(choice)
  if (n === 0) return 0
  if (n % 10 === 1 && n % 100 !== 11) return 1
  if (n % 10 >= 2 && n % 10 <= 4 && (n % 100 < 12 || n % 100 > 14)) return 2
  return 3
}

export const pluralRules = Object.fromEntries(languages.filter(language => language.language === 'ru').map(language => [language.value, russianPlural]))
