import assert from 'node:assert/strict'
import test from 'node:test'
import { effectScope,nextTick,ref } from 'vue'
import type { VistaClient } from '../client/vista.ts'
import { createWorkspaceSync,stateTopics } from '../client/workspaceSync.ts'
import { localDateKey,type UsageStatistics } from '../client/statistics.ts'
import { useStatistics } from './useStatistics.ts'

type Sync=ReturnType<typeof createWorkspaceSync>
type Receive=Parameters<Sync['subscribe']>[1]
const result=(version='a:0',tokens=125)=>({tokens,days:[{date:localDateKey(),activeMinutes:1,tokens,requests:1,unreported:0}],sampledAt:new Date().toISOString(),version}) as UsageStatistics
const settle=async()=>{await nextTick();await new Promise(resolve=>setImmediate(resolve))}
function fixture(request:VistaClient['request']) {
  const client={request} as VistaClient
  let receive:Receive|undefined
  const sync={subscribe(_topics:unknown,callback:Receive){receive=callback;return()=>{receive=undefined}}} as unknown as Sync
  const days=ref(30)
  let scope=effectScope()
  const open=()=>{scope=effectScope();return scope.run(()=>useStatistics(client,sync,days))!}
  const point=async(revision=0,epoch='a',signal=new AbortController().signal)=>{
    const revisions=Object.fromEntries(stateTopics.map(topic=>[topic,topic==='statistics'?revision:0])) as Parameters<Receive>[3]
    await receive?.(['statistics'],signal,{epoch,revisions},revisions)
  }
  return {days,open,point,close:()=>scope.stop()}
}

test('statistics are scoped to the open view and reused on matching revision, including reopen',async()=>{
  let reads=0
  const f=fixture(async<T>()=>{reads++;return result(reads===1?'a:0':reads===2?'a:1':'b:0') as T})
  await f.point();assert.equal(reads,0)
  const view=f.open()
  await f.point();await f.point()
  assert.equal(reads,1);assert.equal(view.data.value?.tokens,125)
  f.close();await f.point(1);assert.equal(reads,1)
  f.open();await f.point();assert.equal(reads,1)
  await f.point(1);assert.equal(reads,2)
  await f.point(0,'b');assert.equal(reads,3)
  f.close()
})

test('switching periods cancels old reads and late responses cannot replace the new chart',async()=>{
  let release!:(value:UsageStatistics)=>void
  const old=new Promise<UsageStatistics>(resolve=>{release=resolve})
  const signals:AbortSignal[]=[]
  const paths:string[]=[]
  const f=fixture(async<T>(path:string,init?:RequestInit)=>{paths.push(path);signals.push(init?.signal as AbortSignal);return (paths.length===1?await old:result('a:0',250)) as T})
  const view=f.open()
  const pending=f.point()
  f.days.value=7;await settle()
  assert.equal(signals[0]!.aborted,true)
  assert.equal(view.data.value?.tokens,250)
  release(result());await pending
  assert.equal(view.data.value?.tokens,250)
  assert.equal(paths.length,2)
  f.days.value=30;await settle()
  assert.equal(paths.length,3)
  f.close()
})

test('reconnect replaces an aborted in-flight read at the same revision',async()=>{
  let release!:(value:UsageStatistics)=>void
  const old=new Promise<UsageStatistics>(resolve=>{release=resolve})
  let reads=0
  const f=fixture(async<T>()=>{reads++;return (reads===1?await old:result()) as T})
  const view=f.open()
  const first=new AbortController()
  const pending=f.point(0,'a',first.signal)
  first.abort()
  await f.point()
  assert.equal(reads,2);assert.equal(view.data.value?.tokens,125)
  release(result('a:0',1));await pending
  assert.equal(view.data.value?.tokens,125)
  f.close()
})

test('a failed manual read shared with a state update remains unacknowledged for recovery',async()=>{
  let reads=0,fail=true
  const f=fixture(async<T>()=>{reads++;if(fail)throw new Error('temporary read failure');return result('a:1') as T})
  const view=f.open()
  const manual=view.refresh()
  await assert.rejects(f.point(1),/temporary read failure/)
  await manual
  assert.equal(reads,1)
  assert.match(view.error.value,/temporary read failure/)
  fail=false
  await f.point(1)
  assert.equal(reads,2)
  assert.equal(view.data.value?.tokens,125)
  assert.equal(view.error.value,'')
  f.close()
})

test('reopening after an hour rereads the heatmap even at the same revision',async t=>{
  t.mock.timers.enable({apis:['Date'],now:Date.now()})
  let reads=0
  const f=fixture(async<T>()=>{reads++;return result() as T})
  f.open();await f.point();assert.equal(reads,1)
  f.close()
  t.mock.timers.tick(60*60*1000)
  f.open();await f.point();assert.equal(reads,2)
  f.close()
})
