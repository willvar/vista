import type { NetworkSettings, ProxyProfile } from '@vista/client'

// Typed projection: never send write-only placeholders, public credential flags,
// or a complete application configuration document back as saved credentials.
export function networkInput(config: NetworkSettings): NetworkSettings {
  return { globalProxy: config.globalProxy, profiles: config.profiles.map(profile => ({
    id: profile.id, name: profile.name, protocol: profile.protocol, server: profile.server, port: profile.port,
    authentication: profile.authentication, connectIp: profile.connectIp || '',
    ...(profile.insecureSkipVerify ? { insecureSkipVerify: true } : {}),
    ...(profile.username ? { username: profile.username } : {}), ...(profile.password ? { password: profile.password } : {}),
  })) }
}
export function newProxyProfile(): ProxyProfile {
  const id = Array.from(crypto.getRandomValues(new Uint8Array(16)), value => value.toString(16).padStart(2, '0')).join('')
  return { id, name: '', protocol: 'http', server: '', port: 80, authentication: 'none', connectIp: '' }
}

function literalIP(value: string): boolean {
  if (!value || value.includes('%')) return false
  if (value.includes(':')) { try { return new URL(`http://[${value}]/`).hostname.startsWith('[') } catch { return false } }
  const parts = value.split('.')
  return parts.length === 4 && parts.every(part => /^(?:0|[1-9][0-9]{0,2})$/.test(part) && Number(part) <= 255)
}
export function validConnectionIP(value: string): boolean {
  if (!value) return true
  if (!literalIP(value)) return false
  if (!value.includes(':')) { const first = Number(value.split('.')[0]); return value !== '0.0.0.0' && (first < 224 || first > 239) }
  const normalized = new URL(`http://[${value}]/`).hostname.toLowerCase()
  return normalized !== '[::]' && !normalized.startsWith('[ff')
}
