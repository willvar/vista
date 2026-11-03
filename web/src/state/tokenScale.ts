type TokenReference = {
  id: string
  tokens: number
  label: string
}

// Fixed, rounded text estimates, not model tokenizer measurements.
// Editions, source counts and the conversion convention: docs/usage-statistics.md.
const page: TokenReference = {
  id: 'page', tokens: 1_000, label: 'statisticsScalePage',
}
const chinese: readonly TokenReference[] = [
  page,
  { id: 'kong-yiji', tokens: 5_200, label: 'statisticsScaleKongYiji' },
  { id: 'ah-q', tokens: 43_000, label: 'statisticsScaleAhQ' },
  { id: 'red-chamber', tokens: 1_700_000, label: 'statisticsScaleRedChamber' },
  { id: 'china-encyclopedia', tokens: 120_000_000, label: 'statisticsScaleChinaEncyclopedia' },
]
const international: readonly TokenReference[] = [
  page,
  { id: 'magi', tokens: 3_900, label: 'statisticsScaleMagi' },
  { id: 'alice', tokens: 53_000, label: 'statisticsScaleAlice' },
  { id: 'war-and-peace', tokens: 1_100_000, label: 'statisticsScaleWarAndPeace' },
  { id: 'britannica', tokens: 88_000_000, label: 'statisticsScaleBritannica' },
]

export function tokenScale(tokens: number, locale: string) {
  if (!Number.isFinite(tokens) || tokens <= 0) return null
  const references = /^zh(?:-|$)/i.test(locale) ? chinese : international
  let reference = page
  for (const item of references) {
    if (tokens < item.tokens) break
    reference = item
  }
  const lessThanPage = tokens < page.tokens / 10
  const count = lessThanPage ? 0.1 : Number((tokens / reference.tokens).toFixed(1))
  return {
    reference,
    count,
    label: lessThanPage ? 'statisticsScaleLessThanPage' : reference.label,
    formattedCount: new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(count),
  }
}
