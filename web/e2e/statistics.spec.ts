import { test, expect } from './fixtures'
import { openSettingsSection } from './settings-navigation'
import { calendarDate, type UsageStatistics } from '../src/client/statistics'

test('statistics are scoped, cached by revision, update from other clients and release reads on leaving',async({page,vista},info)=>{
  const reads:string[]=[]
  const sizes:number[]=[]
  page.on('request',request=>{if(request.method()==='GET'&&new URL(request.url()).pathname==='/api/statistics')reads.push(request.url())})
  page.on('response',response=>{if(new URL(response.url()).pathname==='/api/statistics')void response.body().then(body=>sizes.push(body.length)).catch(()=>{})})
  await page.goto(vista.origin+'/settings/general')
  await expect(page.locator('.appearance-settings-content')).toBeVisible()
  expect(reads).toHaveLength(0)
  await openSettingsSection(page,'statistics')
  const stats=page.locator('.statistics-settings')
  await expect(stats.locator('.ui-empty')).toContainText('No activity or usage recorded')
  await expect(stats.locator('.statistics-notes')).toContainText('Statistics are kept for 90 days.')
  expect(reads).toHaveLength(1)
  await openSettingsSection(page,'general')
  await expect(stats).toHaveCount(0)
  await openSettingsSection(page,'statistics')
  await expect(stats.locator('.ui-empty')).toBeVisible()
  expect(reads).toHaveLength(1)
  const minute=Math.floor(Date.now()/60_000)
  await vista.api('/activity',{method:'POST',body:JSON.stringify({minutes:[minute-10,minute-10]})})
  await expect(stats.locator('.statistics-totals dd').nth(1)).toContainText('1')
  await expect(stats.locator('.statistics-heat-cell')).toHaveCount(168)
  expect(reads).toHaveLength(2)
  await stats.getByRole('combobox',{name:'Period',exact:true}).click()
  await page.locator('.ui-select-dropdown:visible .ui-select-item-option-content').filter({hasText:/^Last 7 days$/}).click()
  await expect(stats).toHaveAttribute('aria-busy','false')
  await expect(stats.getByRole('img',{name:'Daily activity and usage',exact:true})).toBeVisible()
  expect(new URL(reads.at(-1)!).searchParams.get('days')).toBe('7')
  await expect(stats.locator('.statistics-notes')).toContainText('Statistics are kept for 90 days.')
  expect(reads).toHaveLength(3)
  await stats.locator('[data-statistics-hour="0"]').focus()
  await page.keyboard.press('ArrowRight')
  await expect(stats.locator('[data-statistics-hour="1"]')).toBeFocused()
  await expect(stats.locator('.statistics-heatmap h3')).toHaveText('Usual active hours')
  await expect(stats.locator('.statistics-heatmap .statistics-readout')).toContainText('active min (average)')
  const overflow=await stats.evaluate(element=>element.scrollWidth-element.clientWidth)
  expect(overflow).toBeLessThanOrEqual(2)
  await page.screenshot({path:info.outputPath('statistics-light.png'),animations:'disabled'})
  await page.evaluate(()=>{localStorage.setItem('vista-appearance','dark');window.dispatchEvent(new StorageEvent('storage',{key:'vista-appearance',newValue:'dark'}))})
  await page.screenshot({path:info.outputPath('statistics-dark.png'),animations:'disabled'})
  expect(sizes.every(size=>size<20_000)).toBe(true)
  await openSettingsSection(page,'general')
  const settled=reads.length
  await vista.api('/activity',{method:'POST',body:JSON.stringify({minutes:[minute-15]})})
  await page.waitForTimeout(250)
  expect(reads).toHaveLength(settled)
  await expect(page.locator('.state-sync-error')).toHaveCount(0)
})

