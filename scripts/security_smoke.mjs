import { execFileSync } from 'node:child_process'
import { request } from 'node:http'
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { availableVistaPort } from './test_port.mjs'
import { prepareVistaTestConfig } from './test_config.mjs'

// Exercise the real loopback HTTP entry point without opening a browser.
const binary = process.env.VISTA_BIN || join(process.cwd(), 'bin', 'vista')
const parent = join(process.cwd(), '.vista', 'test-state')
mkdirSync(parent, { recursive: true })
const state = mkdtempSync(join(parent, 'security-'))
prepareVistaTestConfig(state)
const env = { ...process.env, VISTA_PORT: await availableVistaPort(), XDG_CONFIG_HOME: state, TMPDIR: state, VISTA_NO_BROWSER: '1' }
const cli = (...args) => execFileSync(binary, args, { env, encoding: 'utf8' }).trim()
let started = false
try {
  const address = cli('start')
  started = true
  const url = new URL(address)
  if (url.hostname !== '127.0.0.1' || url.port !== env.VISTA_PORT || url.hash) throw new Error(`unexpected listen address: ${url}`)
  function call(method, path, headers = {}, body = '') {
    return new Promise((resolve, reject) => {
      const req = request({ hostname: url.hostname, port: url.port, path, method, headers }, response => {
        let content = ''
        response.on('data', chunk => { content += chunk })
        response.on('end', () => resolve({ status: response.statusCode, headers: response.headers, content }))
      })
      req.on('error', reject)
      req.end(body)
    })
  }
  const preferenceBody = JSON.stringify({ lastMode: 'auto' })
  const preflight = await call('OPTIONS', '/api/preferences', { Host: 'vista.example', Origin: 'https://client.example', 'Access-Control-Request-Method': 'PATCH', 'Access-Control-Request-Headers': 'content-type, x-vista-language, x-custom' })
  const checks = [
    ['status without token', await call('GET', '/api/status'), 200],
    ['domain Host', await call('GET', '/api/status', { Host: 'vista.example' }), 200],
    ['LAN Host', await call('GET', '/api/status', { Host: '192.0.2.1:5186' }), 200],
    ['cross-origin write', await call('PATCH', '/api/preferences', { Origin: 'https://client.example', 'Content-Type': 'application/json' }, preferenceBody), 200],
    ['cross-port write', await call('PATCH', '/api/preferences', { Origin: 'http://127.0.0.1:1', 'Content-Type': 'application/json' }, preferenceBody), 200],
    ['opaque-origin write', await call('PATCH', '/api/preferences', { Origin: 'null', 'Content-Type': 'application/json' }, preferenceBody), 200],
    ['preflight', preflight, 204],
    ['write without token', await call('PATCH', '/api/preferences', { Origin: url.origin, 'Content-Type': 'application/json' }, preferenceBody), 200],
    ['oversized Session request', await call('POST', '/api/sessions', { Origin: url.origin, 'Content-Type': 'application/json' }, JSON.stringify({ title: 'safe', padding: 'x'.repeat(5000) })), 400],
  ]
  if (preflight.headers['access-control-allow-origin'] !== 'https://client.example' || preflight.headers['access-control-allow-credentials'] !== 'true' || preflight.headers['access-control-allow-methods'] !== 'PATCH' || preflight.headers['access-control-allow-headers'] !== 'content-type, x-vista-language, x-custom') throw new Error('preflight did not allow the requested origin, credentials, method and headers')
  for (const [name, result, expected] of checks) {
    if (result.status !== expected) throw new Error(`${name}: expected ${expected}, got ${result.status}: ${result.content.slice(0, 200)}`)
  }
  const preferences = await call('GET', '/api/preferences')
  if (preferences.status !== 200 || JSON.parse(preferences.content).lastMode !== 'auto') throw new Error('token-free settings write did not persist')
  const stillRunning = await call('GET', '/api/status')
  if (stillRunning.status !== 200 || !JSON.parse(stillRunning.content).sampledAt || JSON.parse(stillRunning.content).sessions.total !== 0) throw new Error('HTTP access checks changed the session list or stopped Vista')
  console.log('Explicit loopback port, token-free API, open Host/Origin, CORS preflight, and request size checks passed')
} finally {
  if (started) try { cli('stop') } catch {}
  rmSync(state, { recursive: true, force: true })
}
