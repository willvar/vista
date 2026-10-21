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
        response.on('end', () => resolve({ status: response.statusCode, content }))
      })
      req.on('error', reject)
      req.end(body)
    })
  }
  const checks = [
    ['status without token', await call('GET', '/api/status'), 200],
    ['foreign Host', await call('GET', '/api/status', { Host: 'attacker.example' }), 403],
    ['foreign Origin', await call('POST', '/api/stop', { Origin: 'https://attacker.example' }), 403],
    ['other local Origin', await call('POST', '/api/stop', { Origin: 'http://127.0.0.1:1' }), 403],
    ['opaque Origin', await call('POST', '/api/stop', { Origin: 'null' }), 403],
    ['write without token', await call('PATCH', '/api/preferences', { Origin: url.origin, 'Content-Type': 'application/json' }, JSON.stringify({ lastMode: 'auto' })), 200],
    ['oversized Session request', await call('POST', '/api/sessions', { Origin: url.origin, 'Content-Type': 'application/json' }, JSON.stringify({ title: 'safe', padding: 'x'.repeat(5000) })), 400],
  ]
  for (const [name, result, expected] of checks) {
    if (result.status !== expected) throw new Error(`${name}: expected ${expected}, got ${result.status}: ${result.content.slice(0, 200)}`)
  }
  const preferences = await call('GET', '/api/preferences')
  if (preferences.status !== 200 || JSON.parse(preferences.content).lastMode !== 'auto') throw new Error('token-free settings write did not persist')
  const stillRunning = await call('GET', '/api/status')
  if (stillRunning.status !== 200 || !JSON.parse(stillRunning.content).sampledAt || JSON.parse(stillRunning.content).sessions.total !== 0) throw new Error('rejected requests changed Vista state')
  console.log('Explicit loopback port, token-free API, Host, Origin, and request size checks passed')
} finally {
  if (started) try { cli('stop') } catch {}
  rmSync(state, { recursive: true, force: true })
}