test('trend charts preserve date selection with pointer, touch and keyboard across periods and themes @responsive',async({page,vista},info)=>{
  const errors:string[]=[]
  let reads=0
  let days:UsageStatistics['days']=[]
  page.on('pageerror',error=>errors.push(error.message))
  // Activity collection has its own coverage; keep this interaction fixture stable.
  await page.route('**/api/activity',route=>route.fulfill({status:204}))
  await page.route('**/api/statistics?*',async route=>{
    reads++
    const response=await route.fetch()
    const data:UsageStatistics=await response.json()
    days=data.days.map((day,index)=>({...day,tokens:(index+1)*10_000,requests:1,unreported:0,activeMinutes:index+1}))
    await route.fulfill({response,json:{...data,days,activeDays:days.length,activeMinutes:days.length,tokens:days.length*10_000,requests:days.length,unreported:0}})
  })
  await page.goto(vista.origin+'/settings/statistics')
  const stats=page.locator('.statistics-settings')
  const chart=stats.getByRole('img',{name:'Daily activity and usage',exact:true})
  const readout=stats.locator('.statistics-trend figcaption')
  const dateLabel=(date:string)=>new Intl.DateTimeFormat('en-US',{month:'short',day:'numeric'}).format(calendarDate(date))
  const checkDay=async(index:number)=>{
    await expect(readout.locator('strong')).toHaveText(dateLabel(days[index]!.date))
    await expect(readout).toContainText(`${days[index]!.tokens.toLocaleString('en-US')} tokens`)
    await expect(readout).toContainText(`${days[index]!.activeMinutes} active min`)
  }
  for(const period of [30,7,90]){
    if(period!==30){
      await stats.getByRole('combobox',{name:'Period',exact:true}).click()
      await page.locator('.ui-select-dropdown:visible .ui-select-item-option-content').filter({hasText:new RegExp(`^Last ${period} days$`)}).click()
    }
    await expect(stats).toHaveAttribute('aria-busy','false')
    const bars=chart.locator('rect[data-ts-key^="tokens:"]')
    await expect(bars).toHaveCount(period)
    const settled=reads
    await chart.focus()
    await page.keyboard.press('Home')
    await checkDay(0)
    await page.keyboard.press('ArrowRight')
    await checkDay(1)
    await page.keyboard.press('End')
    await checkDay(period-1)
    await page.keyboard.press('ArrowLeft')
    await checkDay(period-2)
    const selected=Math.floor(period/2)
    const bar=bars.nth(selected)
    await bar.scrollIntoViewIfNeeded()
    if(info.project.name==='desktop')await bar.hover()
    else await bar.tap()
    await checkDay(selected)
    expect(reads).toBe(settled)
    expect(await stats.evaluate(element=>element.scrollWidth-element.clientWidth)).toBeLessThanOrEqual(2)
    const box=await chart.boundingBox()
    expect(box!.x).toBeGreaterThanOrEqual(0)
    expect(box!.x+box!.width).toBeLessThanOrEqual(page.viewportSize()!.width+1)
    await page.screenshot({path:info.outputPath(`trend-${period}-light.png`),animations:'disabled'})
    await page.evaluate(()=>{localStorage.setItem('vista-appearance','dark');window.dispatchEvent(new StorageEvent('storage',{key:'vista-appearance',newValue:'dark'}))})
    await checkDay(selected)
    await page.screenshot({path:info.outputPath(`trend-${period}-dark.png`),animations:'disabled'})
    await page.evaluate(()=>{localStorage.setItem('vista-appearance','light');window.dispatchEvent(new StorageEvent('storage',{key:'vista-appearance',newValue:'light'}))})
  }
  expect(errors).toEqual([])
})

test('reported tokens include normal replies and titles once and remain after conversation deletion',async({page,vista})=>{
  await page.goto(vista.origin+'/settings/statistics')
  const stats=page.locator('.statistics-settings')
  await expect(stats.locator('.statistics-totals')).toBeVisible()
  const session=await vista.api('/sessions',{method:'POST',body:'{}'})
  await vista.api(`/sessions/${session.id}/messages`,{method:'POST',body:JSON.stringify({content:'Token accounting fixture'})})
  const total=stats.locator('.statistics-token-value')
  await expect(total).toHaveText('Less than 0.1 A4 page')
  await expect(total).toHaveAttribute('aria-label','Token usage details: 50 tokens')
  await total.focus()
  await expect(page.locator('.statistics-token-details:visible .statistics-exact-tokens')).toHaveText('50 tokens')
  await page.keyboard.press('Escape')
  await expect(page.locator('.statistics-token-details:visible')).toHaveCount(0)
  await expect(stats).toBeVisible()
  const reported=await vista.api('/statistics?days=7')
  expect(reported.tokens).toBe(50)
  expect(reported.requests).toBe(2)
  expect(reported.unreported).toBe(0)
  await expect.poll(async()=> (await vista.api(`/sessions/${session.id}/checkpoint`)).session.executing).toBe(false)
  await vista.api(`/sessions/${session.id}`,{method:'DELETE'})
  expect((await vista.api('/statistics?days=7')).tokens).toBe(50)
  await expect(total).toHaveText('Less than 0.1 A4 page')
  await expect(total).toHaveAttribute('aria-label','Token usage details: 50 tokens')
})

