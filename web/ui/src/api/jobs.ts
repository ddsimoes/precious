// Jobs as the API reports them: GET /api/jobs/{id}, the `job` events of
// GET /api/events, and the active scan embedded in sources and Home.

// JobState is a job's lifecycle state. `cancel_requested` is a displayed
// state only: a running or queued job whose cancellation was asked for.
export type JobState =
  | 'queued'
  | 'running'
  | 'paused'
  | 'succeeded'
  | 'failed'
  | 'cancelled'
  | 'cancel_requested'

// Progress holds a job's running counters. A scan reports `phase` (1 walking,
// 2 finishing), `dirs`, `files`, `bytes`, `written`, `unreadable`, and `missing`.
export type Progress = Record<string, number>

// ActiveJob is the active scan of a source (SourceJSON.active_job).
export interface ActiveJob {
  job_id: string
  state: JobState
  progress: Progress
}

// JobEvent is the data of an SSE `job` event.
export interface JobEvent {
  job_id: string
  kind: string
  source_id?: string
  state: JobState
  cancel_requested: boolean
  pause_reason?: string
  terminal_code?: string
  progress: Progress
  attempts: number
}

// StartScanResult is the 202 body of the start-scan command.
export interface StartScanResult {
  job_id: string
  state: JobState
  coalesced: boolean
}

export const scanKind = 'scan'

export function isTerminal(state: JobState): boolean {
  return state === 'succeeded' || state === 'failed' || state === 'cancelled'
}

// displayState is the state to show for an event: a live job whose
// cancellation was requested shows as `cancel_requested`.
export function displayState(event: JobEvent): JobState {
  return event.cancel_requested && !isTerminal(event.state) ? 'cancel_requested' : event.state
}
