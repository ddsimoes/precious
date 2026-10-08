import { useQuery, type QueryClient } from '@tanstack/react-query'

import {
  displayState,
  isTerminal,
  scanKind,
  type ActiveJob,
  type JobEvent,
  type StartScanResult,
} from '@/api/jobs'
import { apiGet, postCommand } from '@/app/api'

// Sources, the folder picker, and the source commands (design Interfaces:
// GET /api/sources, GET /api/picker, add-source, rename-source,
// remove-source, set-source-schedule, start-scan, and R3's
// set-source-writes).

export type SourceState = 'online' | 'offline' | 'unavailable'

export type VolumeKind = 'uuid' | 'zfs' | 'fsid' | 'path'

export interface Volume {
  kind: VolumeKind
  id: string
  label: string | null
  fs_type: string
  // strong volumes are recognized wherever they are mounted; weak ones only
  // at their current mount point.
  strong: boolean
}

export interface Capabilities {
  known: boolean
  read_only: boolean
  case_sensitive: boolean
  normalization_sensitive: boolean
  stable_identity: boolean
  local_time: boolean
  hard_links: boolean
  time_resolution_ns: number
  // no_replace_rename is true where the file system can rename without ever
  // replacing what holds the new name (R3 design D2).
  no_replace_rename: boolean
}

// WritesUnavailable says why changes cannot be allowed on a source (R3
// design D1), in the order the server checks.
export type WritesUnavailable = 'forbidden_by_config' | 'read_only' | 'no_replace_rename'

// SourceWrites is whether Precious may change the source when the owner
// asks: off until the owner allows it, and impossible while unavailable.
export interface SourceWrites {
  enabled: boolean
  unavailable: WritesUnavailable | null
}

export interface Totals {
  bytes: number
  files: number
  dirs: number
}

// Schedule is a source's rescan schedule: every day, or every week on one
// weekday (0 is Sunday), at a time of day ("HH:MM") in a named time zone.
export interface Schedule {
  every: 'day' | 'week'
  at: string
  weekday?: number
  zone: string
}

// ScheduleSkip is the last time a scheduled scan came due and was skipped,
// and why: the source's state then, or invalid_schedule.
export interface ScheduleSkip {
  at: string
  reason: string
}

export interface Source {
  id: string
  label: string
  state: SourceState
  state_reason: string | null
  mount_point: string | null
  // path is the source's folder where its disk is mounted now; null when the
  // disk is not connected.
  path: string | null
  // rel_root is the source's folder inside its disk, '' for the disk's top.
  rel_root: string
  volume: Volume
  capabilities: Capabilities
  root_entry_id: string
  totals: Totals
  last_scan_at: string | null
  active_job: ActiveJob | null
  // schedule is null when scheduled rescans are off, and next_scan_at then
  // too; schedule_skipped is null unless the last due time was skipped.
  schedule: Schedule | null
  next_scan_at: string | null
  schedule_skipped: ScheduleSkip | null
  writes: SourceWrites
}

export interface SourcesResponse {
  sources: Source[]
}

export interface PickerItem {
  handle: string
  name: string
  path: string
  volume_label: string
  fs_type: string
  is_source: boolean
}

export interface PickerRoots {
  roots: PickerItem[]
}

export interface PickerListing {
  entry: PickerItem
  children: PickerItem[]
  truncated: boolean
}

export interface SourceResult {
  source: Source
}

export const sourcesQueryKey = ['sources'] as const

export function pickerQueryKey(handle: string | null) {
  return ['picker', handle] as const
}

export function fetchSources(signal?: AbortSignal): Promise<SourcesResponse> {
  return apiGet<SourcesResponse>('/api/sources', signal)
}

// useSources reads the source list. Reading it also refreshes each source's
// availability on the server.
export function useSources() {
  return useQuery({
    queryKey: sourcesQueryKey,
    queryFn: ({ signal }) => fetchSources(signal),
  })
}

export function fetchPickerRoots(signal?: AbortSignal): Promise<PickerRoots> {
  return apiGet<PickerRoots>('/api/picker', signal)
}

export function fetchPickerListing(handle: string, signal?: AbortSignal): Promise<PickerListing> {
  return apiGet<PickerListing>(`/api/picker?${new URLSearchParams({ handle })}`, signal)
}

// addSource adds the folder a picker handle designates. Without a label the
// server names the source after the folder.
export function addSource(handle: string, label: string, csrfToken: string): Promise<SourceResult> {
  const body = label === '' ? { handle } : { handle, label }
  return postCommand<SourceResult>('add-source', body, csrfToken)
}

export function renameSource(sourceId: string, label: string, csrfToken: string): Promise<SourceResult> {
  return postCommand<SourceResult>('rename-source', { source_id: sourceId, label }, csrfToken)
}

export function removeSource(sourceId: string, csrfToken: string): Promise<Record<string, never>> {
  return postCommand<Record<string, never>>('remove-source', { source_id: sourceId }, csrfToken)
}

export function startScan(sourceId: string, csrfToken: string): Promise<StartScanResult> {
  return postCommand<StartScanResult>('start-scan', { source_id: sourceId }, csrfToken)
}

// setSourceSchedule sets the source's rescan schedule; null turns it off.
export function setSourceSchedule(
  sourceId: string,
  schedule: Schedule | null,
  csrfToken: string,
): Promise<SourceResult> {
  return postCommand<SourceResult>('set-source-schedule', { source_id: sourceId, schedule }, csrfToken)
}

// setSourceWrites allows or stops changes by Precious on a source. The
// owner confirms allowing them first; stopping needs no confirmation.
export function setSourceWrites(sourceId: string, enabled: boolean, csrfToken: string): Promise<SourceResult> {
  return postCommand<SourceResult>('set-source-writes', { source_id: sourceId, enabled }, csrfToken)
}

// updateSource replaces one source in the cached list.
export function updateSource(queryClient: QueryClient, id: string, update: (source: Source) => Source) {
  queryClient.setQueryData<SourcesResponse>(sourcesQueryKey, (data) =>
    data && { sources: data.sources.map((s) => (s.id === id ? update(s) : s)) },
  )
}

// applyJobEventToSources keeps each source's active scan current. A scan that
// ends refetches the list, whose totals and last scan it changed.
export function applyJobEventToSources(queryClient: QueryClient, event: JobEvent) {
  if (event.kind !== scanKind || event.source_id === undefined) {
    return
  }
  if (isTerminal(event.state)) {
    updateSource(queryClient, event.source_id, (s) =>
      s.active_job?.job_id === event.job_id ? { ...s, active_job: null } : s,
    )
    void queryClient.invalidateQueries({ queryKey: sourcesQueryKey })
    return
  }
  const active: ActiveJob = { job_id: event.job_id, state: displayState(event), progress: event.progress }
  updateSource(queryClient, event.source_id, (s) => ({ ...s, active_job: active }))
}
