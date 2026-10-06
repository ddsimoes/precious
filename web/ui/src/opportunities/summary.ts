import type { TFunction } from 'i18next'

import type { RowSummary } from '@/api/opportunities'
import { en } from '@/i18n/en'
import type { Formatters } from '@/lib/format'

type KnownTrait = keyof typeof en.entry.trait
type KnownSignal = keyof typeof en.entry.signal

// signalLabel names a notable signal of a row: a trait (contains_vcs) or an
// indicator signal (database_present).
function signalLabel(signal: string, t: TFunction): string {
  if (Object.hasOwn(en.entry.trait, signal)) {
    return t(`entry.trait.${signal as KnownTrait}`)
  }
  if (Object.hasOwn(en.entry.signal, signal)) {
    return t(`entry.signal.${signal as KnownSignal}`)
  }
  return t('entry.signal.other')
}

// summaryLine is the one-line summary of a review row (R2 design D13), built
// from its structured fields: its category, its oldest and newest year, its
// files, its bytes, and up to two notable signals, such as "Installed
// application · 2003–2004 · 120 files · 400 MiB · holds Office document".
export function summaryLine(summary: RowSummary, t: TFunction, fmt: Formatters): string {
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
  parts.push(t('units.files', { count: summary.files, formatted: fmt.count(summary.files) }))
  parts.push(fmt.bytes(summary.bytes))
  const signals = summary.signals.slice(0, 2).map((signal) => signalLabel(signal, t))
  if (signals.length > 0) {
    parts.push(t('review.summaryHolds', { signals: fmt.list(signals) }))
  }
  return parts.join(' · ')
}
