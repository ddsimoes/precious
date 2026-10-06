import type { EntryRow } from '@/api/entries'
import { apiGet } from '@/app/api'

// GET /api/search (design D11). The Search screen keeps its filters in the
// address with the API's own parameter names, so the page's query string is
// the request's.

// searchListParams repeat, one value each; every other parameter is single.
export const searchListParams = ['ext', 'file_kind', 'category', 'tag', 'decision', 'triage'] as const
export const searchSingleParams = [
  'source',
  'name',
  'min_size',
  'max_size',
  'year_from',
  'year_to',
  'within',
  'sort',
  'order',
] as const

type ListParam = (typeof searchListParams)[number]
type SingleParam = (typeof searchSingleParams)[number]

// SearchCount is exact up to 10,000 and "10000+" beyond.
export type SearchCount = number | '10000+'

export interface SearchPage {
  items: EntryRow[]
  next_cursor: string | null
  count: SearchCount
}

// SelectionQuery is a search in the JSON form create-selection takes: the
// same names, with lists as arrays and numbers as numbers.
export type SelectionQuery = Partial<Record<ListParam, (string | number)[]>> &
  Partial<Record<SingleParam, string | number>>

const numericSingles: Partial<Record<SingleParam, true>> = {
  min_size: true,
  max_size: true,
  year_from: true,
  year_to: true,
}

export const searchQueryRoot = ['search'] as const

// searchFilters keeps the search parameters of a page address, in a stable
// order with empty values dropped, so equal searches share one cache entry.
export function searchFilters(params: URLSearchParams): URLSearchParams {
  const filters = new URLSearchParams()
  for (const key of searchSingleParams) {
    const value = params.get(key)
    if (value !== null && value !== '') {
      filters.set(key, value)
    }
  }
  for (const key of searchListParams) {
    for (const value of params.getAll(key)) {
      if (value !== '') {
        filters.append(key, value)
      }
    }
  }
  return filters
}

export function searchQueryKey(filters: URLSearchParams) {
  return ['search', filters.toString()] as const
}

export function fetchSearch(
  filters: URLSearchParams,
  cursor: string | null,
  signal?: AbortSignal,
): Promise<SearchPage> {
  const params = new URLSearchParams(filters)
  if (cursor !== null) {
    params.set('cursor', cursor)
  }
  return apiGet<SearchPage>(`/api/search?${params}`, signal)
}

// selectionQuery turns search parameters into the query of create-selection.
export function selectionQuery(filters: URLSearchParams): SelectionQuery {
  const query: SelectionQuery = {}
  for (const key of searchSingleParams) {
    const value = filters.get(key)
    if (value !== null) {
      query[key] = numericSingles[key] ? Number(value) : value
    }
  }
  for (const key of searchListParams) {
    const values = filters.getAll(key)
    if (values.length > 0) {
      query[key] = key === 'tag' ? values.map(Number) : values
    }
  }
  return query
}
