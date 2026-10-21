const timeOptions: Intl.DateTimeFormatOptions = {
  hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23',
}

function format(utc: string, locale: string, options: Intl.DateTimeFormatOptions) {
  const date = new Date(utc)
  return Number.isNaN(date.getTime()) ? '—' : new Intl.DateTimeFormat(locale, options).format(date)
}

export function formatDateTime(utc: string, locale: string) {
  return format(utc, locale, { year: 'numeric', month: '2-digit', day: '2-digit', ...timeOptions })
}

export function formatTime(utc: string, locale: string) {
  return format(utc, locale, timeOptions)
}
