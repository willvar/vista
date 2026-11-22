import { profileStorageKey } from '../client/profileScope.ts'

export type QuoteSource = { key: string; sequence: number; createdAt: string }
export type QuotedText = { text: string; role: 'user' | 'assistant'; source?: QuoteSource }
export type QuotedExcerpt = QuotedText & { id: number }
export type QuotedReply = { items: QuotedExcerpt[]; nextId: number }

export const quoteStoragePrefix = 'vista-session-quote:'

function readText(value: unknown): QuotedText | null {
  if (!value || typeof value !== 'object') return null
  const quote = value as Record<string, unknown>
  if (typeof quote.text !== 'string' || !quote.text.trim() || quote.role !== 'user' && quote.role !== 'assistant') return null
  const source = quote.source as Partial<QuoteSource> | undefined
  return { text: quote.text, role: quote.role,
    ...(source && typeof source.key === 'string' && source.key && Number.isSafeInteger(source.sequence) && source.sequence! > 0
      && typeof source.createdAt === 'string' && Number.isFinite(Date.parse(source.createdAt))
      ? { source: { key: source.key, sequence: source.sequence!, createdAt: new Date(source.createdAt).toISOString() } } : {}) }
}

export function readQuotedReply(sessionId: string, profileId = 'default'): QuotedReply | null {
  try {
    const value = JSON.parse(localStorage.getItem(profileStorageKey(quoteStoragePrefix, profileId) + sessionId) || 'null')
    if (!value || typeof value !== 'object') return null
    // Earlier clients stored a single selection without numbering or a source.
    const legacy = readText(value)
    if (legacy) return { items: [{ ...legacy, id: 1 }], nextId: 2 }
    if (!Array.isArray(value.items)) return null
    const items: QuotedExcerpt[] = []
    let lastId = 0
    for (const item of value.items) {
      const quote = readText(item)
      if (!quote || !Number.isSafeInteger(item.id) || item.id <= lastId || item.id >= Number.MAX_SAFE_INTEGER) continue
      items.push({ ...quote, id: item.id })
      lastId = item.id
    }
    const nextId = Number.isSafeInteger(value.nextId) && value.nextId > lastId ? value.nextId : lastId + 1
    return items.length || nextId > 1 ? { items, nextId } : null
  } catch { return null }
}

// Display-only extraction of leading quote context, including older quote drafts.
// Unknown Markdown stays intact; the canonical message is never changed.
export function sentMessageBody(content: string): string {
  let body = content.replace(/\r\n?/g, '\n')
  let stripped = false
  while (body) {
    const end = body.indexOf('\n\n')
    if (end < 0) break
    const [header, ...lines] = body.slice(0, end).split('\n')
    const bare = header?.startsWith('> ')
    const quoted = bare ? [header!, ...lines] : lines
    if ((!bare && !/ · #\d+ · \d{4}-\d{2}-\d{2}T[\d:.]+Z$/.test(header || ''))
      || !quoted.length || quoted.some(line => !line.startsWith('> '))) break
    body = body.slice(end + 2)
    stripped = true
  }
  return stripped ? body : content
}

export function formatQuotedReply(text: string, reply: QuotedReply | null, label: (quote: QuotedExcerpt, number: number) => string): string {
  if (!reply?.items.length) return text
  return reply.items.map((quote, index) => {
    const source = quote.source ? ` · #${quote.source.sequence} · ${quote.source.createdAt}` : ''
    const body = quote.text.replace(/\r\n?/g, '\n').split('\n').map(line => `> ${line}`).join('\n')
    return `${quote.source ? '' : '> '}${label(quote, index + 1)}${source}\n${body}`
  }).join('\n\n') + '\n\n' + text
}
