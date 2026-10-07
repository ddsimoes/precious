import { expect, type APIRequestContext } from '@playwright/test'

interface Amount {
  files: number
  bytes: number
}

// Coverage is CoverageJSON: how much of what could have a copy was checked.
export interface Coverage {
  candidate: Amount
  checked: Amount
  unchecked: Amount
  unreadable: Amount
}

interface Home {
  scans: unknown[]
  hashing: unknown[]
  coverage: Coverage
}

interface JobStatus {
  id: string
  kind: string
  state: string
}

const terminal = ['succeeded', 'failed', 'cancelled']

// awaitDuplicates waits until every scan and hashing job has ended with
// everything checked, and every relations pass they asked for has ended
// (design D5): the duplicates and cards are then those of the whole
// index. startHash starts a hashing job of a source through the command API
// and returns its job ID: with nothing left to hash, it ends at once, and it
// is newer than every job the scans and hashing asked for, so the relate
// jobs to wait for are the ones before it.
export async function awaitDuplicates(request: APIRequestContext, startHash: () => Promise<string>) {
  await expect
    .poll(
      async () => {
        const home: Home = await (await request.get('/api/home')).json()
        return (
          home.scans.length === 0 &&
          home.hashing.length === 0 &&
          home.coverage.candidate.files > 0 &&
          home.coverage.unchecked.files === 0
        )
      },
      { message: 'hashing ends with everything checked', timeout: 60_000, intervals: [100] },
    )
    .toBe(true)

  const fence = Number(await startHash())
  await expect
    .poll(async () => (await job(request, fence))?.state, { message: `job ${fence} ends`, timeout: 30_000 })
    .toBe('succeeded')
  await expect
    .poll(
      async () => {
        const busy: string[] = []
        // Job IDs grow; a removed source took its jobs, so IDs below the
        // fence may be missing, but none after it.
        for (let id = 1; ; id++) {
          const status = await job(request, id)
          if (status === null) {
            if (id > fence) {
              break
            }
            continue
          }
          if (status.kind === 'relate' && !terminal.includes(status.state)) {
            busy.push(`${status.id} ${status.state}`)
          }
          if (status.kind === 'relate' && status.state === 'failed') {
            throw new Error(`relate job ${status.id} failed`)
          }
        }
        return busy
      },
      { message: 'every relate job ends', timeout: 60_000, intervals: [100] },
    )
    .toEqual([])
  const opportunities: { computed_at: string | null } = await (await request.get('/api/opportunities')).json()
  expect(opportunities.computed_at).not.toBeNull()
}

async function job(request: APIRequestContext, id: number): Promise<JobStatus | null> {
  const resp = await request.get(`/api/jobs/${id}`)
  if (resp.status() === 404) {
    return null
  }
  expect(resp.status()).toBe(200)
  return (await resp.json()) as JobStatus
}
