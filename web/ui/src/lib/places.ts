import type { TFunction } from 'i18next'

import { places, type PlaceSums } from '@/api/entries'
import type { Formatters } from '@/lib/format'

// The Map's figures of where copies are (R5c design D8).

// PlaceKey is one sum of a folder's places: a place with bytes, or the
// disposable files counted apart (R5c design D14).
export type PlaceKey = keyof PlaceSums

// placeKeys are the sums in the legend's order, the disposable files last.
export const placeKeys: PlaceKey[] = [
  ...places.filter((p): p is Exclude<PlaceKey, 'disposable'> => p !== 'none' && p !== 'not_computed'),
  'disposable',
]

// placeParts are the sums holding bytes, in placeKeys' order.
export function placeParts(sums: PlaceSums | null | undefined): { key: PlaceKey; bytes: number }[] {
  if (sums === null || sums === undefined) {
    return []
  }
  return placeKeys.map((key) => ({ key, bytes: sums[key].bytes })).filter((part) => part.bytes > 0)
}

// placeAmountText says one sum in words, such as "2 GiB only copy".
export function placeAmountText(key: PlaceKey, bytes: number, t: TFunction, fmt: Formatters): string {
  return t(`map.places.amount.${key}`, { bytes: fmt.bytes(bytes) })
}
