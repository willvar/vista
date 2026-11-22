import { test, expect } from './fixtures'

const original = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGPQDmj8DwADogH8oe96jAAAAABJRU5ErkJggg==','base64')

test.use({ imageModel: true })

test('home draft and its original survive another tab and send without creating an early session', { tag:'@responsive' }, async ({ page, context, vista }) => {
  await page.goto(vista.origin)
  const sender=page.locator('.sender-card textarea')
  let started!: () => void
  let release!: () => void
  const writing = new Promise<void>(resolve => { started = resolve })
  const held = new Promise<void>(resolve => { release = resolve })
  await page.route('**/api/drafts/home', async route => {
    if (route.request().method() === 'PUT') { started(); await held }
    await route.continue()
  })
  const feedback = page.getByText(/^(Loading draft…|Draft not saved|Saving draft…|Draft saved)$/)
  try {
    await sender.fill('Keep this before sending')
    await expect(feedback).toHaveCount(0)
    await writing
    await expect(feedback).toHaveCount(0)
  } finally { release() }
  await expect.poll(async () => (await vista.api('/drafts/home')).text).toBe('Keep this before sending')
  await expect(feedback).toHaveCount(0)
  await expect(page.locator('.draft-sync-notice')).toHaveCount(0)
  await page.locator('.sender-card input[type=file]').setInputFiles({name:'fixture.png', mimeType:'image/png', buffer:original})
  await expect.poll(async () => (await vista.api('/drafts/home')).images.length).toBe(1)
  expect(await vista.api('/sessions')).toEqual([])
  const tab=await context.newPage()
  await tab.goto(vista.origin)
  await expect(tab.locator('.sender-card textarea')).toHaveValue('Keep this before sending')
  await expect(tab.locator('.sender-attachment img')).toHaveCount(1)
  await expect.poll(() => tab.locator('.sender-attachment img').evaluate((i: HTMLImageElement) => i.naturalWidth)).toBe(1)
  await tab.locator('.composer-send').click()
  await expect(tab.locator('article.user')).toContainText('Keep this before sending')
  await expect(tab.locator('article.assistant')).toContainText('Mock reply: Hi')
  const saved=await vista.api('/drafts/home')
  expect(saved.text).toBe('')
  expect(saved.images).toEqual([])
  const [session]=await vista.api('/sessions')
  const [message]=await vista.api(`/sessions/${session.id}/messages`)
  const image=await tab.request.get(`${vista.origin}/api/sessions/${session.id}/messages/${message.id}/images/0`)
  expect(image.status()).toBe(200)
  expect(await image.body()).toEqual(original)
  await expect(sender).toHaveValue('')
})


test('a browser draft migrates only into an empty server draft', async ({ page, context, vista }) => {
  await page.addInitScript(() => {
    if (!sessionStorage.getItem('draft-fixture-seeded')) {
      localStorage.setItem('vista-home-draft','Legacy home draft')
      sessionStorage.setItem('draft-fixture-seeded','yes')
    }
  })
  await page.goto(vista.origin)
  await expect.poll(async () => (await vista.api('/drafts/home')).text).toBe('Legacy home draft')
  await expect.poll(() => page.evaluate(() => localStorage.getItem('vista-home-draft'))).toBeNull()
  await page.evaluate(() => localStorage.setItem('vista-home-draft','Do not overwrite the server'))
  const tab=await context.newPage()
  await tab.goto(vista.origin)
  await expect(tab.locator('.sender-card textarea')).toHaveValue('Legacy home draft')
  expect((await vista.api('/drafts/home')).text).toBe('Legacy home draft')
})

for (const concurrent of [false, true]) {
  test(`reload preserves unacknowledged draft text ${concurrent ? 'without overwriting another tab' : 'and resumes saving it'}`, async ({ page, vista }) => {
    const patch = '**/api/drafts/home'
    await page.route(patch, route => route.request().method() === 'PUT' ? route.fulfill({ status: 503 }) : route.continue())
    await page.goto(vista.origin)
    const textarea = page.locator('.sender-card textarea')
    const input = 'Only this tab has my unacknowledged text'
    const rejected = page.waitForResponse(response => response.url().endsWith('/api/drafts/home') && response.status() === 503)
    await textarea.fill(input)
    await rejected
    await expect(page.getByText('Could not save the draft. Your changes are still in this tab.')).toBeVisible()
    expect((await vista.api('/drafts/home')).revision).toBe(0)
    await page.unroute(patch)
    if (concurrent) await vista.api('/drafts/home', { method: 'PUT', body: JSON.stringify({ revision: 0, text: 'Newer saved version', images: [] }) })
    await page.reload()
    await expect(textarea).toHaveValue(input)
    if (concurrent) {
      await expect(page.getByText('Another tab changed the saved draft. Your edits are still here.')).toBeVisible()
      expect(await vista.api('/drafts/home')).toMatchObject({ revision: 1, text: 'Newer saved version' })
    } else {
      await expect.poll(() => vista.api('/drafts/home')).toMatchObject({ revision: 1, text: input })
      await expect(page.locator('.draft-sync-notice')).toHaveCount(0)
    }
    expect(await vista.api('/sessions')).toEqual([])
  })
}

