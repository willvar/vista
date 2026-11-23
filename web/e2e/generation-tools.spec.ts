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
const advertised = [
  { id: 'gateway-chat', name: 'Gateway Chat', input_modalities: ['text'] },
  { id: 'fixture-image', name: 'Fixture Canvas', type: 'image', endpoints: ['/images/generations'] },
  { id: 'fixture-video', name: 'Fixture Clip', type: 'video', input_modalities: ['text', 'image'], api: 'xai-video' },
  { id: 'fixture-decider', name: 'Fixture Judge', type: 'decision', output_modalities: ['text'], endpoints: ['/systemone'] },
  { id: 'fixture-unknown', name: 'Unconfirmed Motion', type: 'video', endpoints: ['/videos/generations'] },
]
async function connect(vista: VistaFixture, endpoint: string, name = 'Existing test gateway', account = 'account-a') {
  const settings = await vista.api('/provider-config', { method: 'POST', body: JSON.stringify({
    name, provider: 'openai-responses', providerId: 'custom',
    baseUrl: endpoint + '/gateway', credentialMode: 'x-api-key', apiKey: testKey, headers: { 'X-Fixture-Account': account },
  }) })
  expect(JSON.stringify(settings)).not.toContain(testKey)
  const connection = settings.models.find((row: { name?: string }) => row.name === name).connectionId as string
  await expect.poll(async () => (await vista.api('/generation-models')).filter((row: { connection: string }) => row.connection === connection).length).toBe(3)
  return connection
}
async function bind(vista: VistaFixture, kind: string, connection: string, model: string) {
  const settings = await vista.api('/model-config')
  return vista.api('/model-config', { method: 'PATCH', body: JSON.stringify({ revision: settings.settingsRevision, generation: { kind, model, connection } }) })
}

test.use({ replyWithPrompt: true, imageModel: true })

