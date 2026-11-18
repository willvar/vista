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
async function bind(vista: VistaFixture, kind: string, adapter: string, endpoint: string, model: string) {
  const settings = await vista.api('/model-config')
  return vista.api('/model-config', { method: 'PATCH', body: JSON.stringify({ revision: settings.settingsRevision,
    generation: { kind, adapter, model, baseUrl: endpoint, apiKey: 'media-test-key' },
  }) })
}

test.use({ replyWithPrompt: true, imageModel: true })

test('uses Settings bindings, preserves slow media across steering and finds originals by ID', async ({ page, vista }) => {
  const requests: { path: string; body?: any }[] = []
  let ready = false
  const fixture = createServer(async (req, res) => {
    const path = req.url || ''
    const chunks: Buffer[] = []
    for await (const part of req) chunks.push(Buffer.from(part as Uint8Array))
    const body = chunks.length ? JSON.parse(Buffer.concat(chunks).toString()) : undefined
    requests.push({ path, body })
    res.setHeader('content-type', 'application/json')
    if (path === '/images/generations') return res.end(JSON.stringify({ data: [{ b64_json: png.toString('base64') }] }))
    if (path === '/videos/generations') return res.end(JSON.stringify({ request_id: 'job-saved' }))
    if (path === '/videos/job-saved') return res.end(JSON.stringify(ready ? { status: 'done', video: { url: '/video-resource' } } : { status: 'pending' }))
    if (path === '/video-resource') { res.setHeader('content-type', 'video/mp4'); return res.end(mp4) }
    if (path === '/systemone') return res.end(JSON.stringify({ answers: { ready: { type: 'noul', noul: 0.97 } } }))
    res.writeHead(404).end()
  })
  try {
    const endpoint = await waitListening(fixture)
    await bind(vista, 'video', 'xai', endpoint, 'fixture-video')
    await bind(vista, 'decision', 'openrouter', endpoint, 'fixture-decider')
    await page.goto(`${vista.origin}/settings/models`)
    await page.getByRole('button', {name:'Generate images', exact: true}).click()
    const form = page.locator('.generation-fields').locator('xpath=ancestor::*[@role="dialog"][1]')
    await form.getByRole('combobox', { name: 'Provider', exact:true }).selectOption('xai')
    await form.getByLabel('Model', { exact:true }).fill('fixture-image')
    await form.getByLabel('Base URL', { exact:true }).fill(endpoint)
    await form.getByLabel('API Key', { exact:true }).fill('media-test-key')
    await form.getByRole('button', { name:'Save changes', exact:true }).click()
    await expect(form).toBeHidden()
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
    const submissions = requests.filter(r => r.path === '/videos/generations')
    expect(submissions).toHaveLength(1)
    expect(submissions[0]?.body.image.url).toBe('data:image/png;base64,' + png.toString('base64'))
    await sender.fill('Fixture decision')
    await sender.press('Enter')
    await expect.poll(() => requests.some(r => r.path === '/systemone')).toBe(true)
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
      if (req.url === '/videos/generations' && req.method === 'POST') { posts++; return res.end('{"request_id":"still-pending"}') }
      if (req.url === '/videos/still-pending') return res.end('{"status":"pending"}')
      res.writeHead(404).end()
    })
    try {
      await bind(vista, 'video', 'xai', await waitListening(fixture), 'fixture-video')
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
