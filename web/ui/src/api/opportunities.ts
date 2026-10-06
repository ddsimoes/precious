import { opportunitiesQueryRoot, type Copy, type Coverage, type Relation } from '@/api/content'
import type { Selection } from '@/api/decisions'
import type { Category, EntryRow } from '@/api/entries'
import { apiGet, postCommand } from '@/app/api'

// Opportunity cards and their review lists (R2 design D12, D13; GET
// /api/opportunities, GET /api/opportunities/{list}, command select-list).

export type ReviewListName =
  | 'duplicates'
  | 'unpacked_archives'
  | 'system_junk'
  | 'installers'
  | 'programs'
  | 'caches'
  | 'leftovers'

export const reviewLists: ReviewListName[] = [
  'duplicates',
  'unpacked_archives',
  'system_junk',
  'installers',
  'programs',
  'caches',
  'leftovers',
]

export function isReviewList(value: string | undefined): value is ReviewListName {
  return reviewLists.some((list) => list === value)
}

// Basis says what a card rests on: the classification rules, or the same
// content found by hashing.
export type Basis = 'rules' | 'content'

// Card is CardJSON: a card's open rows, with their bytes.
export interface Card {
  list: ReviewListName
  bytes: number
  rows: number
  basis: Basis
}

export interface Opportunities {
  cards: Card[]
  coverage: Coverage
  computed_at: string | null
}

// RowSummary holds what the one-line summary of a row is built from (D13):
// signals are traits (contains_vcs) or indicator signals (database_present).
export interface RowSummary {
  category: Category | null
  years: [number | null, number | null] | null
  files: number
  bytes: number
  signals: string[]
}

// ReviewRow is RowJSON. An entry row has its entry; a duplicates row is a
// folder relation (its entry is side a, relation.other side b) or a group of
// copies of one content.
export interface ReviewRow {
  id: string
  bytes: number
  files: number
  entry: EntryRow | null
  relation: Relation | null
  copies: Copy[] | null
  summary: RowSummary
}

export interface ReviewPage {
  card: Card
  items: ReviewRow[]
  next_cursor: string | null
}

export function opportunitiesQueryKey(source: string | null) {
  return [...opportunitiesQueryRoot, 'cards', source] as const
}

export function reviewQueryKey(list: ReviewListName, source: string | null, decided: boolean) {
  return [...opportunitiesQueryRoot, 'list', list, source, decided] as const
}

export function fetchOpportunities(source: string | null, signal?: AbortSignal): Promise<Opportunities> {
  const query = source === null ? '' : `?${new URLSearchParams({ source })}`
  return apiGet<Opportunities>(`/api/opportunities${query}`, signal)
}

export function fetchReviewPage(
  list: ReviewListName,
  source: string | null,
  decided: boolean,
  cursor: string | null,
  signal?: AbortSignal,
): Promise<ReviewPage> {
  const params = new URLSearchParams({ decided: decided ? '1' : '0' })
  if (source !== null) {
    params.set('source', source)
  }
  if (cursor !== null) {
    params.set('cursor', cursor)
  }
  return apiGet<ReviewPage>(`/api/opportunities/${list}?${params}`, signal)
}

// SelectableList is a list whose open rows select-list resolves: every
// review list but duplicates, and the Gems sections.
export type SelectableList =
  | Exclude<ReviewListName, 'duplicates'>
  | 'gems_unique'
  | 'gems_rescue'
  | 'gems_only_in_copy'

// selectList makes a selection of the entries of a list's open rows, as
// create-selection does for a search.
export function selectList(list: SelectableList, source: string | null, csrfToken: string): Promise<Selection> {
  return postCommand<Selection>('select-list', source === null ? { list } : { list, source_id: source }, csrfToken)
}
