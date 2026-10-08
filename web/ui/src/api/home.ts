import type { QueryClient } from '@tanstack/react-query'

import type { Coverage, HashingJob } from '@/api/content'
import {
  displayState,
  isHashKind,
  isTerminal,
  scanKind,
  type JobEvent,
  type JobState,
  type Progress,
} from '@/api/jobs'
import type { Card } from '@/api/opportunities'
import type { Totals } from '@/api/sources'
import { apiGet } from '@/app/api'

// GET /api/home?source=ID: the figures of one source, or of every source
// when the source is omitted.

export type Family = 'personal' | 'programs' | 'disposable' | 'containers'

export type FileKind =
  | 'image'
  | 'video'
  | 'audio'
  | 'document'
  | 'source'
  | 'archive'
  | 'installer'
  | 'executable'
  | 'system'
  | 'other'

export type Decision = 'undecided' | 'keep' | 'discard' | 'later'

export interface Amount {
  bytes: number
  files: number
}

export interface FamilyAmount extends Amount {
  family: Family
}

export interface KindAmount extends Amount {
  kind: FileKind
}

// YearAmount is the share of files last changed in a year; year is null for
// files whose date is unknown (at or before the Unix epoch).
export interface YearAmount extends Amount {
  year: number | null
}

export interface HomeScan {
  source_id: string
  job_id: string
  state: JobState
  progress: Progress
}

export interface Home {
  totals: Totals
  by_family: FamilyAmount[]
  by_kind: KindAmount[]
  by_year: YearAmount[]
  // decisions leave the quarantine out; quarantine holds its bytes and
  // files apart (R4 design D15).
  decisions: Record<Decision, Amount> & { quarantine: Amount }
  // partial is set when part of a counted tree could not be read.
  partial: boolean
  scans: HomeScan[]
  // coverage tells how much of what could have a copy was checked (R2).
  coverage: Coverage
  // cards are the opportunity cards, with the bytes of their open rows.
  cards: Card[]
  // hashing lists the active hashing jobs.
  hashing: HashingJob[]
}

export const homeQueryRoot = ['home'] as const

// homeQueryKey keys the figures of source, or of every source when null.
export function homeQueryKey(source: string | null) {
  return ['home', source] as const
}

export function fetchHome(source: string | null, signal?: AbortSignal): Promise<Home> {
  const query = source === null ? '' : `?${new URLSearchParams({ source })}`
  return apiGet<Home>(`/api/home${query}`, signal)
}

// withJob puts a job's latest state into a list of active jobs, or takes it
// out once it ended.
function withJob<T extends { job_id: string }>(jobs: T[], job: T, done: boolean): T[] {
  if (done) {
    return jobs.filter((j) => j.job_id !== job.job_id)
  }
  return jobs.some((j) => j.job_id === job.job_id)
    ? jobs.map((j) => (j.job_id === job.job_id ? job : j))
    : [...jobs, job]
}

// applyJobEventToHome keeps the active scans and hashing jobs of every
// cached Home current. A scan or hashing job that ends refetches Home,
// whose figures it changed.
export function applyJobEventToHome(queryClient: QueryClient, event: JobEvent) {
  const sourceId = event.source_id
  const hashing = isHashKind(event.kind)
  if ((event.kind !== scanKind && !hashing) || sourceId === undefined) {
    return
  }
  const done = isTerminal(event.state)
  const job = { source_id: sourceId, job_id: event.job_id, state: displayState(event), progress: event.progress }
  for (const [key, home] of queryClient.getQueriesData<Home>({ queryKey: homeQueryRoot })) {
    const filter = key[1]
    if (home === undefined || (filter !== null && filter !== sourceId)) {
      continue
    }
    const next = hashing
      ? { ...home, hashing: withJob(home.hashing, { ...job, kind: event.kind }, done) }
      : { ...home, scans: withJob(home.scans, job, done) }
    queryClient.setQueryData<Home>(key, next)
  }
  if (done) {
    void queryClient.invalidateQueries({ queryKey: homeQueryRoot })
  }
}
