import type { Message, ModelInfo, Session } from '../client/types.ts'

export type FailureContext = {
  kind: 'execution' | 'compaction'
  runId: string
  error: string
  sequence?: number
  messageId?: string
  createdAt?: string
  provider?: string
  modelId?: string
  modelKey?: string
}

const encoder = new TextEncoder()
const maxPromptBytes = 8192

function excerpt(content: string, bytes: number) {
  const encoded = encoder.encode(content)
  const clipped = encoded.length > bytes
  // Streaming decode omits an incomplete trailing UTF-8 sequence.
  return { content: clipped ? new TextDecoder().decode(encoded.subarray(0, bytes), { stream: true }) : content,
    originalBytes: encoded.length, truncated: clipped }
}

export function failureAnalysisPrompt(instruction: string, session: Session, failure: FailureContext,
  messages: readonly Message[], model?: ModelInfo) {
  const recent = messages.filter(message => (!failure.sequence || message.sequence < failure.sequence)
    && !message.failure && ['user', 'assistant'].includes(message.role))
    .sort((a, b) => a.sequence - b.sequence).slice(-4)
    .map(message => ({ id: message.id, runId: message.runId, sequence: message.sequence, role: message.role,
      imageCount: message.imageCount || 0, ...excerpt(message.content, 2048) }))
  const data = {
    source: { sessionId: session.id, runId: failure.runId, kind: failure.kind,
      messageId: failure.messageId, sequence: failure.sequence, createdAt: failure.createdAt },
    model: { provider: failure.provider || model?.provider, id: failure.modelId || model?.id,
      key: failure.modelKey || model?.key, reasoningEffort: model?.reasoningEffort },
    error: excerpt(failure.error, maxPromptBytes),
    recentMessages: recent,
  }
  // These are explicitly encoded diagnostic data, not replayed model history.
  // Escape delimiters even inside tool/code excerpts so a broken upstream
  // renderer cannot interpret them as protocol or Markdown structure.
  const render = () => instruction + '\n\n```json\n'
    + JSON.stringify(data, null, 2).replace(/[<>`]/g, character => '\\u' + character.charCodeAt(0).toString(16).padStart(4, '0'))
    + '\n```'
  let prompt = render()
  while (encoder.encode(prompt).length > maxPromptBytes) {
    const longest = recent.reduce<(typeof recent)[number] | undefined>((found, item) =>
      item.content.length > (found?.content.length || 0) ? item : found, undefined)
    const field = longest?.content ? longest : data.error
    if (!field.content) throw new Error('Failure analysis metadata exceeds the message limit')
    field.content = excerpt(field.content, Math.floor(encoder.encode(field.content).length / 2)).content
    field.truncated = true
    prompt = render()
  }
  return prompt
}
