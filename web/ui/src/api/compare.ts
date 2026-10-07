import { compareQueryRoot } from '@/api/content'
import type { Amount } from '@/api/home'
import type { EntryRow } from '@/api/entries'
import { apiGet } from '@/app/api'

// Compare (R2 design D11; GET /api/compare): two folders or archives side
// by side, their files in five groups.

// Bucket is one group: files whose content is only on the left or only on
// the right, on both sides (identical), at the same path with different
// content (different), or not checked yet while their size occurs on the
// other side (unchecked).
export type Bucket = 'only_left' | 'only_right' | 'identical' | 'different' | 'unchecked'

export const buckets: Bucket[] = ['only_left', 'only_right', 'identical', 'different', 'unchecked']

export function isBucket(value: string | null): value is Bucket {
  return buckets.some((b) => b === value)
}

// CompareItem is one file of a group: its path relative to the sides (the
// left file's, else the right's), each side's own path inside that side,
// and its row on each side that holds it. An identical item with one file
// is an extra copy: twin is the other side's file holding the same
// content, with its path inside that side (r2b design D11).
export interface CompareItem {
  path: string
  path_b64: string
  left_path: string | null
  right_path: string | null
  left: EntryRow | null
  right: EntryRow | null
  twin: { path: string; entry: EntryRow } | null
}

// ComparePage is a page of one group: the group asked for or, without
// one, the first that holds files (only_left, only_right, different,
// unchecked, identical), named in bucket.
export interface ComparePage {
  left: EntryRow
  right: EntryRow
  summary: Record<Bucket, Amount>
  bucket: Bucket
  items: CompareItem[]
  next_cursor: string | null
}

// A null bucket asks for the group the server opens on.
export function compareQueryKey(left: string, right: string, bucket: Bucket | null) {
  return [...compareQueryRoot, left, right, bucket] as const
}

export function fetchCompare(
  left: string,
  right: string,
  bucket: Bucket | null,
  cursor: string | null,
  signal?: AbortSignal,
): Promise<ComparePage> {
  const params = new URLSearchParams({ left, right })
  if (bucket !== null) {
    params.set('bucket', bucket)
  }
  if (cursor !== null) {
    params.set('cursor', cursor)
  }
  return apiGet<ComparePage>(`/api/compare?${params}`, signal)
}
