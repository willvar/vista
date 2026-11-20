import { test, expect } from './fixtures'

const original = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGPQDmj8DwADogH8oe96jAAAAABJRU5ErkJggg==','base64')

test.use({ imageModel: true })

test('home draft and its original survive another tab and send without creating an early session', { tag:'@responsive' }, async ({ page, context, vista }) => {
  await page.goto(vista.origin)
  const sender=page.locator('.sender-card textarea')
  await sender.fill('Keep this before sending')
  await expect(page.getByText('Draft saved',{exact:true})).toBeVisible()
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
  await expect(page.getByText('Draft saved',{exact:true})).toBeVisible()
  expect((await vista.api('/drafts/home')).text).toBe('Legacy home draft')
  expect(await page.evaluate(() => localStorage.getItem('vista-home-draft'))).toBeNull()
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
    expect((await vista.api('/drafts/home')).revision).toBe(0)
    await page.unroute(patch)
    if (concurrent) await vista.api('/drafts/home', { method: 'PUT', body: JSON.stringify({ revision: 0, text: 'Newer saved version', images: [] }) })
    await page.reload()
    await expect(textarea).toHaveValue(input)
    if (concurrent) {
      await expect(page.getByText('Another tab changed the saved draft. Your edits are still here.')).toBeVisible()
      expect(await vista.api('/drafts/home')).toMatchObject({ revision: 1, text: 'Newer saved version' })
    } else {
      await expect(page.getByText('Draft saved', { exact: true })).toBeVisible()
      expect(await vista.api('/drafts/home')).toMatchObject({ revision: 1, text: input })
    }
    expect(await vista.api('/sessions')).toEqual([])
  })
}
