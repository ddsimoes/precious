import type { QueryClient } from '@tanstack/react-query'

import { compareQueryRoot, opportunitiesQueryRoot } from '@/api/content'
import { entriesQueryRoot, type EntryRow } from '@/api/entries'
import { homeQueryRoot } from '@/api/home'
import { isTerminal, organizeKind, type JobEvent, type JobState } from '@/api/jobs'
import type { ReviewListName } from '@/api/opportunities'
import { searchQueryRoot } from '@/api/search'
import { sourcesQueryKey } from '@/api/sources'
import { apiGet, postCommand } from '@/app/api'

// Organizing (R3 design Interfaces): every change to a source is an action
// that a plan-* command plans and run-action runs in an organize job. The
// history lists the actions that were run, with their items. R4 adds the
// cleanup kinds: a cleanup plan moves discarded items to quarantine, a
// restore brings them back, and a purge deletes a checked set for good.

export type ActionKind =
  | 'move'
  | 'rename'
  | 'create_folder'
  | 'rescue'
  | 'merge'
  | 'undo'
  | 'cleanup'
  | 'restore'
  | 'purge'

// cleanupKinds are the kinds History offers no Undo for: a cleanup is
// reversed by restoring its items, and a purge cannot be reversed.
export const cleanupKinds: readonly ActionKind[] = ['cleanup', 'restore', 'purge']

export type ActionState = 'planned' | 'queued' | 'running' | 'done' | 'stopped' | 'expired'

// ItemState is where one step of an action stands: planned, refused or in
// conflict when planned, blocked by a kept entry inside a cleanup item, its
// intent recorded while it runs, then how it ended.
export type ItemState =
  | 'planned'
  | 'refused'
  | 'conflict'
  | 'blocked'
  | 'intent'
  | 'done'
  | 'not_permitted'
  | 'offline'
  | 'changed'
  | 'failed'
  | 'no_safe_rename'
  | 'not_empty'
  | 'manual_recovery'
  | 'not_attempted'
  | 'resolved'

// ItemReason says why an item was refused, is a conflict, is blocked, or
// changed.
export type ItemReason =
  | 'other_source'
  | 'inside_archive'
  | 'missing'
  | 'source_root'
  | 'into_itself'
  | 'already_there'
  | 'other_filesystem'
  | 'contains_mount'
  | 'name_taken'
  | 'name_taken_in_plan'
  | 'name_taken_by_missing'
  | 'previous_folder_gone'
  | 'would_lose_keep'
  | 'already_undone'
  | 'holds_kept'
  | 'last_copy'
  | 'both_sides'
  | 'no_verified_copy'
  | 'identity_changed'
  | 'decision_changed'
  | 'in_quarantine'
  | 'unreadable'
  | 'writes_off'
  | 'check_stale'
  | 'file_changed'
  | 'copy_changed'

// Found is what was at one name when a step could not be confirmed: nothing,
// the entry expected, or something else.
export type Found = 'absent' | 'same' | 'other'

export interface ItemPath {
  path: string
  path_b64: string
}

export interface Action {
  id: string
  kind: ActionKind
  source_id: string
  state: ActionState
  created_at: string
  expires_at: string | null
  started_at: string | null
  finished_at: string | null
  destination: EntryRow | null
  job_id: string | null
  undo_of: string | null
  bulk: boolean
  counts: Record<ItemState, number>
  // entries counts, for a cleanup, restore, or purge, only the steps that
  // stand for an entry (its rename, or its purge), by state.
  entries?: Record<ItemState, number>
  bytes: number
  files: number
  // kept_lost counts the items whose effective decision goes from keep to
  // another value (R3 design D14).
  kept_lost: number
  reversed: number
  undo: {
    possible: boolean
    reason: 'not_done' | 'already_undone' | 'nothing_done' | 'not_undoable_kind' | null
  }
  // The R4 fields: what a cleanup plan was drafted from (the discards, or
  // the duplicates list's rules, and the review list), the check a purge
  // deletes, and what a purge deleted and freed.
  ground: 'discard' | 'duplicate' | null
  list: ReviewListName | null
  check_id: string | null
  deleted_files: number
  deleted_bytes: number
  freed_bytes: number
}

