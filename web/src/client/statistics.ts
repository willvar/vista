export type StatisticsDay = { date: string; activeMinutes: number; tokens: number; requests: number; unreported: number }
export type UsageStatistics = {
  days: StatisticsDay[]; hours: number[][]; activeMinutes: number; activeDays: number
  tokens: number; requests: number; unreported: number
  activitySince: string; usageSince: string; sampledAt: string; timezone: string; retentionDays: number; version: string
}

export function revisionAtLeast(value: string | undefined, expected: string) {
  const [epoch, revision] = value?.split(':') ?? []
  const [targetEpoch, target] = expected.split(':')
  return epoch === targetEpoch && Number.isSafeInteger(Number(revision)) && Number(revision) >= Number(target)
}

// Chart dates are calendar keys, not UTC instants. Noon avoids DST midnight
// boundaries when formatting them in the browser's timezone.
export function calendarDate(key: string) { return new Date(`${key}T12:00:00`) }

export function localDateKey() {
  const now = new Date()
  return `${now.getFullYear()}-${String(now.getMonth()+1).padStart(2,'0')}-${String(now.getDate()).padStart(2,'0')}`
}
