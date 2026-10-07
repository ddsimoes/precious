import type { QueryClient } from '@tanstack/react-query'

import { entriesQueryRoot, type ArchiveState, type ContentState, type EntryRow } from '@/api/entries'
import { homeQueryRoot, type Amount, type Decision } from '@/api/home'
import { isHashKind, isTerminal, relateKind, type JobEvent, type JobState } from '@/api/jobs'
import { searchQueryRoot } from '@/api/search'
import { apiGet, postCommand } from '@/app/api'

// What hashing learned about content (R2 design D8, D9, D16, Interfaces):
// coverage, the copies of a file, the relations of a folder, and the
// commands that start hashing.

// Coverage is CoverageJSON: of the files that could have a copy (candidate:
// files sharing their size with another), how many were checked, are not
// checked yet, or could not be read.
export interface Coverage {
  candidate: Amount
  checked: Amount
  unchecked: Amount
  unreadable: Amount
}

// checkedShare is the fraction of the candidate bytes that were checked; 1
// when nothing could have a copy.
export function checkedShare(coverage: Coverage): number {
  return coverage.candidate.bytes === 0 ? 1 : coverage.checked.bytes / coverage.candidate.bytes
}

// Copy is CopyJSON: one copy of a content. ref is an entry ID, or "m<id>"
// for a member of an archive, whose archive_id is then set. decision is the
// copy's own decision, null when it follows its folder (always for a
// member).
export interface Copy {
  ref: string
  source_id: string
  path: string
  path_b64: string
  archive_id: string | null
  hard_link: boolean
  offline: boolean
  decision: Decision | null
  eff_decision: Decision
}

export type RelationKind = 'same' | 'inside' | 'overlap'

// Relation is RelationJSON: how a folder or archive relates to another.
// self says which side the entry asked about is: for inside, side a is the
// contained one.
export interface Relation {
  id: string
  kind: RelationKind
  self: 'a' | 'b'
  other: EntryRow
  matched_bytes: number
  redundant_bytes: number
  only_here: Amount
  only_there: Amount
}

// EntryContent is the detail's content of a file or file member: its state,
// its SHA-256 once read in full, and up to 20 of its copies.
export interface EntryContent {
  state: ContentState
  sha256: string | null
  checked_at: string | null
  copies: Copy[]
  copies_count: number
}

export type ArchiveFormat = 'zip' | 'tar' | 'tar_gzip' | 'tar_bzip2' | 'gzip' | 'bzip2'

// ArchiveInfo is the detail's archive of an archive file Precious opened.
export interface ArchiveInfo {
  format: ArchiveFormat
  state: ArchiveState
  detail: string | null
  members: number
  unpacked_bytes: number
}

export interface CopiesPage {
  items: Copy[]
  next_cursor: string | null
  count: number
}

export function copiesQueryKey(ref: string) {
  return ['entries', ref, 'copies'] as const
}

export function fetchCopies(ref: string, cursor: string | null, signal?: AbortSignal): Promise<CopiesPage> {
  const params = new URLSearchParams()
  if (cursor !== null) {
    params.set('cursor', cursor)
  }
  const query = params.size === 0 ? '' : `?${params}`
  return apiGet<CopiesPage>(`/api/entries/${encodeURIComponent(ref)}/copies${query}`, signal)
}

// HashingJob is an active hashing job of a source, on Home.
export interface HashingJob {
  source_id: string
  job_id: string
  kind: string
  state: JobState
  progress: Record<string, number>
}

export interface StartedJob {
  job_id: string
  state: JobState
  coalesced: boolean
}

// checkNow hashes the files not checked yet inside one or two folders or
// archives before any other hashing work on their disks.
export function checkNow(refs: string[], csrfToken: string): Promise<{ jobs: StartedJob[] }> {
  return postCommand<{ jobs: StartedJob[] }>('check-now', { entry_ids: refs }, csrfToken)
}

// The query roots of the responses built from duplicates. Each module keys
// its responses under its root.
export const opportunitiesQueryRoot = ['opportunities'] as const
export const gemsQueryRoot = ['gems'] as const
export const compareQueryRoot = ['compare'] as const

// duplicatesQueryRoots are every response that shows duplicates or content
// figures: they are fetched again when hashing or a recomputation of
// relations ends.
export const duplicatesQueryRoots = [
  opportunitiesQueryRoot,
  gemsQueryRoot,
  compareQueryRoot,
  entriesQueryRoot,
  searchQueryRoot,
  homeQueryRoot,
] as const

// applyJobEventToDuplicates refetches every response that shows duplicates
// when a hashing job or a recomputation of relations ends: content states,
// copies, relations, cards, and Gems may have changed.
export function applyJobEventToDuplicates(queryClient: QueryClient, event: JobEvent) {
  if ((!isHashKind(event.kind) && event.kind !== relateKind) || !isTerminal(event.state)) {
    return
  }
  for (const queryKey of duplicatesQueryRoots) {
    void queryClient.invalidateQueries({ queryKey })
  }
}