test('usage header reset requires confirmation and synchronizes all periods and other clients',async({page,vista})=>{
  const session=await vista.api('/sessions',{method:'POST',body:'{}'})
  await vista.api(`/sessions/${session.id}/messages`,{method:'POST',body:JSON.stringify({content:'Keep this conversation after resetting statistics'})})
  await expect.poll(async()=> (await vista.api('/statistics?days=7')).requests).toBe(2)
  await expect.poll(async()=> (await vista.api(`/sessions/${session.id}/checkpoint`)).session.executing).toBe(false)
  const messages=await vista.api(`/sessions/${session.id}/messages`)
  const minute=Math.floor(Date.now()/60_000)
  await vista.api('/activity',{method:'POST',body:JSON.stringify({minutes:[minute-10]})})
  await page.goto(vista.origin+'/settings/statistics')
  const stats=page.locator('.statistics-settings')
  const resetButton=page.locator('.ui-modal-header-actions').getByRole('button',{name:'Reset statistics',exact:true})
  await expect(resetButton).toBeVisible()
  await expect(resetButton.locator('svg')).toHaveCount(1)
  await expect(stats.locator('.statistics-token-value')).toHaveAttribute('aria-label','Token usage details: 50 tokens')
  const selectPeriod=async(days:number)=>{
    await stats.getByRole('combobox',{name:'Period',exact:true}).click()
    await page.locator('.ui-select-dropdown:visible .ui-select-item-option-content').filter({hasText:new RegExp(`^Last ${days} days$`)}).click()
    await expect(page.locator('.ui-select-dropdown:visible')).toHaveCount(0)
    await expect(stats).toHaveAttribute('aria-busy','false')
  }
  await selectPeriod(90)
  await selectPeriod(7)
  const sibling=await page.context().newPage()
  await sibling.goto(vista.origin+'/settings/statistics')
  await expect(sibling.locator('.statistics-token-value')).toHaveAttribute('aria-label','Token usage details: 50 tokens')
  await page.bringToFront()
  const mutations:string[]=[]
  const unrelated:string[]=[]
  page.on('request',request=>{
    const path=new URL(request.url()).pathname
    if(path==='/api/statistics/reset')mutations.push(request.postData()??'')
    if(['/api/sessions','/api/models','/api/status'].includes(path))unrelated.push(path)
  })
  const confirmation=page.locator('.statistics-reset-modal:visible')
  await resetButton.click()
  await expect(confirmation).toContainText('Conversations and settings are kept.')
  await confirmation.getByRole('button',{name:'Cancel',exact:true}).click()
  await expect(confirmation).toHaveCount(0)
  await expect(stats).toBeVisible()
  expect(mutations).toHaveLength(0)
  await resetButton.click()
  await page.keyboard.press('Escape')
  await expect(confirmation).toHaveCount(0)
  await expect(stats).toBeVisible()
  expect(mutations).toHaveLength(0)
  await resetButton.click()
  await confirmation.getByRole('button',{name:'Reset',exact:true}).click()
  await expect(confirmation).toHaveCount(0)
  await expect(stats.locator('.statistics-token-value')).toHaveText('0')
  await expect(stats.locator('.statistics-totals dd').nth(1)).toHaveText('0min')
  await expect(sibling.locator('.statistics-token-value')).toHaveText('0')
  await expect(sibling.locator('.statistics-totals dd').nth(1)).toHaveText('0min')
  expect(mutations.map(body=>JSON.parse(body))).toEqual([{confirm:'reset-statistics'}])
  expect(unrelated).toEqual([])
  await selectPeriod(90)
  await expect(stats.locator('.statistics-token-value')).toHaveText('0')
  await selectPeriod(30)
  await expect(stats.locator('.statistics-token-value')).toHaveText('0')
  // A pre-reset activity buffer cannot refill the cleared statistics.
  await vista.api('/activity',{method:'POST',body:JSON.stringify({minutes:[minute-10,minute]})})
  expect((await vista.api('/statistics?days=90')).activeMinutes).toBe(0)
  expect(await vista.api(`/sessions/${session.id}/messages`)).toEqual(messages)
  await page.reload()
  await expect(stats.locator('.statistics-token-value')).toHaveText('0')
  await vista.api(`/sessions/${session.id}/messages`,{method:'POST',body:JSON.stringify({content:'New usage after reset'})})
  await expect(stats.locator('.statistics-token-value')).toHaveAttribute('aria-label','Token usage details: 25 tokens')
  await expect(sibling.locator('.statistics-token-value')).toHaveAttribute('aria-label','Token usage details: 25 tokens')
  await sibling.close()
})

