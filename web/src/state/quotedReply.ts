export type QuotedText = { text: string; role: 'user' | 'assistant' }

export const quoteStoragePrefix = 'vista-session-quote:'

export function readQuotedText(sessionId: string): QuotedText | null {
  try {
    const quote = JSON.parse(localStorage.getItem(quoteStoragePrefix + sessionId) || 'null')
    return quote && typeof quote.text === 'string' && quote.text.trim()
      && (quote.role === 'user' || quote.role === 'assistant') ? { text: quote.text, role: quote.role } : null
  } catch { return null }
}

export function formatQuotedReply(text: string, quote: QuotedText | null): string {
  return quote ? quote.text.replace(/\r\n?/g, '\n').split('\n').map(line => `> ${line}`).join('\n') + '\n\n' + text : text
}
