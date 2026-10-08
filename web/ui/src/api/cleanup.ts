import type { QueryClient } from '@tanstack/react-query'

import type { EntryRow } from '@/api/entries'
import type { Amount } from '@/api/home'
import { isTerminal, organizeKind, type JobEvent, type Progress } from '@/api/jobs'
import type { ReviewListName } from '@/api/opportunities'
import { historyQueryRoot, refreshAfterMove, type ItemPath, type PlanResult } from '@/api/organize'
import { apiGet, postCommand } from '@/app/api'

// Cleanup (R4 design Interfaces): a cleanup plan moves discarded items to
// their source's quarantine, a restore brings them back, a check reads a
// chosen set of quarantined items and looks for a copy of every file, and a
// purge deletes a checked set for good. Plans run through run-action and
// cancel-action, as organizing does.

// purgeCheckKind is the job that checks a purge set (D7). Its progress
// holds `files` and `bytes` read so far, of `of_files` and `of_bytes`.
export const purgeCheckKind = 'purge_check'

// Summary is what plan-cleanup reports from the index: the bytes with a
// known copy outside the plan and the quarantine, the bytes with no known
// copy, the bytes not checked for copies yet, and the items that hold
// personal material.
export interface CleanupSummary {
  with_copy_bytes: number
  no_copy_bytes: number
  unchecked_bytes: number
  personal_items: number
}

// CleanupPlan is the 201 answer of plan-cleanup.
export interface CleanupPlan extends PlanResult {
  summary: CleanupSummary
}

// isCleanupPlan tells a plan-cleanup answer, with its summary, from the
// other plans.
export function isCleanupPlan(plan: PlanResult): plan is CleanupPlan {
  return 'summary' in plan
}

export type CheckState = 'running' | 'ready' | 'failed' | 'stale'

// Verdict is what a check found for one recorded entry (D7): a copy read
// and verified, a copy only on a disk that is not connected, no copy, a
// member that could not be read, an archive whose insides were not opened,
// or nothing to copy (a folder, a link, an empty file).
export type Verdict = 'safe' | 'copy_offline' | 'unique' | 'unreadable' | 'opaque_archive' | 'no_content'

export const verdicts: Verdict[] = ['safe', 'copy_offline', 'unique', 'unreadable', 'opaque_archive', 'no_content']

// CheckClass ranks a file with no copy by the rules (D8).
export type CheckClass = 'possibly_valuable' | 'likely_junk' | 'uncertain'

export const checkClasses: CheckClass[] = ['possibly_valuable', 'uncertain', 'likely_junk']

// CheckCounts has a bucket per verdict, and per class for the files with
// no copy.
export interface CheckCounts {
  verdict: Record<Verdict, Amount>
  class: Record<CheckClass, Amount>
}

export interface Check {
  id: string
  source_id: string
  state: CheckState
  // job_id is the purge_check job, whose events carry its progress.
  job_id: string | null
  created_at: string
  finished_at: string | null
  stale_reason: string | null
  // items is how many quarantined items the set holds.
  items: number
  counts: CheckCounts
  // confirmed and unconfirmed are the files that need a confirmation:
  // confirmed already, and not yet (the likely junk counts as unconfirmed
  // until its group is confirmed).
  confirmed: Amount
  unconfirmed: Amount
  junk_confirmed: boolean
  // allowed is set when every file is safe or confirmed: the purge may be
  // planned.
  allowed: boolean
}

export interface CheckFile {
  id: string
  // item is the quarantined item the file is in.
  item: EntryRow
  // entry_id is null for an archive member, which cannot be moved out.
  entry_id: string | null
  path: string
  path_b64: string
  member: string | null
  kind: string
  size: number
  verdict: Verdict
  class: CheckClass | null
  copy: { source_id: string; path: string; path_b64: string; hard_link: boolean } | null
  confirmed: boolean
}

export interface CheckFilesPage {
  items: CheckFile[]
  next_cursor: string | null
}

// CheckFilesFilter limits a check's files to a verdict, a class, and those
// confirmed or not.
export interface CheckFilesFilter {
  verdict: Verdict | null
  class: CheckClass | null
  confirmed: boolean | null
}