test('draft retry reads a saved draft after a load failure and saves only after an edit', async ({ page, vista }) => {
  const path = '/drafts/home'
  const route = '**/api' + path
  const initial = 'An existing server draft'
  await vista.api(path, { method: 'PUT', body: JSON.stringify({ revision: 0, text: initial, images: [] }) })
  await page.route(route, request => request.fulfill({ status: 503 }))
  await page.goto(vista.origin)
  const composer = page.locator('.sender-card')
  const retry = composer.getByRole('button', { name: 'Retry', exact: true })
  await expect(composer.getByText('Could not load the saved draft.')).toBeVisible()
  await page.unroute(route)
  await retry.click()
  await expect(composer.locator('textarea')).toHaveValue(initial)
  await expect(retry).toHaveCount(0)
  expect(await vista.api(path)).toMatchObject({ revision: 1, text: initial })

  await page.route(route, request => request.request().method() === 'PUT' ? request.fulfill({ status: 503 }) : request.continue())
  await composer.locator('textarea').fill('Still in this tab')
  await expect(composer.getByText('Could not save the draft. Your changes are still in this tab.')).toBeVisible()
  expect(await vista.api(path)).toMatchObject({ revision: 1, text: initial })
  await page.unroute(route)
  await retry.click()
  await expect.poll(() => vista.api(path)).toMatchObject({ revision: 2, text: 'Still in this tab' })
  await expect(retry).toHaveCount(0)
})

for (const reading of [true, false]) {
  test(reading ? 'sidebar return does not wait for an old draft read' : 'sidebar return keeps text while its save is pending', { tag: '@responsive' }, async ({ page, vista }) => {
    const a = await vista.api('/sessions', { method: 'POST', body: '{}' })
    const b = await vista.api('/sessions', { method: 'POST', body: '{}' })
    const path = `/drafts/${a.id}`
    const before = 'Previously loaded text'
    const after = 'The latest text draft'
    const put = (revision: number, text: string) => vista.api(path, { method: 'PUT', body: JSON.stringify({ revision, text, images: [] }) })
    await put(0, before)
    const subscribed = page.waitForResponse(response => response.ok() && response.url().endsWith('/api/events/subscriptions')
      && response.request().postDataJSON()?.topics?.includes('drafts'))
    await page.goto(`${vista.origin}/sessions/${a.id}`)
    const input = page.locator('.sender-card textarea')
    await expect(input).toHaveValue(before)
    await subscribed

    let entered!: () => void
    let release!: () => void
    const started = new Promise<void>(resolve => { entered = resolve })
    const hold = new Promise<void>(resolve => { release = resolve })
    let held = false
    await page.route(`**/api${path}`, async route => {
      if (!held && route.request().method() === (reading ? 'GET' : 'PUT')) {
        held = true
        entered()
        await hold
      }
      await route.continue().catch(() => { /* The previous view may have canceled its read. */ })
    })
    async function switchTo(id: string) {
      const drawer = page.locator('.mobile-sessions-trigger')
      if (await drawer.isVisible()) await drawer.click()
      await page.locator(`.session-card[data-session-id="${id}"]`).click()
      await expect(page).toHaveURL(`${vista.origin}/sessions/${id}`)
    }
    try {
      if (reading) await put(1, after)
      else await input.fill(after)
      await started
      await switchTo(b.id)
      await expect(input).toHaveValue('')
      await switchTo(a.id)
      await expect(input).toHaveValue(after, { timeout: 5_000 })
    } finally { release() }
    await expect.poll(() => vista.api(path)).toMatchObject({ revision: 2, text: after })
  })
}
