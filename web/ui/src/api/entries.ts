import type { QueryClient } from '@tanstack/react-query'

import type { Decision, Family, FamilyAmount, FileKind, KindAmount, YearAmount } from '@/api/home'
import { isTerminal, scanKind, type JobEvent } from '@/api/jobs'
import { searchQueryRoot } from '@/api/search'
import { apiGet, apiProbe } from '@/app/api'

// Entries of the index (design Interfaces: EntryRow, GET /api/entries/{id},
// /children, /treemap, /text, and /content).

// EntryKind is a directory, file, or symlink, or a special file's platform
// kind.
export type EntryKind =
  | 'directory'
  | 'file'
  | 'symlink'
  | 'fifo'
  | 'socket'
  | 'char_device'
  | 'block_device'
  | 'unknown'

export type EntryState = 'present' | 'missing' | 'unreadable'

export type Category =
  | 'personal_media'
  | 'documents'
  | 'source_project'
  | 'application_user_data'
  | 'application_installation'
  | 'application_configuration'
  | 'os_installation'
  | 'installer_download'
  | 'system_junk'
  | 'cache'
  | 'temporary_data'
  | 'generated_artifacts'
  | 'download_collection'
  | 'backup'
  | 'mixed'
  | 'unknown'

// categories lists every category, grouped by family (spec §6.6).
export const categories: Category[] = [
  'personal_media',
  'documents',
  'source_project',
  'application_user_data',
  'application_installation',
  'application_configuration',
  'os_installation',
  'installer_download',
  'system_junk',
  'cache',
  'temporary_data',
  'generated_artifacts',
  'download_collection',
  'backup',
  'mixed',
  'unknown',
]

export const fileKinds: FileKind[] = [
  'image',
  'video',
  'audio',
  'document',
  'source',
  'archive',
  'installer',
  'executable',
  'system',
  'other',
]

export const families: Family[] = ['personal', 'programs', 'disposable', 'containers']

export const decisions: Decision[] = ['undecided', 'keep', 'discard', 'later']

export type Triage = 'keep' | 'discard' | 'review'

export const triages: Triage[] = ['keep', 'discard', 'review']

export type Trait =
  | 'contains_user_material'
  | 'contains_credentials'
  | 'contains_database'
  | 'contains_vcs'
  | 'possible_generated_content'

// EntryRow is the row of children, treemap, and search responses. name and
// path are escaped display strings; name_b64 and path_b64 are the raw bytes.
// Times are RFC 3339 or null.
export interface EntryRow {
  id: string
  source_id: string
  name: string
  name_b64: string
  path: string
  path_b64: string
  kind: EntryKind
  file_kind: FileKind | null
  main_kind: FileKind | null
  category: Category | null
  family: Family | null
  triage: Triage | null
  group: boolean
  veto: boolean
  size: number
  total_bytes: number
  total_files: number
  mtime: string | null
  newest: string | null
  oldest: string | null
  state: EntryState
  partial: boolean
  mount_boundary: boolean
  // decision is the entry's own decision; null means it inherits.
  decision: Decision | null
  eff_decision: Decision
  // tag_ids are the entry's own tags.
  tag_ids: number[]
  // composition is a folder's bytes and files by family, from its content,
  // or a file's one element under its file family (design D21). Families
  // holding nothing are omitted; the order is that of families. A server
  // older than D21 omits the field.
  composition?: FamilyAmount[]
}

export interface Ancestor {
  id: string
  name: string
  name_b64: string
}

// Ref names the entry a decision or a tag comes from.
export interface Ref {
  id: string
  path: string
  path_b64: string
}

export interface RuleExplanation {
  id: string
  explain: string
}

// Indicator is user material found inside a folder whose discard was vetoed.
export interface Indicator {
  entry_id: string
  path: string
  path_b64: string
  signal: string
}

export interface Classification {
  category: Category | null
  family: Family | null
  traits: Trait[]
  triage: Triage | null
  group: boolean
  veto: boolean
  rules: RuleExplanation[]
  indicators: Indicator[]
}

export interface EffectiveTag {
  id: number
  name: string
  own: boolean
  // from is the folder an inherited tag comes from; null for an own tag.
  from: Ref | null
}

export interface Intent {
  decision: Decision | null
  eff_decision: Decision
  // from is the entry whose own decision applies; null for the default.
  from: Ref | null
  tags: EffectiveTag[]
}

