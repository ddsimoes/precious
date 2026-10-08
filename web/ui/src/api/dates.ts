import type { QueryClient } from '@tanstack/react-query'

import { displayState, isTerminal, mediaKind, type JobEvent, type JobState, type Progress } from '@/api/jobs'
import type { PlanResult } from '@/api/organize'
import { apiGet, postCommand } from '@/app/api'
import { localMillis, type LocalPrecision } from '@/lib/format'

// Media dates (R5 design Interfaces): the date each photo and video was
// taken, where it comes from and how sure it is, the cameras whose clocks
// look wrong, the owner's corrections, and the two plans that act on the
// dates, setting files' modification times and organizing by date.

export type Precision = LocalPrecision

// DateSource is where an effective date comes from, most trusted first.
export type DateSource = 'owner' | 'exif' | 'gps' | 'container' | 'file_name' | 'folder_name' | 'mtime' | 'none'

export const dateSources: DateSource[] = [
  'owner',
  'exif',
  'gps',
  'container',
  'file_name',
  'folder_name',
  'mtime',
  'none',
]

export type Confidence = 'high' | 'medium' | 'low' | 'lowest' | 'none'

export const confidences: Confidence[] = ['high', 'medium', 'low', 'lowest', 'none']

// MetaState is what the media job knows of a file's header: not read yet,
// read, a format it does not read, or a file it could not open.
export type MetaState = 'pending' | 'read' | 'none' | 'unreadable'

export const metaStates: MetaState[] = ['pending', 'read', 'none', 'unreadable']

export type DateFlag = 'mtime_disagrees' | 'implausible' | 'camera_offset' | 'no_date_metadata'

export const dateFlags: DateFlag[] = ['mtime_disagrees', 'implausible', 'camera_offset', 'no_date_metadata']

export type CorrectionKind = 'set' | 'shift' | 'use_name' | 'use_folder'

// LocalDate is a wall time as the server writes it: YYYY, YYYY-MM,
// YYYY-MM-DD, or YYYY-MM-DDTHH:MM:SS.
export type LocalDate = string

// DateJSON is an effective date. instant, local, and precision are null
// when there is no date (source none).
export interface DateJSON {
  instant: string | null
  local: LocalDate | null
  offset_min: number | null
  precision: Precision | null
  source: DateSource
  confidence: Confidence
  // refined is set when a name or folder date took the modification time
  // that falls inside it.
  refined: boolean
  corrected: CorrectionKind | null
}

export interface CorrectionJSON {
  kind: CorrectionKind
  local?: LocalDate
  offset_min?: number
  shift_s?: number
  created_at: string
}

export interface CameraRef {
  key: string
  make: string | null
  model: string | null
  serial: string | null
}

export interface MediaEntry {
  id: string
  source_id: string
  name: string
  path: string
  path_b64: string
  size: number
  mtime: string | null
}

// MediaDate is one row of the dates list.
export interface MediaDate {
  entry: MediaEntry
  date: DateJSON
  metadata: MetaState
  flags: DateFlag[]
  camera: CameraRef | null
  correction: CorrectionJSON | null
}

// Candidate is one date found for a file, plausible or not.
export interface Candidate {
  source: DateSource
  local: LocalDate
  offset_min: number | null
  instant: string
  precision: Precision
  plausible: boolean
}

// EntryDates is GET /api/entries/{id}/dates: a MediaDate without its
// entry, and every date found.
export interface EntryDates extends Omit<MediaDate, 'entry'> {
  candidates: Candidate[]
}

export interface DatesSummary {
  media: number
  metadata: Record<MetaState, number>
  by_source: Record<DateSource, number>
  by_confidence: Record<Confidence, number>
  flags: Record<DateFlag, number>
  cameras: { offset: number; disagrees: number }
  time_zone: string
  time_zone_set: boolean
  summary_at: string | null
  detected_at: string | null
}

export interface DatesPage {
  items: MediaDate[]
  next_cursor: string | null
}

export type CameraState = 'ok' | 'offset' | 'disagrees'

// Reference is what sides with the other cameras at an event: their GPS,
// the folder's date, or the camera's own GPS; null for none.
export type Reference = 'gps' | 'folder_name' | 'own_gps'

export interface CameraEvent {
  folder: { id: string; path: string; path_b64: string }
  delta_s: number
  photos: number
  reference: Reference | null
}

export interface Camera extends CameraRef {
  source_id: string
  photos: number
  state: CameraState
  suggested_shift_s: number | null
  events: CameraEvent[]
  computed_at: string
}

export interface CamerasResponse {
  items: Camera[]
}

