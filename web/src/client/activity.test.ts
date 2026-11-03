import assert from 'node:assert/strict'
import test from 'node:test'
import { createActivityCollector } from './activity.ts'

test('idle pages make no reports and actual interaction is coalesced to unique UTC minutes',async () => {
  let now = 600_000
  const reports:number[][]=[]
  const tracker=createActivityCollector(async minutes=>{reports.push(minutes)}, {now:()=>now,delayMs:60_000})
  await tracker.flush()
  assert.deepEqual(reports,[])
  for(let i=0;i<100;i++)tracker.record()
  now+=60_000;tracker.record()
  await tracker.flush()
  assert.deepEqual(reports,[[10,11]])
  tracker.record();await tracker.flush()
  assert.equal(reports.length,1)
  now+=60_000;tracker.record();tracker.stop()
  await tracker.flush()
  assert.deepEqual(reports,[[10,11],[12]])
  tracker.record();await tracker.flush()
  assert.equal(reports.length,2)
})

test('failed reports wait for interaction to retry and never grow beyond a bounded backlog',async () => {
  let now=100_000_000, fail=true
  const reports:number[][]=[]
  const tracker=createActivityCollector(async minutes=>{reports.push(minutes);if(fail)throw new Error('offline')},{now:()=>now,delayMs:60_000})
  tracker.record();await tracker.flush()
  assert.equal(reports.length,1)
  fail=false;tracker.record();await tracker.flush()
  assert.deepEqual(reports[0],reports[1])
  for(let i=0;i<200;i++){now+=60_000;tracker.record()}
  await tracker.flush()
  assert.equal(reports[2]!.length,120)
  tracker.stop()
})

test('page exit waits for in-flight reports and flushes newly recorded minutes',async () => {
  let now=600_000
  let release!:()=>void
  const first=new Promise<void>(resolve=>{release=resolve})
  const reports:number[][]=[]
  const tracker=createActivityCollector(async minutes=>{reports.push(minutes);if(reports.length===1)await first},{now:()=>now,delayMs:60_000})
  tracker.record()
  const pending=tracker.flush()
  now+=60_000;tracker.record()
  const exit=tracker.flush()
  release();await Promise.all([pending,exit])
  assert.deepEqual(reports,[[10],[11]])
  tracker.stop()
})