// InsideItem is a notable entry below a folder (design D21): a group, a
// folder mostly outside the folder's dominant family, or a file outside it.
export interface InsideItem {
  entry_id: string
  path: string
  path_b64: string
  category: Category | null
  family: Family | null
  group: boolean
  bytes: number
  files: number
}

export interface FolderStats {
  dirs: number
  files: number
  unreadable: number
  mount_boundaries: number
  by_kind: KindAmount[]
  by_year: YearAmount[]
  // inside lists up to 10 notable entries, largest first; a server older
  // than D21 omits it.
  inside?: InsideItem[]
}

export interface EntryDetail {
  entry: EntryRow
  ancestors: Ancestor[]
  classification: Classification
  intent: Intent
  // stats is null for anything but a folder.
  stats: FolderStats | null
}

export type ChildSort = 'bytes' | 'files' | 'newest' | 'name'
export type SortOrder = 'desc' | 'asc'

export const childSorts: ChildSort[] = ['bytes', 'files', 'newest', 'name']

export interface ChildrenPage {
  items: EntryRow[]
  next_cursor: string | null
}

export interface Treemap {
  entry: EntryRow
  // items are the largest children by bytes (at most 300).
  items: EntryRow[]
  // other holds the count and bytes of the remaining children.
  other: { count: number; bytes: number }
}

export interface TextContent {
  encoding: string
  text: string
  truncated: boolean
  language: string | null
  markdown: boolean
}

// entriesQueryRoot prefixes every cached entry response, so a change of
// decisions or tags refetches them together.
export const entriesQueryRoot = ['entries'] as const

export function entryQueryKey(id: string) {
  return ['entries', id, 'detail'] as const
}

export function childrenQueryKey(id: string, sort: ChildSort, order: SortOrder) {
  return ['entries', id, 'children', sort, order] as const
}

export function treemapQueryKey(id: string) {
  return ['entries', id, 'treemap'] as const
}

export function textQueryKey(id: string) {
  return ['entries', id, 'text'] as const
}

export function contentProbeQueryKey(id: string, attempt: number) {
  return ['entries', id, 'content', attempt] as const
}

function entryPath(id: string, rest = ''): string {
  return `/api/entries/${encodeURIComponent(id)}${rest}`
}

export function fetchEntry(id: string, signal?: AbortSignal): Promise<EntryDetail> {
  return apiGet<EntryDetail>(entryPath(id), signal)
}

export function fetchChildren(
  id: string,
  sort: ChildSort,
  order: SortOrder,
  cursor: string | null,
  signal?: AbortSignal,
): Promise<ChildrenPage> {
  const params = new URLSearchParams({ sort, order })
  if (cursor !== null) {
    params.set('cursor', cursor)
  }
  return apiGet<ChildrenPage>(entryPath(id, `/children?${params}`), signal)
}

export function fetchTreemap(id: string, signal?: AbortSignal): Promise<Treemap> {
  return apiGet<Treemap>(entryPath(id, '/treemap'), signal)
}

export function fetchText(id: string, signal?: AbortSignal): Promise<TextContent> {
  return apiGet<TextContent>(entryPath(id, '/text'), signal)
}

// contentUrl is the file's raw content, served with a type Precious chooses
// and a sandboxing Content-Security-Policy (design D12).
export function contentUrl(id: string): string {
  return entryPath(id, '/content')
}

// probeContent checks that the file's content can be served and returns the
// answer's status: a file changed on disk since the last scan fails with
// invalid_entry_state, and one on a disk that is not connected with
// source_offline.
export function probeContent(id: string, signal?: AbortSignal): Promise<number> {
  return apiProbe(contentUrl(id), signal)
}

// displayKind is the file kind of a file, or the main kind of a folder.
export function displayKind(entry: Pick<EntryRow, 'kind' | 'file_kind' | 'main_kind'>): FileKind | null {
  return entry.kind === 'directory' ? entry.main_kind : entry.file_kind
}

// lastChange is the newest modification inside a folder, or a file's own.
export function lastChange(entry: Pick<EntryRow, 'kind' | 'mtime' | 'newest'>): string | null {
  return entry.kind === 'directory' ? entry.newest : entry.mtime
}

// applyJobEventToEntries refetches every entry response and search result
// when a scan ends: sizes, states, and classifications may have changed.
export function applyJobEventToEntries(queryClient: QueryClient, event: JobEvent) {
  if (event.kind !== scanKind || !isTerminal(event.state)) {
    return
  }
  void queryClient.invalidateQueries({ queryKey: entriesQueryRoot })
  void queryClient.invalidateQueries({ queryKey: searchQueryRoot })
}