// DatesFilter is what the list shows of one source: a flag, a source of
// date, a camera, and a folder (within), each optional.
export interface DatesFilter {
  source: string
  flag: DateFlag | null
  dateSource: DateSource | null
  camera: string | null
  within: string | null
}

// DateTargets names what a correction or a date plan applies to (D11): one
// entry (corrections only), up to 1,000 entries, or up to 100 folders of
// one source, optionally only one camera's photos directly in them.
export type DateTargets =
  | { entry_id: string }
  | { entry_ids: string[] }
  | { folder_ids: string[]; camera_key?: string }

// BulkTargets are the targets of the plans, which are always bulk.
export type BulkTargets = { entry_ids: string[] } | { folder_ids: string[] }

export type Correction =
  | { kind: 'set'; local: LocalDate; offset_min?: number }
  | { kind: 'shift'; shift_s: number }
  | { kind: 'use_name' }
  | { kind: 'use_folder' }

export type SkipReason = 'not_media' | 'no_name_date' | 'no_folder_date' | 'in_future'

export interface SkippedDate {
  entry_id: string
  path: string
  path_b64: string
  reason: SkipReason
}

export interface SetCorrectionResult {
  applied: number
  skipped_count: number
  // skipped lists up to 100 of the skipped targets, in path order.
  skipped: SkippedDate[]
  batch_id: string
}

export interface ClearCorrectionResult {
  cleared: number
  batch_id: string
}

// SetMtimePlan is the 201 answer of plan-set-mtime: files already at their
// date are only counted.
export interface SetMtimePlan extends PlanResult {
  summary: { unchanged: number }
}

// DateOrganizePlan is the 201 answer of plan-date-organize: how many
// planned files have a copy among the targets or in the destination, and
// how many leave a file of the same name behind.
export interface DateOrganizePlan extends PlanResult {
  summary: { files_with_copies: number; split_siblings: number }
}

export function isSetMtimePlan(plan: PlanResult): plan is SetMtimePlan {
  return plan.action.kind === 'set_mtime' && 'summary' in plan
}

export function isDateOrganizePlan(plan: PlanResult): plan is DateOrganizePlan {
  return plan.action.kind === 'date_organize' && 'summary' in plan
}

// defaultTemplate is the folders of an organize by date unless chosen.
export const defaultTemplate = '{year}/{month}'

// maxShiftS is the longest shift a correction takes: 50 years.
export const maxShiftS = 1_577_880_000

// maxFolders is the most folders one request may name.
export const maxFolders = 100

// The shift units the interface offers (D8): a year is 365 days and a day
// 24 hours, so "+1 year 3 hours" is 31,546,800 seconds.
export const shiftUnits = { years: 365 * 86_400, days: 86_400, hours: 3_600, minutes: 60 } as const

export type ShiftUnit = keyof typeof shiftUnits

export type ShiftParts = Record<ShiftUnit, number>

// shiftSeconds is the shift in seconds of whole units, later or earlier.
export function shiftSeconds(parts: ShiftParts, later: boolean): number {
  const seconds = (Object.keys(shiftUnits) as ShiftUnit[]).reduce(
    (sum, unit) => sum + parts[unit] * shiftUnits[unit],
    0,
  )
  return later ? seconds : -seconds
}

// shiftParts splits a shift into the interface's units, largest first;
// seconds below a minute are left out.
export function shiftParts(shiftS: number): ShiftParts {
  let rest = Math.abs(shiftS)
  const parts = { years: 0, days: 0, hours: 0, minutes: 0 }
  for (const unit of Object.keys(shiftUnits) as ShiftUnit[]) {
    parts[unit] = Math.floor(rest / shiftUnits[unit])
    rest -= parts[unit] * shiftUnits[unit]
  }
  return parts
}

const localLengths: Record<Precision, number> = { year: 4, month: 7, day: 10, second: 19 }

// shiftLocal moves a local date by a shift and keeps its precision: a date
// coarser than a second starts its new period, as the server derives it.
export function shiftLocal(local: LocalDate, precision: Precision, shiftS: number): LocalDate {
  const shifted = new Date(localMillis(local) + shiftS * 1000)
  const year = String(shifted.getUTCFullYear()).padStart(4, '0')
  const two = (n: number) => String(n).padStart(2, '0')
  const full = `${year}-${two(shifted.getUTCMonth() + 1)}-${two(shifted.getUTCDate())}T${two(shifted.getUTCHours())}:${two(shifted.getUTCMinutes())}:${two(shifted.getUTCSeconds())}`
  return full.slice(0, localLengths[precision])
}