test('chooses classified media and decision models by name from saved connections', async ({ page, vista }) => {
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
    if (path === '/gateway/models') return res.end(JSON.stringify({ data: advertised }))
    if (path === '/gateway/images/generations') {
      // Reject an incorrect saved binding or unsupported image fields.
      if (req.method !== 'POST' || !hasSavedAuth || body?.model !== 'fixture-image' || body?.n !== 1 ||
        typeof body?.prompt !== 'string' || !body.prompt.trim() || Object.keys(body).sort().join(',') !== 'model,n,prompt') {
        return res.writeHead(400).end(JSON.stringify({ error: 'Image request does not match the provider contract' }))
      }
      return res.end(JSON.stringify({ data: [{ b64_json: png.toString('base64') }] }))
    }
    if (path === '/gateway/videos/generations') return res.end(JSON.stringify({ request_id: 'job-saved' }))
    if (path === '/gateway/videos/job-saved') return res.end(JSON.stringify(ready ? { status: 'done', video: { url: '/gateway/video-resource' } } : { status: 'pending' }))
    if (path === '/gateway/video-resource') { res.setHeader('content-type', 'video/mp4'); return res.end(mp4) }
    if (path === '/gateway/systemone') return res.end(JSON.stringify({ answers: { ready: { type: 'noul', noul: 0.97 } } }))
    res.writeHead(404).end()
  })
  try {
    const endpoint = await waitListening(fixture)
    const connection = await connect(vista, endpoint)
    await connect(vista, endpoint, 'Other test gateway', 'account-b')
    const roster = await vista.api('/generation-models')
    expect(roster.filter((row: { connection: string }) => row.connection === connection).map((row: { model: string }) => row.model)).toEqual(['fixture-image', 'fixture-video', 'fixture-decider'])
    expect(JSON.stringify(roster)).not.toMatch(/"adapter"|fixture-unknown|"apiKey"|"baseUrl"/)
    expect((await vista.api('/models')).models.map((row: { id: string }) => row.id)).not.toContain('fixture-decider')
    expect((await vista.api('/models')).models.map((row: { id: string }) => row.id)).not.toContain('fixture-image')
    await page.goto(`${vista.origin}/settings/models`)
    await expect(page.locator('.generation-settings').getByRole('combobox')).toHaveCount(4)
    for (const [purpose, name, id] of [['image', 'Fixture Canvas', 'fixture-image'], ['video', 'Fixture Clip', 'fixture-video'], ['decision', 'Fixture Judge', 'fixture-decider']] as const) {
      const row = page.locator(`[data-generation-purpose="${purpose}"]`)
      const picker = row.getByRole('combobox')
      await expect(picker).toBeEnabled()
      await picker.click()
      await picker.fill(name)
      const popup = page.locator('.ui-select-dropdown:visible')
      await expect(popup.getByRole('option', { name, exact: true })).toHaveCount(2)
      const option = popup.getByRole('group', { name: 'Existing test gateway', exact: true }).getByRole('option', { name, exact: true })
      await expect(page.locator('.generation-fields, .generation-settings input[list], .generation-settings input[type=password]')).toHaveCount(0)
      const save = page.waitForRequest(r => r.method() === 'PATCH' && new URL(r.url()).pathname === '/api/model-config')
      await option.click()
      expect((await save).postDataJSON().generation).toEqual({ kind: purpose, connection, model: id })
      await expect(row.locator('.ui-select-selection-item')).toHaveText(`Existing test gateway · ${name}`)
    }
    const settings = await vista.api('/model-config')
    expect(settings.generation.map((row: { connection: string; adapter: string }) => [row.connection, row.adapter])).toEqual([[connection, 'openai'], [connection, 'xai'], [connection, 'openrouter']])
    expect(JSON.stringify(settings)).not.toContain(testKey)
    await closeSettings(page)
    const sender = page.locator('.sender-card textarea')
    await expect(page.locator('.composer-model')).toHaveCount(0)
    await sender.fill('Fixture image')
    await sender.press('Control+Enter')
    const image = page.locator('[data-generation-kind="image"]')
    await expect(image).toHaveAttribute('data-generation-status', 'succeeded')
    const imageURL = await image.locator('img').getAttribute('src')
    const hash = imageURL?.split('/').at(-1)
    expect(hash).toMatch(/^[a-f0-9]{64}$/)
    const sessionId = new URL(page.url()).pathname.split('/').at(-1)!
    const original = await page.request.get(vista.origin + imageURL)
    expect(original.status()).toBe(200)
    expect(await original.body()).toEqual(png)
    expect(requests.filter(r => r.path === '/gateway/images/generations')).toHaveLength(1)
    await sender.fill(`Fixture video ${hash}`)
    await sender.press('Control+Enter')
    const video = page.locator('[data-generation-kind="video"]')
    await expect(video).toHaveAttribute('data-generation-status', 'started')
    await sender.fill('Can we continue while the clip is queued?')
    await sender.press('Control+Enter')
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
    await sender.press('Control+Enter')
    await expect.poll(() => requests.some(r => r.path === '/gateway/systemone')).toBe(true)
    expect(requests.filter(r => ['/gateway/images/generations', '/gateway/videos/generations', '/gateway/systemone'].includes(r.path)).every(r => r.hasSavedAuth)).toBe(true)
    await page.reload()
    await expect(video).toHaveAttribute('data-generation-status', 'succeeded')
    const outsider = await vista.api('/sessions', { method: 'POST', body: '{}' })
    expect(outsider.id).not.toBe(sessionId)
    expect((await page.request.get(`${vista.origin}/api/sessions/${outsider.id}/resources/${hash}`)).status()).toBe(404)
    const current = await vista.api('/model-config')
    const account = current.models.find((row: { connectionId?: string }) => row.connectionId === connection)
    await vista.api('/provider-config', { method: 'PATCH', body: JSON.stringify({ key: account.key, disabled: true, revision: current.settingsRevision }) })
    await page.goto(`${vista.origin}/settings/models?session=${sessionId}`)
    const unavailable = page.locator('[data-generation-purpose="image"] .generation-legacy')
    await expect(unavailable).toContainText('Existing test gateway')
    expect((await vista.api('/generation-models')).some((row: { connection: string }) => row.connection === connection)).toBe(false)
    await unavailable.getByRole('button', { name: 'Clear binding', exact: true }).click()
    await expect(unavailable).toHaveCount(0)
    expect((await vista.api('/model-config')).generation.map((row: { kind: string }) => row.kind)).toEqual(['video', 'decision'])
  } finally { fixture.closeAllConnections(); await new Promise<void>(r => fixture.close(() => r())) }
})

