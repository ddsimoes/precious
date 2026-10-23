import type { QueryClient } from '@tanstack/react-query'

import type { ArchiveInfo, ArchiveNote, Coverage, EntryContent, Relation } from '@/api/content'
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

// ContentState is what hashing knows about a file or a file member (R2
// design D3).
export type ContentState = 'unique_size' | 'pending' | 'sampled' | 'hashed' | 'changed' | 'unreadable'

// ArchiveState is an archive file's listing outcome (R2 design D7).
export type ArchiveState =
  | 'listing'
  | 'complete'
  | 'partial'
  | 'rejected'
  | 'encrypted'
  | 'corrupt'
  | 'unsupported'
  | 'changed'
  | 'unreadable'

// Place is where the copies of an entry's content are (R5c design D1):
// only_copy has no other copy anywhere, this_source has copies only on its
// own source, and other_source has one on another source; unchecked is not
// checked yet, unreadable could not be read, none holds nothing with
// content, and not_computed is a folder or archive whose figures the next
// pass computes.
export type Place =
  | 'only_copy'
  | 'this_source'
  | 'other_source'
  | 'unchecked'
  | 'unreadable'
  | 'none'
  | 'not_computed'

// places lists every place, from most to least in need of keeping.
export const places: Place[] = [
  'only_copy',
  'this_source',
  'other_source',
  'unchecked',
  'unreadable',
  'none',
  'not_computed',
]

export interface PlaceAmount {
  files: number
  bytes: number
}

// PlaceSums are a folder's or archive's files and bytes by place; disposable
// files are counted apart, in disposable only (R5c design D14).
export interface PlaceSums {
  only_copy: PlaceAmount
  this_source: PlaceAmount
  other_source: PlaceAmount
  unchecked: PlaceAmount
  unreadable: PlaceAmount
  disposable: PlaceAmount
}

// OtherCopy names one copy of a file or member placed this_source or
// other_source (R5c design D9). ref is an entry ID or a member's "m<id>";
// archive_id is a member's archive, and then archive_path and member_path
// split its path into the archive's display path and its path inside the
// archive, which no '!' in path can tell apart. via_archive marks a
// member's copy named through its archive's coverage (D6); more is the
// number of other copies beyond the two the row and this copy make.
export type OtherCopy = {
  ref: string
  source_id: string
  path: string
  path_b64: string
  offline: boolean
  via_archive: boolean
  more: number
} & ({ archive_id: null } | { archive_id: string; archive_path: string; member_path: string })

// CoveredBy is the folder or archive that holds the most of a folder's or
// complete archive's content (R5c design D9).
export interface CoveredBy {
  kind: 'same' | 'inside'
  other: { id: string; source_id: string; path: string; path_b64: string }
}

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
  // The dates are null when unknown: none recorded, or at or before the Unix
  // epoch. A folder's newest and oldest leave such files out.
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
  // The R2 content fields (R2 design D16), null where they do not apply:
  // content_state and copies for files and file members; candidate_bytes,
  // checked_bytes, and duplicated_bytes for folders; archive_state for an
  // archive file (null when unlisted); archive_id for a member, whose id is
  // "m<id>".
  content_state: ContentState | null
  copies: number | null
  candidate_bytes: number | null
  checked_bytes: number | null
  duplicated_bytes: number | null
  archive_state: ArchiveState | null
  archive_id: string | null
  // The R5c place fields, sent only when places=1 is asked for (R5c design
  // Interfaces). place is null for a missing entry; place_offline is set for
  // a file or member placed other_source whose copies are all on sources
  // not connected; disposable is set for disposable files. places are the
  // sums of a folder, complete archive (its split), or member folder, and
  // null otherwise. member_places are a complete archive's unpacked member
  // sums, on the entry of the entry response only. other_copy and
  // covered_by come on children rows only.
  place?: Place | null
  place_offline?: boolean
  disposable?: boolean
  places?: PlaceSums | null
  member_places?: PlaceSums | null
  other_copy?: OtherCopy | null
  covered_by?: CoveredBy | null
}

export interface Ancestor {
  id: string
  name: string
  name_b64: string
  // only_child is true when the ancestor holds nothing but the next one
  // (the entry itself for the last), r2b design D9.
  only_child: boolean
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
  // owner is what the owner set (r2b design D5), null where the rules
  // decide; rules_category and rules_group are what the rules would set
  // (rules_category null for an entry the scan does not classify).
  owner: { category: Category | null; group: boolean | null }
  rules_category: Category | null
  rules_group: boolean
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
  // The R2 fields (R2 design D16): content for a file or file member,
  // up to 20 relations of a folder or archive, the archive of an archive
  // file Precious opened, and the coverage of every source together.
  content: EntryContent | null
  relations: Relation[]
  archive: ArchiveInfo | null
  coverage: Coverage
  // only_folder is the only child of a folder holding exactly one entry, a
  // folder (r2b design D9); archive_note says why an archive file has no
  // archive (D10).
  only_folder: string | null
  archive_note: ArchiveNote | null
  // in_quarantine is set for an entry in its source's quarantine (R4
  // design D13): the plan that put it there, when, and where it came from
  // (null when unknown).
  in_quarantine: InQuarantine | null
  // places_updating is set, when places=1 is asked for, while the places
  // may still change: relations are being recomputed (R5c design D8).
  places_updating?: boolean
}