// Quarantined is one item of a source's quarantine. original is where it
// came from, null when unknown (an item found there by a scan).
export interface Quarantined {
  entry: EntryRow
  original: ItemPath | null
  plan_id: string | null
  quarantined_at: string | null
  bytes: number
  files: number
  check: { id: string; state: CheckState } | null
}

export interface QuarantinePage {
  items: Quarantined[]
  next_cursor: string | null
  total: Amount
}

export interface KeptPage {
  count: number
  items: EntryRow[]
  next_cursor: string | null
}

// needsOwnConfirmation reports whether a file must be confirmed one by one
// (D8): no copy and not likely junk (which its group confirms), a copy only
// on a disk that is not connected, a member that could not be read, or an
// archive not opened with no copy of its own.
export function needsOwnConfirmation(file: CheckFile): boolean {
  switch (file.verdict) {
    case 'unique':
      return file.class !== 'likely_junk'
    case 'copy_offline':
    case 'unreadable':
      return true
    case 'opaque_archive':
      return file.copy === null
    default:
      return false
  }
}

export function planCleanup(sourceId: string, list: ReviewListName | null, csrfToken: string): Promise<CleanupPlan> {
  const body = list === null ? { source_id: sourceId } : { source_id: sourceId, list }
  return postCommand<CleanupPlan>('plan-cleanup', body, csrfToken)
}

// RestoreTargets names what plan-restore brings back: quarantined items, or
// every item of a plan.
export type RestoreTargets = { entry_ids: string[] } | { plan_id: string }

// planRestore plans quarantined items back to their original places; with a
// destination, the items whose place is taken or gone go there instead.
export function planRestore(
  targets: RestoreTargets,
  destinationId: string | null,
  csrfToken: string,
): Promise<PlanResult> {
  const body = destinationId === null ? targets : { ...targets, destination_id: destinationId }
  return postCommand<PlanResult>('plan-restore', body, csrfToken)
}

export interface CheckStarted {
  check_id: string
  job_id: string
}

export function checkPurge(entryIds: string[], csrfToken: string): Promise<CheckStarted> {
  return postCommand<CheckStarted>('check-purge', { entry_ids: entryIds }, csrfToken)
}

// ConfirmTargets names what confirm-purge confirms: files one by one, or
// the likely junk group.
export type ConfirmTargets = { file_ids: string[] } | { group: 'likely_junk' }

export function confirmPurge(checkId: string, targets: ConfirmTargets, csrfToken: string): Promise<{ check: Check }> {
  return postCommand<{ check: Check }>('confirm-purge', { check_id: checkId, ...targets }, csrfToken)
}

export function planPurge(checkId: string, csrfToken: string): Promise<PlanResult> {
  return postCommand<PlanResult>('plan-purge', { check_id: checkId }, csrfToken)
}

// cancelJob asks a job to stop: here, a check that is running.
export function cancelJob(jobId: string, csrfToken: string): Promise<unknown> {
  return postCommand<unknown>('cancel-job', { job_id: jobId }, csrfToken)
}

// quarantineQueryRoot and checksQueryRoot prefix the cached quarantine
// listings and checks; checkProgressRoot the latest progress of each
// check's job, which only events bring.
export const quarantineQueryRoot = ['quarantine'] as const
export const checksQueryRoot = ['checks'] as const
export const checkProgressRoot = ['check-progress'] as const

export function quarantineQueryKey(source: string) {
  return [...quarantineQueryRoot, source] as const
}

export function checkQueryKey(id: string) {
  return [...checksQueryRoot, id] as const
}

export function checkFilesQueryKey(id: string, filter: CheckFilesFilter) {
  return [...checksQueryRoot, id, 'files', filter.verdict, filter.class, filter.confirmed] as const
}

export function checkProgressKey(jobId: string) {
  return [...checkProgressRoot, jobId] as const
}

// keptQueryKey is under the history root's action, so it goes with it.
export function keptQueryKey(actionId: string, itemId: string) {
  return [...historyQueryRoot, actionId, 'kept', itemId] as const
}

// quarantinePageSize is the most items one quarantine page holds.
const quarantinePageSize = 500

