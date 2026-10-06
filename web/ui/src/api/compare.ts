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

// firstBucket is the group a comparison opens on: the first that holds
// files, in the order only_left, only_right, different, unchecked,
// identical, so a pair with nothing apart opens on its identical files.
export function firstBucket(summary: Record<Bucket, Amount>): Bucket {
  const order: Bucket[] = ['only_left', 'only_right', 'different', 'unchecked', 'identical']
  return order.find((b) => summary[b].files > 0) ?? 'only_left'
}

// CompareItem is one file of a group: its path relative to the sides, and
// its row on each side that holds it.
export interface CompareItem {
  path: string
  path_b64: string
  left: EntryRow | null
  right: EntryRow | null
}

export interface ComparePage {
  left: EntryRow
  right: EntryRow
  summary: Record<Bucket, Amount>
  items: CompareItem[]
  next_cursor: string | null
}

// A null bucket asks for the summary alone, with no items.
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
