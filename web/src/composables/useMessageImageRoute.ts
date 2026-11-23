import { useRouteLayer } from './routeLayers'

export function useMessageImageRoute() {
  return useRouteLayer('image-preview', to => to.name === 'session')
}

export function messageImageKey(messageId: string, index: number) { return `${messageId}/${index}` }

export function readMessageImageKey(key: string) {
  const parts = /^([\w-]{1,128})\/(0|[1-9]\d*)$/.exec(key)
  if (!parts) return
  const index = Number(parts[2])
  if (Number.isSafeInteger(index)) return { messageId: parts[1]!, index }
}

export function messageImagePath(sessionId: string, messageId: string, index: number) {
  return `/sessions/${encodeURIComponent(sessionId)}/messages/${encodeURIComponent(messageId)}/images/${index}`
}
