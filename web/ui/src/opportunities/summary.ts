import type { TFunction } from 'i18next'

import type { RowSummary } from '@/api/opportunities'
import { en } from '@/i18n/en'
import type { Formatters } from '@/lib/format'

type ReviewSignal = keyof typeof en.review.signal

// summaryLine is the one-line summary of a review row (R2 design D13), built
// from its structured fields: its category, its oldest and newest year, its
// files, its bytes, and up to two notable signals, a trait (contains_vcs) or
// an indicator signal (database_present), each named as what the row holds,
// such as "Installed application · 2003–2004 · 120 files · 400 MiB · holds
// office documents and version history".
// files: false leaves the file count out, for a group of copies of one file
// whose title counts them.
export function summaryLine(
  summary: RowSummary,
  t: TFunction,
  fmt: Formatters,
  { files = true }: { files?: boolean } = {},
): string {
  const parts: string[] = []
  if (summary.category !== null) {
    parts.push(t(`entry.category.${summary.category}`))
  }
  const [from, to] = summary.years ?? [null, null]
  if (from !== null && to !== null && from !== to) {
    parts.push(t('review.summaryYears', { from, to }))
  } else if (from !== null || to !== null) {
    parts.push(String(from ?? to))
  }
  if (files) {
    parts.push(t('units.files', { count: summary.files, formatted: fmt.count(summary.files) }))
  }
  parts.push(fmt.bytes(summary.bytes))
  const signals = summary.signals
    .slice(0, 2)
    .map((signal) =>
      Object.hasOwn(en.review.signal, signal) ? t(`review.signal.${signal as ReviewSignal}`) : t('review.signal.other'),
    )
  if (signals.length > 0) {
    // A trait and an indicator can read the same: a database.
    parts.push(t('review.summaryHolds', { signals: fmt.conjunction([...new Set(signals)]) }))
  }
  return parts.join(' · ')
}
