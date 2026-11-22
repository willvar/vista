import { test, expect } from './fixtures'

test.use({ replyWithPrompt:true })

test('Profiles isolate setup and two active conversations across tabs', { tag: '@responsive' }, async ({ page, vista }, info) => {
  const call = async (base: string, path: string, body?: object, method = body ? 'POST' : 'GET') => {
    const response = await page.request.fetch(vista.origin + base + '/api' + path, { method, data: body })
    if (!response.ok()) throw Error(`${method} ${path}: ${response.status()} ${await response.text()}`)
    const text = await response.text()
    return text ? JSON.parse(text) : null
  }
  const session = await call('', '/sessions', { title: 'Original history' })
  await page.goto(vista.origin + `/sessions/${session.id}`)
  if (info.project.name !== 'desktop') await page.getByRole('button', { name: 'Open sessions', exact: true }).click()
  const navigation = page.locator('.sidebar-navigation')
  const trigger = navigation.getByRole('button', { name: 'Profiles', exact: true })
  await expect(trigger).toHaveText('Default Profile')
  const manageBox = (await navigation.locator('.manage-sessions-link').boundingBox())!
  const profileBox = (await trigger.boundingBox())!
  const settingsBox = (await navigation.getByRole('button', { name: 'Settings', exact: true }).boundingBox())!
  expect(manageBox.y + manageBox.height).toBeLessThanOrEqual(profileBox.y + 1)
  expect(profileBox.x + profileBox.width).toBeLessThanOrEqual(settingsBox.x + 1)
  expect(Math.abs(profileBox.y - settingsBox.y)).toBeLessThan(1)
  await trigger.click()
  const picker = page.getByRole('dialog', { name: 'Profiles', exact: true })
  await expect(picker.locator('.profile-current')).toContainText('Default Profile')
  // The positioner parks the panel above the viewport until it measures the trigger.
  const viewportHeight = page.viewportSize()!.height
  await expect.poll(async () => {
    const box = await picker.boundingBox()
    return box !== null && box.y >= -1 && box.y + box.height <= viewportHeight + 1
  }, { message: 'Profile switcher fits after positioning' }).toBe(true)
  await page.keyboard.press('Escape')
  await expect(picker).toBeHidden()
  await expect(trigger).toHaveAttribute('aria-expanded', 'false')
  await trigger.click()
  await picker.getByRole('button', { name: 'Manage profiles', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'Manage profiles', exact: true })
  await dialog.getByRole('button', { name: 'New Profile' }).click()
  await dialog.getByRole('textbox', { name: 'Profile name' }).fill('Research')
  await dialog.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(dialog.locator('.profiles-list')).toContainText('Research')
  await dialog.getByRole('button', { name: 'Close', exact: true }).click()
  await expect(dialog).toBeHidden()
  await trigger.click()
  const link = picker.getByRole('link', { name: 'Research', exact: true })
  const prefix = (await link.getAttribute('href'))!.replace(/\/$/, '')
  expect(prefix).toMatch(/^\/profiles\/[0-9a-f-]+$/)
  const [second] = await Promise.all([
    page.waitForEvent('popup'), picker.getByRole('link', { name: 'Open in new tab', exact: true }).click(),
  ])
  await expect(second).toHaveURL(vista.origin + prefix + '/')
  expect(await call(prefix,'/sessions')).toEqual([])
  expect((await call(prefix,'/models')).models).toEqual([])
  await call(prefix,'/provider-config',{name:'Research model',provider:'openai-chat',providerId:'custom',baseUrl:vista.modelEndpoint,credentialMode:'bearer',apiKey:'test-key',ids:[vista.modelId]})
  await call('', '/agent-settings', {automaticTitles:false}, 'PUT')
  await call(prefix, '/agent-settings', {automaticTitles:false}, 'PUT')
  const other = await call(prefix,'/sessions',{title:'Research history'})
  await second.goto(vista.origin + prefix + `/sessions/${other.id}`)
  const release = vista.pauseModelReplies(true)
  const id = prefix.split('/').at(-1)
  try {
    await Promise.all([call('',`/sessions/${session.id}/messages`,{content:'Answer from default'}),call(prefix,`/sessions/${other.id}/messages`,{content:'Answer from Research'})])
    await expect.poll(async () => [(await call('', '/status')).sessions.running,(await call(prefix, '/status')).sessions.running]).toEqual([1,1])
    expect((await page.request.patch(`${vista.origin}/api/profiles/${id}`, {data:{archived:true}})).status()).toBe(409)
    expect((await page.request.get(`${vista.origin}${prefix}/api/sessions/${session.id}/checkpoint`)).status()).toBe(404)
    expect((await page.request.get(`${vista.origin}/api/sessions/${other.id}/checkpoint`)).status()).toBe(404)
    expect((await page.request.patch(`${vista.origin}/api/profiles/default`, {data:{archived:true}})).status()).toBe(409)
  } finally { release() }
  await expect(second.locator('article.assistant').filter({ hasText: 'Answer from Research' })).toHaveCount(1)
  await expect.poll(async () => (await call(prefix,'/status')).sessions.running).toBe(0)
  await expect.poll(async () => (await call('','/status')).sessions.running).toBe(0)
  const config = await call('', '/memory-config')
  await call('', '/memory-config', { ...config, concurrentRuns: 1 }, 'PUT')
  expect((await call(prefix, '/memory-config')).concurrentRuns).toBe(1)
  const releaseLast = vista.pauseModelReplies(true)
  try {
    const results = await Promise.all([['', session.id], [prefix, other.id]].map(([address, sessionID]) => page.request.post(
      `${vista.origin}${address}/api/sessions/${sessionID}/messages`, { data: { content: 'Compete for the last execution slot' } },
    )))
    expect(results.map(response => response.status()).sort()).toEqual([202,503])
    expect(await results.find(response => response.status() === 503)!.text()).toContain('concurrent execution limit reached')
  } finally { releaseLast() }
  await expect.poll(async () => (await call('', '/status')).sessions.running + (await call(prefix, '/status')).sessions.running).toBe(0)
  await call('',`/profiles/${id}`,{name:'Writing'},'PATCH')
  await call('',`/profiles/${id}`,{archived:true},'PATCH')
  expect((await page.request.get(vista.origin+prefix+'/')).status()).toBe(410)
  await call('',`/profiles/${id}`,{archived:false},'PATCH')
  await second.reload()
  await expect(second.locator('article.assistant').filter({ hasText: 'Answer from Research' })).toHaveCount(1)
  expect(page.url()).toBe(vista.origin + `/sessions/${session.id}`)
  await trigger.click()
  await picker.getByRole('link', { name: 'Writing', exact: true }).click()
  await expect(page).toHaveURL(vista.origin + prefix + '/')
})
