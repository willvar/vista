import { createServer, type Server } from 'node:http'
import { test, expect, type VistaFixture } from './fixtures'
import { closeSettings } from './settings-navigation'

const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aLW8AAAAASUVORK5CYII=', 'base64')
const mp4 = Buffer.from('000000186674797069736f6d0000020069736f6d6d703431', 'hex')
function waitListening(server: Server) {
  return new Promise<string>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => {
      const address = server.address()
      if (!address || typeof address === 'string') return reject(new Error('expected TCP loopback'))
      resolve(`http://127.0.0.1:${address.port}`)
    })
  })
}
const testKey = 'fixture-media-credential'
async function connect(vista: VistaFixture, endpoint: string) {
  const settings = await vista.api('/provider-config', { method: 'POST', body: JSON.stringify({
    name: 'Existing test gateway', provider: 'openai-responses', providerId: 'custom',
    baseUrl: endpoint + '/gateway', credentialMode: 'x-api-key', apiKey: testKey, headers: { 'X-Fixture-Account': 'account-a' },
  }) })
  expect(JSON.stringify(settings)).not.toContain(testKey)
  return settings.models.find((row: { name?: string }) => row.name === 'Existing test gateway').connectionId as string
}
async function bind(vista: VistaFixture, kind: string, adapter: string, connection: string, model: string) {
  const settings = await vista.api('/model-config')
  return vista.api('/model-config', { method: 'PATCH', body: JSON.stringify({ revision: settings.settingsRevision,
    generation: { kind, adapter, model, connection },
  }) })
}

test.use({ replyWithPrompt: true, imageModel: true })

test('binds media and decisions to one saved gateway without re-entering credentials', async ({ page, vista }) => {
  const requests: { path: string; hasSavedAuth: boolean; body?: any }[] = []
  let ready = false
  const fixture = createServer(async (req, res) => {
    const path = req.url || ''
    const chunks: Buffer[] = []
    for await (const part of req) chunks.push(Buffer.from(part as Uint8Array))
    const body = chunks.length ? JSON.parse(Buffer.concat(chunks).toString()) : undefined
    const hasSavedAuth = req.headers['x-api-key'] === testKey && req.headers['x-fixture-account'] === 'account-a' && !req.headers.authorization
    requests.push({ path, body, hasSavedAuth })
    res.setHeader('content-type', 'application/json')
    if (path === '/gateway/models') return res.end(JSON.stringify({ data: [{ id: 'gateway-chat', input_modalities: ['text'] }] }))
    if (path === '/gateway/images/generations') return res.end(JSON.stringify({ data: [{ b64_json: png.toString('base64') }] }))
    if (path === '/gateway/videos/generations') return res.end(JSON.stringify({ request_id: 'job-saved' }))
    if (path === '/gateway/videos/job-saved') return res.end(JSON.stringify(ready ? { status: 'done', video: { url: '/gateway/video-resource' } } : { status: 'pending' }))
    if (path === '/gateway/video-resource') { res.setHeader('content-type', 'video/mp4'); return res.end(mp4) }
    if (path === '/gateway/systemone') return res.end(JSON.stringify({ answers: { ready: { type: 'noul', noul: 0.97 } } }))
    res.writeHead(404).end()
  })
  try {
    const endpoint = await waitListening(fixture)
    const connection = await connect(vista, endpoint)
    await bind(vista, 'video', 'xai', connection, 'fixture-video')
    await bind(vista, 'decision', 'openrouter', connection, 'fixture-decider')
    await page.goto(`${vista.origin}/settings/models`)
    await page.getByRole('button', { name: 'Generate images', exact: true }).click()
    const form = page.locator('.generation-fields').locator('xpath=ancestor::*[@role="dialog"][1]')
    const connections = form.getByRole('combobox', { name: 'Service connections', exact: true })
    await expect(connections.getByRole('option', { name: 'Existing test gateway', exact: true })).toHaveCount(1)
    await expect(connections).toHaveValue('') // The fixture also has a compatible chat connection.
    await connections.selectOption(connection)
    await form.getByRole('combobox', { name: 'Media API format', exact: true }).selectOption('openai')
    await form.getByLabel('Model', { exact: true }).fill('fixture-image')
    await expect(form.locator('input[type=password], input[autocomplete=new-password]')).toHaveCount(0)
    await expect(form.getByLabel('Base URL', { exact: true })).toHaveCount(0)
    const save = page.waitForRequest(r => r.method() === 'PATCH' && new URL(r.url()).pathname === '/api/model-config')
    await form.getByRole('button', { name: 'Save changes', exact: true }).click()
    expect((await save).postDataJSON().generation).toEqual({ kind: 'image', connection, adapter: 'openai', model: 'fixture-image' })
    await expect(form).toBeHidden()
    const settings = await vista.api('/model-config')
    expect(settings.models.filter((row: { name?: string }) => row.name === 'Existing test gateway')).toHaveLength(1)
    expect(settings.generation.map((row: { connection: string }) => row.connection)).toEqual([connection, connection, connection])
    expect(JSON.stringify(settings)).not.toContain(testKey)
    await closeSettings(page)
    const sender = page.locator('.sender-card textarea')
    await expect(page.locator('.composer-model')).toHaveCount(0)
    await sender.fill('Fixture image')
    await sender.press('Enter')
    const image = page.locator('[data-generation-kind="image"]')
    await expect(image).toHaveAttribute('data-generation-status', 'succeeded')
    const imageURL = await image.locator('img').getAttribute('src')
    const hash = imageURL?.split('/').at(-1)
    expect(hash).toMatch(/^[a-f0-9]{64}$/)
    const sessionId = new URL(page.url()).pathname.split('/').at(-1)!
    const original = await page.request.get(vista.origin + imageURL)
    expect(original.status()).toBe(200)
    expect(await original.body()).toEqual(png)
    await sender.fill(`Fixture video ${hash}`)
    await sender.press('Enter')
    const video = page.locator('[data-generation-kind="video"]')
    await expect(video).toHaveAttribute('data-generation-status', 'started')
    await sender.fill('Can we continue while the clip is queued?')
    await sender.press('Enter')
    await expect(page.locator('article.assistant').filter({ hasText: 'Can we continue while the clip is queued?' })).toBeVisible()
    await expect(video).toHaveAttribute('data-generation-status', 'started')
    ready = true
    await expect(video).toHaveAttribute('data-generation-status', 'succeeded')
    const link = await video.getByRole('link').getAttribute('href')
    expect(await (await page.request.get(vista.origin + link)).body()).toEqual(mp4)
    const submissions = requests.filter(r => r.path === '/gateway/videos/generations')
    expect(submissions).toHaveLength(1)
    expect(submissions[0]?.body.image.url).toBe('data:image/png;base64,' + png.toString('base64'))
    await sender.fill('Fixture decision')
    await sender.press('Enter')
    await expect.poll(() => requests.some(r => r.path === '/gateway/systemone')).toBe(true)
    expect(requests.filter(r => ['/gateway/images/generations', '/gateway/videos/generations', '/gateway/systemone'].includes(r.path)).every(r => r.hasSavedAuth)).toBe(true)
    await page.reload()
    await expect(video).toHaveAttribute('data-generation-status', 'succeeded')
    const outsider = await vista.api('/sessions', { method: 'POST', body: '{}' })
    expect(outsider.id).not.toBe(sessionId)
    expect((await page.request.get(`${vista.origin}/api/sessions/${outsider.id}/resources/${hash}`)).status()).toBe(404)
  } finally { fixture.closeAllConnections(); await new Promise<void>(r => fixture.close(() => r())) }
})