for (const stop of ['media', 'execution'] as const) {
  test(`stops a pending ${stop} without resubmitting or declaring upstream cancellation`, async ({ page, vista }) => {
    let posts = 0
    const fixture = createServer((req, res) => {
      res.setHeader('content-type', 'application/json')
      if (req.url === '/gateway/videos/generations' && req.method === 'POST') { posts++; return res.end('{"request_id":"still-pending"}') }
      if (req.url === '/gateway/videos/still-pending') return res.end('{"status":"pending"}')
      if (req.url === '/gateway/models') return res.end(JSON.stringify({ data: advertised }))
      res.writeHead(404).end()
    })
    try {
      await bind(vista, 'video', await connect(vista, await waitListening(fixture)), 'fixture-video')
      await page.goto(vista.origin)
      const sender = page.locator('.sender-card textarea')
      await sender.fill('Fixture video')
      await sender.press('Control+Enter')
      const card = page.locator('[data-generation-kind="video"]')
      await expect(card).toHaveAttribute('data-generation-status', 'started')
      await expect.poll(() => posts).toBe(1)
      if (stop === 'media') {
        await sender.fill('Fixture stop generation')
        await sender.press('Control+Enter')
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

  test('explains missing choices and selects a media-only model after connecting once', async ({ page, vista }) => {
    await page.goto(`${vista.origin}/settings/models`)
    const image = page.locator('[data-generation-purpose="image"]')
    await expect(image.getByText('Connect a compatible service first.')).toBeVisible()
    await expect(image.getByRole('combobox')).toBeDisabled()
    await page.getByRole('button', { name: 'Connect provider', exact: true }).click()
    const editor = page.locator('.provider-connection-modal')
    await editor.getByRole('combobox', { name: 'Provider', exact: true }).click()
    await page.getByRole('option', { name: 'DeepInfra', exact: true }).click()
    await editor.getByLabel('Base URL', { exact: true }).fill('http://127.0.0.1:9')
    await editor.locator('input[type=password]').fill(testKey)
    await editor.getByRole('button', { name: 'Connect provider', exact: true }).click()
    await expect(editor).toBeHidden()
    const settings = await vista.api('/model-config')
    expect(settings.default).toBe('')
    expect(settings.models).toHaveLength(1)
    expect((await vista.api('/models')).models).toEqual([])
    await expect(image.getByRole('combobox')).toBeEnabled()
    await image.getByRole('combobox').click()
    await page.locator('.ui-select-dropdown:visible').getByRole('option', { name: 'FLUX.2 Pro', exact: true }).click()
    await expect(image.locator('.ui-select-selection-item')).toHaveText('DeepInfra · FLUX.2 Pro')
    const saved = await vista.api('/model-config')
    expect(saved.generation).toMatchObject([{ kind: 'image', connection: settings.models[0].connectionId, model: 'black-forest-labs/FLUX-2-pro' }])
    expect(JSON.stringify(saved)).not.toContain(testKey)

    const fallbackID = 'black-forest-labs/FLUX-1-schnell'
    await image.getByRole('combobox').click()
    await page.locator('.ui-select-dropdown:visible').getByRole('option', { name: fallbackID, exact: true }).click()
    await expect(image.locator('.ui-select-selection-item')).toHaveText(`DeepInfra · ${fallbackID}`)
    expect((await vista.api('/model-config')).generation).toMatchObject([{ kind: 'image', connection: settings.models[0].connectionId, model: fallbackID }])
  })
})

test('discovers a running ONNX model only in classifications and reuses its local connection', async ({ page, vista }) => {
  let decisions = 0
  const fixture = createServer(async (request, response) => {
    response.setHeader('Content-Type', 'application/json')
    expect(request.headers.authorization).toBeUndefined()
    if (request.method === 'GET' && request.url === '/v1/models') return response.end(JSON.stringify({ data: [{ id: 'fixture-onnx', name: 'Local Fixture', type: 'decision' }] }))
    if (request.method === 'POST' && request.url === '/v1/systemone') {
      decisions++
      const chunks: Buffer[] = []
      for await (const chunk of request) chunks.push(chunk as Buffer)
      expect(JSON.parse(Buffer.concat(chunks).toString())).toMatchObject({ model: 'fixture-onnx', questions: { ready: { type: 'noul' } } })
      return response.end(JSON.stringify({ answers: { ready: { type: 'noul', noul: 0.9 } } }))
    }
    response.writeHead(404).end()
  })
  try {
    const baseUrl = (await waitListening(fixture)) + '/v1'
    await page.goto(`${vista.origin}/settings/models`)
    await page.getByRole('button', { name: 'Connect provider', exact: true }).click()
    const editor = page.locator('.provider-connection-modal')
    await editor.getByRole('combobox', { name: 'Provider', exact: true }).click()
    await page.getByRole('option', { name: 'ONNX (local)', exact: true }).click()
    await editor.getByLabel('Base URL', { exact: true }).fill(baseUrl)
    await expect(editor.locator('input[type=password]')).toHaveCount(0)
    await editor.getByRole('button', { name: 'Connect provider', exact: true }).click()
    await expect(editor).toBeHidden()
    const row = page.locator('[data-generation-purpose="decision"]')
    await expect(row.getByRole('combobox')).toBeEnabled()
    await row.getByRole('combobox').click()
    await page.locator('.ui-select-dropdown:visible').getByRole('option', { name: 'Local Fixture', exact: true }).click()
    await expect(row.locator('.ui-select-selection-item')).toHaveText('ONNX (local) · Local Fixture')
    expect((await vista.api('/models')).models.map((item: { id: string }) => item.id)).not.toContain('fixture-onnx')
    await closeSettings(page)
    const sender = page.locator('.sender-card textarea')
    await sender.fill('Fixture decision')
    await sender.press('Control+Enter')
    await expect(page.locator('article.assistant .markdown').last()).toContainText('Fixture result recorded.')
    expect(decisions).toBe(1)
  } finally {
    fixture.closeAllConnections()
    await new Promise<void>(done => fixture.close(() => done()))
  }
})