export interface InQuarantine {
  plan_id: string | null
  quarantined_at: string | null
  original: { path: string; path_b64: string } | null
}

// only_here sorts by the bytes kept only on this source and needs places=1
// (R5c design D7). childSorts lists the sorts any listing takes, so an
// address read with childSorts alone falls back from only_here; a view with
// the places on accepts placesSorts as well.
export type ChildSort = 'bytes' | 'files' | 'newest' | 'name' | 'only_here'
export type SortOrder = 'desc' | 'asc'

export const childSorts: ChildSort[] = ['bytes', 'files', 'newest', 'name']

export const placesSorts: ChildSort[] = ['only_here']

// ChildrenView asks for the children's places (places=1) and, with them,
// hides the rows with nothing that needs keeping here (hide=other_source).
// hide means nothing without places, so it is sent only with them.
export interface ChildrenView {
  places: boolean
  hide: boolean
}

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

// The keys of entry responses asked for with places end in 'places', and
// children hiding rows in 'hide', so each view has its own cache while the
// key without them prefixes it.
export function entryQueryKey(id: string, places = false) {
  return places ? (['entries', id, 'detail', 'places'] as const) : (['entries', id, 'detail'] as const)
}

export function childrenQueryKey(id: string, sort: ChildSort, order: SortOrder, view?: ChildrenView) {
  const key = ['entries', id, 'children', sort, order] as const
  if (view?.places !== true) {
    return key
  }
  return view.hide ? ([...key, 'places', 'hide'] as const) : ([...key, 'places'] as const)
}

export function treemapQueryKey(id: string, places = false) {
  return places ? (['entries', id, 'treemap', 'places'] as const) : (['entries', id, 'treemap'] as const)
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

export function fetchEntry(id: string, signal?: AbortSignal, places = false): Promise<EntryDetail> {
  return apiGet<EntryDetail>(entryPath(id, places ? '?places=1' : ''), signal)
}

export function fetchChildren(
  id: string,
  sort: ChildSort,
  order: SortOrder,
  cursor: string | null,
  signal?: AbortSignal,
  view?: ChildrenView,
): Promise<ChildrenPage> {
  const params = new URLSearchParams({ sort, order })
  if (cursor !== null) {
    params.set('cursor', cursor)
  }
  if (view?.places === true) {
    params.set('places', '1')
    if (view.hide) {
      params.set('hide', 'other_source')
    }
  }
  return apiGet<ChildrenPage>(entryPath(id, `/children?${params}`), signal)
}

export function fetchTreemap(id: string, signal?: AbortSignal, places = false): Promise<Treemap> {
  return apiGet<Treemap>(entryPath(id, places ? '/treemap?places=1' : '/treemap'), signal)
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

// changeDates are a row's oldest and newest change: a folder's range, or a
// file's own last change at both ends. A null end is unknown.
export function changeDates(entry: Pick<EntryRow, 'kind' | 'mtime' | 'newest' | 'oldest'>): [string | null, string | null] {
  return entry.kind === 'directory' ? [entry.oldest, entry.newest] : [entry.mtime, entry.mtime]
}

// isMember reports whether a row is a member of an archive ("m<id>"), which
// has no decision or tags of its own: it is decided with its archive.
export function isMember(entry: Pick<EntryRow, 'archive_id'>): boolean {
  return entry.archive_id !== null
}

// isDrillable reports whether a row opens as a folder: a folder, a folder
// inside an archive, or an archive Precious read completely.
export function isDrillable(entry: Pick<EntryRow, 'kind' | 'archive_state'>): boolean {
  return entry.kind === 'directory' || entry.archive_state === 'complete'
}

// uncheckedStates are the content states of files not checked yet.
export const uncheckedStates: Partial<Record<ContentState, true>> = { pending: true, changed: true, unreadable: true }

// Duplication is the share of an entry's bytes that have another copy, and
// whether everything in it that could have a copy was checked.
export interface Duplication {
  fraction: number
  checked: boolean
}

// duplicationOf gives a folder its duplicated bytes over its size, and a
// file 0 or 100% from its copies (R2 design D10); null for what has no
// content to compare, such as an empty file or a link. A folder whose
// figures are not computed yet counts as not checked.
export function duplicationOf(entry: EntryRow): Duplication | null {
  if (entry.kind === 'directory') {
    const { candidate_bytes: candidate, checked_bytes: checked, duplicated_bytes: duplicated } = entry
    return {
      fraction: entry.total_bytes > 0 ? (duplicated ?? 0) / entry.total_bytes : 0,
      checked: candidate !== null && checked !== null && checked >= candidate,
    }
  }
  if (entry.content_state === null) {
    return null
  }
  if (uncheckedStates[entry.content_state]) {
    return { fraction: 0, checked: false }
  }
  return { fraction: (entry.copies ?? 1) > 1 ? 1 : 0, checked: true }
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