test('failed statistics resets retain the confirmation and data for an explicit retry',async({page,vista})=>{
  await vista.api('/activity',{method:'POST',body:JSON.stringify({minutes:[Math.floor(Date.now()/60_000)-5]})})
  await page.goto(vista.origin+'/settings/statistics')
  const stats=page.locator('.statistics-settings')
  await expect(stats.locator('.statistics-totals dd').nth(1)).toHaveText('1min')
  let attempts=0
  await page.route('**/api/statistics/reset',async route=>{
    attempts++
    if(attempts===1)await route.fulfill({status:500,body:'could not reset statistics'})
    else await route.continue()
  })
  await page.getByRole('button',{name:'Reset statistics',exact:true}).click()
  const confirmation=page.locator('.statistics-reset-modal:visible')
  await confirmation.getByRole('button',{name:'Reset',exact:true}).click()
  await expect(confirmation.getByRole('alert')).toContainText('could not reset statistics')
  await expect(stats.locator('.statistics-totals dd').nth(1)).toHaveText('1min')
  await confirmation.getByRole('button',{name:'Reset',exact:true}).click()
  await expect(confirmation).toHaveCount(0)
  await expect(stats.locator('.statistics-totals dd').nth(1)).toHaveText('0min')
  expect(attempts).toBe(2)
})

test('token comparisons change reference with scale, reveal exact totals without reads and fit small screens',async({page,vista},info)=>{
  let tokens=52_000
  let unreported=0
  let reads=0
  const errors:string[]=[]
  page.on('pageerror',error=>errors.push(error.message))
  page.on('request',request=>{if(request.method()==='GET'&&new URL(request.url()).pathname==='/api/statistics')reads++})
  await page.route('**/api/statistics?*',async route=>{
    const response=await route.fetch()
    await route.fulfill({response,json:{...await response.json(),tokens,requests:2,unreported}})
  })
  await page.evaluate(()=>{localStorage.setItem('vista-language','zh-CN');document.cookie='vista-language=zh-CN; Path=/'})
  await page.goto(vista.origin+'/settings/statistics')
  const stats=page.locator('.statistics-settings')
  const total=stats.locator('.statistics-token-value')
  const details=page.locator('.statistics-token-details:visible')
  const minute=Math.floor(Date.now()/60_000)
  for(const [index,scenario] of [
    {tokens:52_000,label:'约 1.2 部《阿Q正传》'},
    {tokens:1_700_000,label:'约 1 部《红楼梦》'},
    {tokens:120_000_000,label:'约 1 套《中国大百科全书》'},
  ].entries()){
    tokens=scenario.tokens
    if(index){
      unreported=index===2?1:0
      await vista.api('/activity',{method:'POST',body:JSON.stringify({minutes:[minute-index-10]})})
    }
    await expect(total).toHaveText(scenario.label)
    const beforeDetails=reads
    if(info.project.name==='desktop')await total.hover()
    else await total.tap()
    await expect(total).toHaveAttribute('aria-expanded','true')
    // A closing overlay can stay visible during its leave animation.
    await page.waitForTimeout(350)
    await expect(details).toBeVisible()
    await expect(details.locator('.statistics-exact-tokens')).toHaveText(`${tokens.toLocaleString('zh-CN')} 词元`)
    await expect(details.locator('p')).toHaveText(['这是累计处理量的粗略类比，不代表生成的文字长度或费用。'])
    await expect(details.locator('a')).toHaveCount(0)
    if(unreported)await expect(stats.locator('.statistics-notes')).toContainText('1 次调用未上报用量')
    const popupBox=await details.boundingBox()
    expect(popupBox!.x).toBeGreaterThanOrEqual(0)
    expect(popupBox!.x+popupBox!.width).toBeLessThanOrEqual(page.viewportSize()!.width)
    expect(await stats.evaluate(element=>element.scrollWidth-element.clientWidth)).toBeLessThanOrEqual(2)
    expect(reads).toBe(beforeDetails)
    await page.screenshot({path:info.outputPath(`token-scale-${index}.png`),animations:'disabled'})
    await page.keyboard.press('Escape')
    await expect(details).toHaveCount(0)
    await expect(stats).toBeVisible()
    await page.mouse.move(0,0)
  }
  await total.evaluate(element=>(element as HTMLElement).blur())
  await page.keyboard.press('Tab')
  await total.focus()
  await expect(total).toHaveAttribute('aria-expanded','true')
  await expect(details.locator('.statistics-exact-tokens')).toHaveText('120,000,000 词元')
  await page.keyboard.press('Escape')
  await expect(details).toHaveCount(0)
  expect(errors).toEqual([])
})

