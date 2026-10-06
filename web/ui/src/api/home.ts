import type { QueryClient } from '@tanstack/react-query'

import { displayState, isTerminal, scanKind, type JobEvent, type JobState, type Progress } from '@/api/jobs'
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

export interface YearAmount extends Amount {
  year: number
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
  decisions: Record<Decision, Amount>
  // partial is set when part of a counted tree could not be read.
  partial: boolean
  scans: HomeScan[]
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

// applyJobEventToHome keeps the active scans of every cached Home current. A
// scan that ends refetches Home, whose figures it changed.
export function applyJobEventToHome(queryClient: QueryClient, event: JobEvent) {
  const sourceId = event.source_id
  if (event.kind !== scanKind || sourceId === undefined) {
    return
  }
  const done = isTerminal(event.state)
  const scan: HomeScan = {
    source_id: sourceId,
    job_id: event.job_id,
    state: displayState(event),
    progress: event.progress,
  }
  for (const [key, home] of queryClient.getQueriesData<Home>({ queryKey: homeQueryRoot })) {
    const filter = key[1]
    if (home === undefined || (filter !== null && filter !== sourceId)) {
      continue
    }
    let scans: HomeScan[]
    if (done) {
      scans = home.scans.filter((s) => s.job_id !== event.job_id)
    } else if (home.scans.some((s) => s.job_id === event.job_id)) {
      scans = home.scans.map((s) => (s.job_id === event.job_id ? scan : s))
    } else {
      scans = [...home.scans, scan]
    }
    queryClient.setQueryData<Home>(key, { ...home, scans })
  }
  if (done) {
    void queryClient.invalidateQueries({ queryKey: homeQueryRoot })
  }
}
