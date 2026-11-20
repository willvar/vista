import assert from 'node:assert/strict'
import test from 'node:test'
import { jobActive } from '@vista/client'
import type { Job } from '@vista/client'
import { jobStatusKey } from './jobs.ts'

test('process state distinguishes running, normal exit, requested stop, timeout and interruption', () => {
  const job: Job = { id: 'job', runId: 'run', directory: '/work', command: 'sleep 10', status: 'starting', bytes: 0, exitCode: -1, complete: false }
  assert.equal(jobActive(job), true)
  for (const [status, terminationReason, key] of [
    ['running', '', 'processRunning'], ['succeeded', '', 'processFinished'], ['failed', 'exit', 'processFailed'],
    ['interrupted', 'stopped', 'processStopped'], ['interrupted', 'timeout', 'processTimedOut'], ['interrupted', 'worker_closed', 'processInterrupted'],
  ]) assert.equal(jobStatusKey({ ...job, status, terminationReason }), key)
  assert.equal(jobActive({ ...job, status: 'interrupted' }), false)
})
