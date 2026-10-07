import type { TFunction } from 'i18next'

import type { YearAmount } from '@/api/home'
import type { Bar } from '@/home/BarList'

// yearBars are a by-year breakdown's bars, the years ascending and the
// unknown date last.
export function yearBars(byYear: readonly YearAmount[], t: TFunction): Bar[] {
  return [...byYear]
    .sort((a, b) => (a.year ?? Infinity) - (b.year ?? Infinity))
    .map((a) => ({
      key: String(a.year),
      label: a.year === null ? t('entry.unknownDate') : String(a.year),
      bytes: a.bytes,
      files: a.files,
    }))
}
