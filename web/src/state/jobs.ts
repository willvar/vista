import type { Job } from '../client/types.ts'

export function jobActive(job: Job) { return job.status === 'starting' || job.status === 'running' }
export function jobStatusKey(job: Job) {
  if (job.status === 'starting') return 'processStarting'
  if (job.status === 'running') return 'processRunning'
  if (job.terminationReason === 'stopped') return 'processStopped'
  if (job.terminationReason === 'timeout') return 'processTimedOut'
  if (job.status === 'succeeded') return 'processFinished'
  return job.status === 'failed' ? 'processFailed' : 'processInterrupted'
}
