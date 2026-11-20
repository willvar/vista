import type { Job } from '@vista/client'

export function jobStatusKey(job: Job) {
  if (job.status === 'starting') return 'processStarting'
  if (job.status === 'running') return 'processRunning'
  if (job.terminationReason === 'stopped') return 'processStopped'
  if (job.terminationReason === 'timeout') return 'processTimedOut'
  if (job.status === 'succeeded') return 'processFinished'
  return job.status === 'failed' ? 'processFailed' : 'processInterrupted'
}
