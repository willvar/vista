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
  await picker.getByRole('button', { name: 'New Profile', exact: true }).click()
  const name = picker.getByRole('textbox', { name: 'Profile name' })
  await expect(name).toBeFocused()
  await name.fill('Research')
  await picker.getByRole('button', { name: 'Create', exact: true }).click()
  await expect(page.getByRole('dialog')).toHaveCount(1)
  await expect(picker.locator('.profile-current')).toContainText('Default Profile')
  await expect(picker.getByRole('button', { name: /^(Archive|Restore|Manage profiles)$/ })).toHaveCount(0)
  const link = picker.getByRole('link', { name: 'Research', exact: true })
  const prefix = (await link.getAttribute('href'))!.replace(/\/$/, '')
  expect(prefix).toMatch(/^\/profiles\/[0-9a-f-]+$/)
  const research = picker.locator('.profile-choice').filter({ has: page.getByRole('link', { name: 'Research', exact: true }) })
  await expect(research.getByRole('button', { name: 'Rename', exact: true })).toBeFocused()
  const [second] = await Promise.all([
    page.waitForEvent('popup'), research.getByRole('link', { name: 'Open in new tab', exact: true }).click(),
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
    expect((await page.request.patch(`${vista.origin}/api/profiles/${id}`, {data:{archived:true}})).status()).toBe(400)
    expect((await call(prefix, '/status')).sessions.running).toBe(1)
    expect((await page.request.get(`${vista.origin}${prefix}/api/sessions/${session.id}/checkpoint`)).status()).toBe(404)
    expect((await page.request.get(`${vista.origin}/api/sessions/${other.id}/checkpoint`)).status()).toBe(404)
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
  await second.reload()
  await expect(second.locator('article.assistant').filter({ hasText: 'Answer from Research' })).toHaveCount(1)
  expect(page.url()).toBe(vista.origin + `/sessions/${session.id}`)
  await trigger.click()
  await picker.getByRole('link', { name: 'Writing', exact: true }).click()
  await expect(page).toHaveURL(vista.origin + prefix + '/')
})


test('Profile controls stay reachable on short screens and long names have room', { tag: '@responsive' }, async ({ page, vista }, info) => {
  const post = (path: string, data: object) => vista.api(path, { method: 'POST', body: JSON.stringify(data) })
  const longName = 'Research and long-term planning workspace'
  const current = await post('/profiles', { name: longName })
  for (let index = 1; index <= 8; index++) await post('/profiles', { name: `Workspace ${index}` })
  const base = `/profiles/${current.id}`
  if (info.project.name === 'desktop') {
    await page.context().addCookies([{ url: vista.origin, name: 'vista-sidebar-width', value: '220' }])
  }
  // The old short-height override let these rows push the footer below the viewport.
  for (let index = 1; index <= 5; index++) {
    const response = await page.request.post(`${vista.origin}${base}/api/sessions`, { data: { title: `Fixture conversation ${index}` } })
    expect(response.ok()).toBe(true)
  }
  await page.goto(vista.origin + base + '/')
  if (info.project.name !== 'desktop') await page.getByRole('button', { name: 'Open sessions', exact: true }).click()
  const sidebar = page.locator('#sessions-panel')
  const trigger = sidebar.getByRole('button', { name: 'Profiles', exact: true })
  await expect(trigger.locator('.profile-name')).toHaveText(longName)
  await expect(sidebar.locator('.session-card')).toHaveCount(5)
  await expect(trigger).toBeInViewport({ ratio: 1 })
  await expect(sidebar.getByRole('button', { name: 'Settings', exact: true })).toBeInViewport({ ratio: 1 })
  expect((await trigger.locator('.profile-name').boundingBox())!.width).toBeGreaterThanOrEqual(90)
  if (info.project.name === 'mobile-landscape') {
    const list = sidebar.locator('ul').first()
    const before = (await trigger.boundingBox())!.y
    expect(await list.evaluate(node => node.scrollHeight > node.clientHeight)).toBe(true)
    await list.hover()
    await page.mouse.wheel(0, 400)
    await expect.poll(() => list.evaluate(node => node.scrollTop)).toBeGreaterThan(0)
    expect((await trigger.boundingBox())!.y).toBeCloseTo(before, 0)
  }
  if (info.project.name === 'desktop') await trigger.click()
  else await trigger.tap()
  const picker = page.getByRole('dialog', { name: 'Profiles', exact: true })
  await expect(picker.locator('.profile-current')).toContainText(longName)
  await expect.poll(async () => {
    const [popup, anchor] = await Promise.all([picker.boundingBox(), trigger.boundingBox()])
    return !!popup && !!anchor && popup.y >= 0 && popup.y + popup.height <= anchor.y - 2
  }, { message: 'The picker stays above its trigger without covering it' }).toBe(true)
  await expect(picker.getByRole('button', { name: 'New Profile', exact: true })).toBeInViewport({ ratio: 1 })
  const choices = picker.locator('.profile-choices')
  const last = choices.getByRole('link', { name: 'Workspace 8', exact: true })
  await expect(last).toBeVisible()
  await last.scrollIntoViewIfNeeded()
  await expect(last).toBeInViewport({ ratio: 1 })
  await expect(picker.locator('.profile-picker-header')).toBeInViewport({ ratio: 1 })
  await expect(picker.getByRole('button', { name: 'New Profile', exact: true })).toBeInViewport({ ratio: 1 })
  await page.keyboard.press('Escape')
  await expect(picker).toBeHidden()
})