test('only real visible input creates activity reports, with no idle heartbeat or detailed interaction data',async({page,vista},info)=>{
  test.skip(info.project.name!=='desktop','Input collection is independent of viewport')
  const bodies:unknown[]=[]
  page.on('request',request=>{if(new URL(request.url()).pathname==='/api/activity')bodies.push(request.postDataJSON())})
  await page.goto(vista.origin+'/')
  await expect(page.locator('.sender-card textarea')).toBeVisible()
  await page.evaluate(()=>{window.dispatchEvent(new Event('pointerdown'));window.dispatchEvent(new Event('pagehide'))})
  await page.waitForTimeout(16_000)
  expect(bodies).toEqual([])
  expect((await vista.api('/statistics?days=7')).activeMinutes).toBe(0)
  await page.locator('.sender-card textarea').fill('Private text must not be in activity telemetry')
  await page.locator('.sender-card textarea').press('ArrowLeft')
  await page.evaluate(()=>window.dispatchEvent(new Event('pagehide')))
  await expect.poll(()=>bodies.length).toBe(1)
  const body=bodies[0] as {minutes:number[]}
  expect(Object.keys(body)).toEqual(['minutes'])
  expect(body.minutes).toHaveLength(1)
  await expect.poll(async()=> (await vista.api('/statistics?days=7')).activeMinutes).toBe(1)
  await page.evaluate(()=>window.dispatchEvent(new Event('pagehide')))
  await page.waitForTimeout(150)
  expect(bodies).toHaveLength(1)
})

test.describe('missing usage',()=>{
  test.use({providerOutput:'responses'})
  test('unreported model usage stays unknown rather than showing a measured zero',async({page,vista})=>{
    const session=await vista.api('/sessions',{method:'POST',body:'{}'})
    await vista.api(`/sessions/${session.id}/messages`,{method:'POST',body:JSON.stringify({content:'Unknown usage fixture'})})
    await expect.poll(async()=> (await vista.api('/statistics?days=7')).requests).toBe(2)
    await page.goto(vista.origin+'/settings/statistics')
    const stats=page.locator('.statistics-settings')
    await expect(stats.locator('.statistics-totals dd').nth(2)).toHaveText('—')
    await expect(stats.locator('.statistics-notes')).toContainText('2 calls did not report usage')
    const chart=stats.getByRole('img',{name:'Daily activity and usage',exact:true})
    await chart.focus()
    await page.keyboard.press('End')
    await expect(stats.locator('.statistics-trend figcaption')).toContainText('— tokens')
    await expect(stats.locator('.statistics-trend figcaption')).toContainText('2 calls did not report usage')
  })
})

test.describe('failed requests without usage',()=>{
  test.use({modelFailure:{status:429,body:{error:{code:'insufficient_quota',message:'Account quota is exhausted'}}}})
  test('failed conversation requests do not appear as unreported consumption',async({page,vista})=>{
    const session=await vista.api('/sessions',{method:'POST',body:'{}'})
    await vista.api(`/sessions/${session.id}/messages`,{method:'POST',body:JSON.stringify({content:'Unavailable model fixture'})})
    await expect.poll(async()=> (await vista.api(`/sessions/${session.id}/checkpoint`)).session.executing).toBe(false)
    // Only the successful title request contributes usage.
    await expect.poll(async()=> (await vista.api('/statistics?days=7')).requests).toBe(1)
    await page.goto(vista.origin+'/settings/statistics')
    await expect(page.locator('.statistics-token-value')).toHaveAttribute('aria-label','Token usage details: 25 tokens')
    await expect(page.locator('.statistics-notes')).not.toContainText('did not report usage')
    expect((await vista.api('/statistics?days=7')).unreported).toBe(0)
  })
})