// ItemOp is what one step does. R4 adds writing an origin record, removing
// one, deleting a checked item for good, and comparing a checked set with
// the disk before a purge.
export type ItemOp = 'rename' | 'mkdir' | 'rmdir' | 'record' | 'unlink' | 'purge' | 'verify'

export interface Item {
  id: string
  seq: number
  op: ItemOp
  entry: EntryRow | null
  from: ItemPath | null
  to: ItemPath | null
  state: ItemState
  reason: ItemReason | null
  decision_after: 'undecided' | 'keep' | 'discard' | 'later' | null
  // detail is the system's error text of a failed item.
  detail: string | null
  // found is what a manual_recovery item found at its old and new name.
  found: { from: Found; to: Found } | null
  reversed: boolean
  bytes: number
  files: number
  // kept_count is how many kept entries block a blocked cleanup item.
  kept_count?: number
}

// PlanResult is the 201 answer of every plan-* command: the planned action
// and the first page of its items.
export interface PlanResult {
  action: Action
  items: Item[]
  next_cursor: string | null
}

export interface RunResult {
  action: Action
  job_id: string
  state: JobState
}

export interface HistoryPage {
  items: Action[]
  next_cursor: string | null
}

export interface ItemsPage {
  items: Item[]
  next_cursor: string | null
}

export interface FoldersPage {
  items: EntryRow[]
  next_cursor: string | null
}

// MoveTargets names what plan-move moves: one entry (an individual move),
// or explicit IDs or a selection (a bulk move).
export type MoveTargets = { entry_id: string } | { entry_ids: string[] } | { selection_id: string }

export function planMove(targets: MoveTargets, destinationId: string, csrfToken: string): Promise<PlanResult> {
  return postCommand<PlanResult>('plan-move', { ...targets, destination_id: destinationId }, csrfToken)
}

export function planRename(entryId: string, name: string, csrfToken: string): Promise<PlanResult> {
  return postCommand<PlanResult>('plan-rename', { entry_id: entryId, name }, csrfToken)
}

export function planCreateFolder(parentId: string, name: string, csrfToken: string): Promise<PlanResult> {
  return postCommand<PlanResult>('plan-create-folder', { parent_id: parentId, name }, csrfToken)
}

export function planRescue(folderId: string, destinationId: string, csrfToken: string): Promise<PlanResult> {
  return postCommand<PlanResult>('plan-rescue', { folder_id: folderId, destination_id: destinationId }, csrfToken)
}

export function planMerge(
  leftId: string,
  rightId: string,
  from: 'left' | 'right',
  csrfToken: string,
): Promise<PlanResult> {
  return postCommand<PlanResult>('plan-merge', { left_id: leftId, right_id: rightId, from }, csrfToken)
}

// planUndo plans the reverse of a done action; with a destination, the
// items whose previous place is taken or gone go there instead.
export function planUndo(actionId: string, destinationId: string | null, csrfToken: string): Promise<PlanResult> {
  const body = destinationId === null ? { action_id: actionId } : { action_id: actionId, destination_id: destinationId }
  return postCommand<PlanResult>('plan-undo', body, csrfToken)
}

export function runAction(actionId: string, csrfToken: string): Promise<RunResult> {
  return postCommand<RunResult>('run-action', { action_id: actionId }, csrfToken)
}

export function cancelAction(actionId: string, csrfToken: string): Promise<{ action: Action }> {
  return postCommand<{ action: Action }>('cancel-action', { action_id: actionId }, csrfToken)
}

// ResolveResult is the answer of resolve-recovery: the action, and the scan
// of its source that the server started.
export interface ResolveResult {
  action: Action
  scan: { job_id: string; coalesced: boolean }
}

// resolveRecovery marks an item the owner checked by hand as resolved; the
// server starts a scan of its source.
export function resolveRecovery(itemId: string, csrfToken: string): Promise<ResolveResult> {
  return postCommand<ResolveResult>('resolve-recovery', { item_id: itemId }, csrfToken)
}

// historyQueryRoot prefixes every cached history response.
export const historyQueryRoot = ['history'] as const

export function historyQueryKey() {
  return [...historyQueryRoot, 'list'] as const
}

export function actionQueryKey(id: string) {
  return [...historyQueryRoot, id] as const
}

// ItemsFilter limits an action's items to some states and some ops; empty
// lists every one.
export interface ItemsFilter {
  states?: readonly ItemState[]
  ops?: readonly ItemOp[]
}