test('Profile rename stays in its row and retains the input on failure', { tag: '@responsive' }, async ({ page, vista }, info) => {
  const user = await vista.api('/profiles', { method: 'POST', body: JSON.stringify({ name: 'Research' }) })
  for (let n = 0; n < 6; n++) await vista.api('/profiles', { method: 'POST', body: JSON.stringify({ name: `Workspace ${n + 1}` }) })
  await page.goto(vista.origin + '/')
  if (info.project.name !== 'desktop') await page.getByRole('button', { name: 'Open sessions', exact: true }).click()
  const trigger = page.locator('.sidebar-navigation').getByRole('button', { name: 'Profiles', exact: true })
  await trigger.click()
  const picker = page.getByRole('dialog', { name: 'Profiles', exact: true })
  const row = picker.locator(`.profile-choice[data-profile-id="${user.id}"]`)
  const rename = row.getByRole('button', { name: 'Rename', exact: true })
  let writes = 0
  await page.route(`**/api/profiles/${user.id}`, async route => {
    if (route.request().method() !== 'PATCH') return route.continue()
    if (++writes === 1) return route.fulfill({ status: 500, body: 'synthetic save failure' })
    return route.continue()
  })
  await rename.click()
  const input = row.getByRole('textbox', { name: 'Profile name' })
  await expect(input).toBeFocused()
  await expect(input).toHaveValue('Research')
  await expect(page.getByRole('dialog')).toHaveCount(1)
  await expect.poll(() => input.evaluate((element: HTMLInputElement) => element.selectionEnd! - element.selectionStart!)).toBe('Research'.length)
  await input.fill('Discard this edit')
  await page.keyboard.press('Escape')
  await expect(input).toHaveCount(0)
  await expect(picker).toBeVisible()
  await expect(rename).toBeFocused()
  expect(writes).toBe(0)
  await rename.click()
  await input.fill('Edited profile name')
  await input.press('Enter')
  await expect(row.getByRole('alert')).toHaveText('Could not save the name. Your input is still here.')
  await expect(input).toHaveValue('Edited profile name')
  // An outside click keeps the desktop editor open. On mobile it closes the
  // containing drawer; reopening the same popover must restore the draft.
  await page.mouse.click(page.viewportSize()!.width - 2, 2)
  if (info.project.name !== 'desktop') {
    await expect(picker).toBeHidden()
    await page.getByRole('button', { name: 'Open sessions', exact: true }).click()
    await trigger.click()
  }
  await expect(input).toHaveValue('Edited profile name')
  expect(writes).toBe(1)
  await row.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(row.getByRole('link', { name: 'Edited profile name', exact: true })).toBeVisible()
  await expect(rename).toBeFocused()
  expect(writes).toBe(2)
  await expect(page).toHaveURL(vista.origin + '/')
  await expect(picker.locator('[aria-current="true"]')).toContainText('Default Profile')
})

test.describe('Chinese user wording', () => {
  test.use({ locale: 'zh-CN' })
  test('the Chinese user switcher is one popover', { tag: '@responsive' }, async ({ page, vista }, info) => {
    await page.goto(vista.origin + '/')
    if (info.project.name !== 'desktop') await page.locator('.mobile-sessions-trigger').click()
    await page.locator('.sidebar-navigation').getByRole('button', { name: '用户', exact: true }).click()
    const picker = page.getByRole('dialog', { name: '用户', exact: true })
    await expect(picker.getByText('当前用户', { exact: true })).toBeVisible()
    await expect(picker).toContainText('切换时无需重新登录')
    await picker.getByRole('button', { name: '新建用户', exact: true }).click()
    const input = picker.getByRole('textbox', { name: '用户名称' })
    await expect(input).toBeFocused()
    await input.fill('日常写作')
    await input.dispatchEvent('compositionstart')
    await input.press('Enter')
    await expect(input).toHaveValue('日常写作')
    await input.dispatchEvent('compositionend')
    await picker.getByRole('button', { name: '创建', exact: true }).click()
    await expect(picker.getByRole('link', { name: '日常写作', exact: true })).toBeVisible()
    await expect(picker.locator('[aria-current="true"]')).toContainText('默认用户')
    await expect(page.getByRole('dialog')).toHaveCount(1)
  })
})