export function fetchQuarantine(
  source: string,
  cursor: string | null,
  signal?: AbortSignal,
  limit?: number,
): Promise<QuarantinePage> {
  const params = new URLSearchParams({ source })
  if (cursor !== null) {
    params.set('cursor', cursor)
  }
  if (limit !== undefined) {
    params.set('limit', String(limit))
  }
  return apiGet<QuarantinePage>(`/api/quarantine?${params}`, signal)
}

// fetchCheckSet lists the items of a source's quarantine that a check
// holds, reading every page: the set to check again once it went stale,
// without the items restored since.
export async function fetchCheckSet(source: string, checkId: string): Promise<string[]> {
  const ids: string[] = []
  let cursor: string | null = null
  do {
    const page: QuarantinePage = await fetchQuarantine(source, cursor, undefined, quarantinePageSize)
    for (const item of page.items) {
      if (item.check?.id === checkId) {
        ids.push(item.entry.id)
      }
    }
    cursor = page.next_cursor
  } while (cursor !== null)
  return ids
}

export function fetchCheck(id: string, signal?: AbortSignal): Promise<Check> {
  return apiGet<Check>(`/api/checks/${encodeURIComponent(id)}`, signal)
}

export function fetchCheckFiles(
  id: string,
  filter: CheckFilesFilter,
  cursor: string | null,
  signal?: AbortSignal,
): Promise<CheckFilesPage> {
  const params = new URLSearchParams()
  if (filter.verdict !== null) {
    params.set('verdict', filter.verdict)
  }
  if (filter.class !== null) {
    params.set('class', filter.class)
  }
  if (filter.confirmed !== null) {
    params.set('confirmed', filter.confirmed ? '1' : '0')
  }
  if (cursor !== null) {
    params.set('cursor', cursor)
  }
  const query = params.toString()
  return apiGet<CheckFilesPage>(`/api/checks/${encodeURIComponent(id)}/files${query === '' ? '' : `?${query}`}`, signal)
}

// fetchKept lists the kept entries that block a cleanup item, 100 to a
// page.
export function fetchKept(actionId: string, itemId: string, cursor: string | null, signal?: AbortSignal): Promise<KeptPage> {
  const query = cursor === null ? '' : `?${new URLSearchParams({ cursor })}`
  return apiGet<KeptPage>(
    `/api/history/${encodeURIComponent(actionId)}/items/${encodeURIComponent(itemId)}/kept${query}`,
    signal,
  )
}

// refreshAfterCleanup refetches what a cleanup, restore, or purge makes
// stale: everything a move does, the quarantine listings, and the checks.
export async function refreshAfterCleanup(queryClient: QueryClient) {
  await Promise.all([
    refreshAfterMove(queryClient),
    queryClient.invalidateQueries({ queryKey: quarantineQueryRoot }),
    queryClient.invalidateQueries({ queryKey: checksQueryRoot }),
  ])
}

// applyJobEventToCleanup keeps the Cleanup screen live. A check's job
// progress is kept for its report and refetches the checks themselves,
// whose counts grow as it runs, but not their file lists; its end
// refetches every check response and the quarantine, whose items show
// their check. An organize job that ends (a cleanup, restore, purge, or
// move out) refetches the quarantine and the checks it may have made
// stale; the history refetches the rest.
export function applyJobEventToCleanup(queryClient: QueryClient, event: JobEvent) {
  if (event.kind === purgeCheckKind) {
    queryClient.setQueryData<Progress>(checkProgressKey(event.job_id), event.progress)
    if (isTerminal(event.state)) {
      void queryClient.invalidateQueries({ queryKey: checksQueryRoot })
      void queryClient.invalidateQueries({ queryKey: quarantineQueryRoot })
    } else {
      void queryClient.invalidateQueries({ queryKey: checksQueryRoot, predicate: (query) => query.queryKey.length === 2 })
    }
    return
  }
  if (event.kind === organizeKind && isTerminal(event.state)) {
    void queryClient.invalidateQueries({ queryKey: quarantineQueryRoot })
    void queryClient.invalidateQueries({ queryKey: checksQueryRoot })
  }
}