for (const stop of ['media', 'execution'] as const) {
  test(`stops a pending ${stop} without resubmitting or declaring upstream cancellation`, async ({ page, vista }) => {
    let posts = 0
    const fixture = createServer((req, res) => {
      res.setHeader('content-type', 'application/json')
      if (req.url === '/gateway/videos/generations' && req.method === 'POST') { posts++; return res.end('{"request_id":"still-pending"}') }
      if (req.url === '/gateway/videos/still-pending') return res.end('{"status":"pending"}')
      if (req.url === '/gateway/models') return res.end(JSON.stringify({ data: [{ id: 'gateway-chat', input_modalities: ['text'] }] }))
      res.writeHead(404).end()
    })
    try {
      await bind(vista, 'video', 'xai', await connect(vista, await waitListening(fixture)), 'fixture-video')
      await page.goto(vista.origin)
      const sender = page.locator('.sender-card textarea')
      await sender.fill('Fixture video')
      await sender.press('Enter')
      const card = page.locator('[data-generation-kind="video"]')
      await expect(card).toHaveAttribute('data-generation-status', 'started')
      await expect.poll(() => posts).toBe(1)
      if (stop === 'media') {
        await sender.fill('Fixture stop generation')
        await sender.press('Enter')
        await expect(page.locator('article.assistant').filter({ hasText: 'Fixture stop processed.' })).toBeVisible()
      } else await page.getByRole('button', { name: 'Stop current execution', exact: true }).click()
      await expect(card).toHaveAttribute('data-generation-status', 'stopped')
      expect(posts).toBe(1)
      await expect(card.getByRole('link')).toHaveCount(0)
    } finally { fixture.closeAllConnections(); await new Promise<void>(r => fixture.close(() => r())) }
  })
}

test.describe('without an existing provider', () => {
  test.use({ configuredProvider: false })

  test('adds a media-only service in the common connection editor and reuses it', async ({ page, vista }) => {
    await page.goto(`${vista.origin}/settings/models`)
    await page.getByRole('button', { name: 'Generate images', exact: true }).click()
    const fields = page.locator('.generation-fields')
    await expect(fields.getByText('Connect a compatible service first.')).toBeVisible()
    await fields.getByRole('button', { name: 'Connect provider', exact: true }).click()
    const editor = page.locator('.provider-connection-modal')
    await editor.getByRole('combobox', { name: 'Provider', exact: true }).click()
    await page.getByRole('option', { name: 'fal', exact: true }).click()
    await editor.getByLabel('Base URL', { exact: true }).fill('http://127.0.0.1:9')
    await editor.locator('input[type=password]').fill(testKey)
    await editor.getByRole('button', { name: 'Connect provider', exact: true }).click()
    await expect(editor).toBeHidden()
    const settings = await vista.api('/model-config')
    expect(settings.default).toBe('')
    expect(settings.models).toHaveLength(1)
    expect((await vista.api('/models')).models).toEqual([])
    const connection = settings.models[0].connectionId
    for (const [label, model] of [['Generate images', 'fixture-image'], ['Generate videos', 'fixture-video']] as const) {
      await page.getByRole('button', { name: label, exact: true }).click()
      const form = fields.locator('xpath=ancestor::*[@role="dialog"][1]')
      await expect(form.getByRole('combobox', { name: 'Service connections', exact: true })).toHaveValue(connection)
      await expect(form.locator('input[type=password]')).toHaveCount(0)
      await form.getByLabel('Model', { exact: true }).fill(model)
      await form.getByRole('button', { name: 'Save changes', exact: true }).click()
      await expect(form).toBeHidden()
    }
    const saved = await vista.api('/model-config')
    expect(saved.models).toHaveLength(1)
    expect(saved.generation.map((row: { connection: string }) => row.connection)).toEqual([connection, connection])
    expect(JSON.stringify(saved)).not.toContain(testKey)
  })
})