export function actionItemsQueryKey(id: string, { states = [], ops = [] }: ItemsFilter = {}) {
  return [...historyQueryRoot, id, 'items', ...states, ...ops] as const
}

// foldersQueryKey is under the entries root, so a move or a new folder
// refetches it.
export function foldersQueryKey(id: string) {
  return [...entriesQueryRoot, id, 'folders'] as const
}

export function fetchHistory(cursor: string | null, signal?: AbortSignal): Promise<HistoryPage> {
  const params = new URLSearchParams()
  if (cursor !== null) {
    params.set('cursor', cursor)
  }
  const query = params.toString()
  return apiGet<HistoryPage>(query === '' ? '/api/history' : `/api/history?${query}`, signal)
}

export function fetchAction(id: string, signal?: AbortSignal): Promise<Action> {
  return apiGet<Action>(`/api/history/${encodeURIComponent(id)}`, signal)
}

export function fetchActionItems(
  id: string,
  { states = [], ops = [] }: ItemsFilter,
  cursor: string | null,
  signal?: AbortSignal,
): Promise<ItemsPage> {
  const params = new URLSearchParams()
  for (const state of states) {
    params.append('state', state)
  }
  for (const op of ops) {
    params.append('op', op)
  }
  if (cursor !== null) {
    params.set('cursor', cursor)
  }
  const query = params.toString()
  return apiGet<ItemsPage>(`/api/history/${encodeURIComponent(id)}/items${query === '' ? '' : `?${query}`}`, signal)
}

// entryOps are the steps that stand for an entry in a cleanup, restore, or
// purge, one per entry; null for the other kinds, which list every step.
export function entryOps(kind: ActionKind): readonly ItemOp[] | null {
  if (kind === 'cleanup' || kind === 'restore') {
    return ['rename']
  }
  return kind === 'purge' ? ['purge'] : null
}

// exportUrl is the CSV of an action's items (R4 design D16), as an
// attachment.
export function exportUrl(id: string): string {
  return `/api/history/${encodeURIComponent(id)}/export.csv`
}

// fetchFolders lists the folders inside a folder by name, for the
// destination chooser (archives are left out).
export function fetchFolders(id: string, cursor: string | null, signal?: AbortSignal): Promise<FoldersPage> {
  const params = new URLSearchParams({ kind: 'directory', sort: 'name', order: 'asc' })
  if (cursor !== null) {
    params.set('cursor', cursor)
  }
  return apiGet<FoldersPage>(`/api/entries/${encodeURIComponent(id)}/children?${params}`, signal)
}

// needsPreview reports whether a plan must be shown before it runs (R3
// design D9): a bulk action, or one with a conflict, a refused item, a kept
// item that would no longer be kept, or nothing to run. A single action
// without any runs at once.
export function needsPreview(action: Action): boolean {
  const c = action.counts
  return action.bulk || c.conflict > 0 || c.refused > 0 || action.kept_lost > 0 || c.planned === 0
}

// isActive reports whether an action is waiting for its turn or running.
export function isActive(action: Action): boolean {
  return action.state === 'queued' || action.state === 'running'
}

// refreshAfterMove refetches what a done move, rename, or new folder makes
// stale: every entry and folder listing, search results, Compare, the
// opportunity cards and lists, Home, the sources' totals, the history, and
// the Map's start, whose chain of single folders may have changed.
export async function refreshAfterMove(queryClient: QueryClient) {
  const roots = [
    entriesQueryRoot,
    searchQueryRoot,
    compareQueryRoot,
    opportunitiesQueryRoot,
    homeQueryRoot,
    sourcesQueryKey,
    historyQueryRoot,
    ['map-start'],
  ]
  await Promise.all(roots.map((queryKey) => queryClient.invalidateQueries({ queryKey })))
}

// applyJobEventToHistory keeps the history live: every organize event
// refetches it, and one that ends refetches everything a move changes.
export function applyJobEventToHistory(queryClient: QueryClient, event: JobEvent) {
  if (event.kind !== organizeKind) {
    return
  }
  if (isTerminal(event.state)) {
    void refreshAfterMove(queryClient)
  } else {
    void queryClient.invalidateQueries({ queryKey: historyQueryRoot })
  }
}