// datesQueryRoot prefixes every cached dates response, the entries' dates
// included, so a correction or a media job's end refetches them all;
// mediaProgressRoot the latest progress of each source's media job, which
// only events bring.
export const datesQueryRoot = ['dates'] as const
export const mediaProgressRoot = ['media-progress'] as const

export function datesSummaryQueryKey(source: string | null) {
  return [...datesQueryRoot, 'summary', source] as const
}

export function datesListQueryKey(filter: DatesFilter) {
  return [
    ...datesQueryRoot,
    'list',
    filter.source,
    filter.flag,
    filter.dateSource,
    filter.camera,
    filter.within,
  ] as const
}

export function camerasQueryKey(source: string) {
  return [...datesQueryRoot, 'cameras', source] as const
}

export function entryDatesQueryKey(id: string) {
  return [...datesQueryRoot, 'entry', id] as const
}

export function mediaProgressKey(source: string) {
  return [...mediaProgressRoot, source] as const
}

export function fetchDatesSummary(source: string | null, signal?: AbortSignal): Promise<DatesSummary> {
  const query = source === null ? '' : `?${new URLSearchParams({ source })}`
  return apiGet<DatesSummary>(`/api/dates/summary${query}`, signal)
}

export function fetchDates(filter: DatesFilter, cursor: string | null, signal?: AbortSignal): Promise<DatesPage> {
  const params = new URLSearchParams({ source: filter.source })
  if (filter.flag !== null) {
    params.set('flag', filter.flag)
  }
  if (filter.dateSource !== null) {
    params.set('date_source', filter.dateSource)
  }
  if (filter.camera !== null) {
    params.set('camera', filter.camera)
  }
  if (filter.within !== null) {
    params.set('within', filter.within)
  }
  if (cursor !== null) {
    params.set('cursor', cursor)
  }
  return apiGet<DatesPage>(`/api/dates?${params}`, signal)
}

export function fetchCameras(source: string, signal?: AbortSignal): Promise<CamerasResponse> {
  return apiGet<CamerasResponse>(`/api/dates/cameras?${new URLSearchParams({ source })}`, signal)
}

export function fetchEntryDates(id: string, signal?: AbortSignal): Promise<{ dates: EntryDates | null }> {
  return apiGet<{ dates: EntryDates | null }>(`/api/entries/${encodeURIComponent(id)}/dates`, signal)
}

export function setDateCorrection(
  targets: DateTargets,
  correction: Correction,
  csrfToken: string,
): Promise<SetCorrectionResult> {
  return postCommand<SetCorrectionResult>('set-date-correction', { ...targets, correction }, csrfToken)
}

export function clearDateCorrection(targets: DateTargets, csrfToken: string): Promise<ClearCorrectionResult> {
  return postCommand<ClearCorrectionResult>('clear-date-correction', targets, csrfToken)
}

export function planSetMtime(targets: BulkTargets, csrfToken: string): Promise<SetMtimePlan> {
  return postCommand<SetMtimePlan>('plan-set-mtime', targets, csrfToken)
}

export interface DateOrganizeOptions {
  destinationId: string
  template: string
  rename: boolean
}

export function planDateOrganize(
  targets: BulkTargets,
  { destinationId, template, rename }: DateOrganizeOptions,
  csrfToken: string,
): Promise<DateOrganizePlan> {
  return postCommand<DateOrganizePlan>(
    'plan-date-organize',
    { ...targets, destination_id: destinationId, template, rename },
    csrfToken,
  )
}

// MediaJob is the latest event of a source's media job while it runs.
export interface MediaJob {
  job_id: string
  state: JobState
  progress: Progress
}

// refreshAfterCorrection refetches every dates response a correction
// changes: the summary, the list, the cameras, and the entries' dates.
export async function refreshAfterCorrection(queryClient: QueryClient) {
  await queryClient.invalidateQueries({ queryKey: datesQueryRoot })
}

// applyJobEventToDates keeps the Dates screen live: a media job's progress
// is kept for its source, and its end refetches every dates response. An
// organize job's end refetches them through the history (refreshAfterMove).
export function applyJobEventToDates(queryClient: QueryClient, event: JobEvent) {
  if (event.kind !== mediaKind || event.source_id === undefined) {
    return
  }
  const key = mediaProgressKey(event.source_id)
  if (isTerminal(event.state)) {
    queryClient.setQueryData<MediaJob | null>(key, null)
    void queryClient.invalidateQueries({ queryKey: datesQueryRoot })
    return
  }
  const job: MediaJob = { job_id: event.job_id, state: displayState(event), progress: event.progress }
  queryClient.setQueryData<MediaJob | null>(key, job)
}
