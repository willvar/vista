import type { StreamEvent } from '@vista/client'

export type GenerationCard = {
  key: string; runId: string; source: number; kind: 'image' | 'video'
  status: 'started' | 'succeeded' | 'failed' | 'stopped' | 'canceled'
  hash?: string
}

export function generationCards(events: readonly StreamEvent[]): Map<number, GenerationCard[]> {
  const cards = new Map<string, GenerationCard>()
  const ended = new Set<string>()
  for (const event of events) {
    const data = event.data
    if (event.kind === 'run_finished' && data?.runId) ended.add(data.runId)
    if (event.kind !== 'tool' || !data?.runId || !data.tool?.callId || !data.tool.sourceSequence) continue
    const tool = data.tool
    const kind = tool.name === 'generate_image' ? 'image' : tool.name === 'generate_video' ? 'video' : undefined
    if (!kind) continue
    const key = `${data.runId}/${tool.callId}`
    const previous = cards.get(key)
    if (previous && previous.status !== 'started' && data.status === 'started') continue
    const hash = /^[a-f0-9]{64}$/.test(tool.generated?.hash || '') ? String(tool.generated.hash) : undefined
    const status = data.status === 'started' ? 'started' : data.status === 'succeeded' && hash ? 'succeeded' : tool.mediaState === 'canceled' ? 'canceled' : tool.mediaState === 'stopped' ? 'stopped' : 'failed'
    cards.set(key, { key, kind, runId: data.runId, source: tool.sourceSequence, status, hash })
  }
  const grouped = new Map<number, GenerationCard[]>()
  for (const card of cards.values()) {
    if (card.status === 'started' && ended.has(card.runId)) card.status = 'stopped'
    const row = grouped.get(card.source) || []
    row.push(card)
    grouped.set(card.source, row)
  }
  return grouped
}
