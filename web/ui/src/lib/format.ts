import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

// Sizes, counts, and dates in the reader's locale (spec §11). Sizes use
// binary units, the ones file managers and disk tools use for capacity.

const byteUnits = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB', 'EiB']

// formatBytes formats a byte count with one decimal below 10 of a unit and
// none above: "512 B", "1.5 KiB", "120 GiB".
export function formatBytes(bytes: number, locale: string): string {
  let value = Math.abs(bytes)
  let unit = 0
  while (unit < byteUnits.length - 1 && value >= 1024) {
    value /= 1024
    unit++
  }
  const fractionDigits = unit > 0 && value < 10 ? 1 : 0
  // A value that rounds up to 1024 reads better as 1 of the next unit.
  if (unit < byteUnits.length - 1 && Number(value.toFixed(fractionDigits)) >= 1024) {
    value /= 1024
    unit++
  }
  const number = new Intl.NumberFormat(locale, {
    maximumFractionDigits: unit > 0 && value < 10 ? 1 : 0,
  }).format(Math.sign(bytes) * value)
  return `${number}\u00a0${byteUnits[unit]}`
}

export function formatCount(count: number, locale: string): string {
  return new Intl.NumberFormat(locale).format(count)
}

// formatPercent formats a fraction as a whole percentage: 0.98 is "98%".
export function formatPercent(fraction: number, locale: string): string {
  return new Intl.NumberFormat(locale, { style: 'percent', maximumFractionDigits: 0 }).format(fraction)
}

// formatList joins items as a list: "a, b, c".
export function formatList(items: string[], locale: string): string {
  return new Intl.ListFormat(locale, { style: 'short', type: 'unit' }).format(items)
}

// formatDate formats an RFC 3339 time as a date; formatDateTime adds the
// time of day. Both use the browser's time zone.
export function formatDate(time: string, locale: string): string {
  return new Intl.DateTimeFormat(locale, { dateStyle: 'medium' }).format(new Date(time))
}

export function formatDateTime(time: string, locale: string): string {
  return new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(
    new Date(time),
  )
}

// formatDateSpan formats the dates from oldest to newest: one date when both
// fall on the same day or only one is known, and null when neither is.
export function formatDateSpan(oldest: string | null, newest: string | null, locale: string): string | null {
  const from = oldest === null ? null : formatDate(oldest, locale)
  const to = newest === null ? null : formatDate(newest, locale)
  if (from !== null && to !== null && from !== to) {
    return `${from} – ${to}`
  }
  return to ?? from
}

const precisionUnits = [
  { unit: 'second', ns: 1e9 },
  { unit: 'millisecond', ns: 1e6 },
  { unit: 'microsecond', ns: 1e3 },
  { unit: 'nanosecond', ns: 1 },
] as const

// formatTimePrecision formats a file system's time resolution, given in
// nanoseconds, in the largest whole unit: "2 seconds", "100 nanoseconds".
export function formatTimePrecision(ns: number, locale: string): string {
  const { unit, ns: size } =
    precisionUnits.find((u) => ns >= u.ns && ns % u.ns === 0) ?? precisionUnits[3]
  return new Intl.NumberFormat(locale, { style: 'unit', unit, unitDisplay: 'long' }).format(ns / size)
}

export interface Formatters {
  bytes: (bytes: number) => string
  count: (count: number) => string
  percent: (fraction: number) => string
  list: (items: string[]) => string
  date: (time: string) => string
  dateTime: (time: string) => string
  dateSpan: (oldest: string | null, newest: string | null) => string | null
  timePrecision: (ns: number) => string
}

// useFormat returns the formatters for the interface language.
export function useFormat(): Formatters {
  const { i18n } = useTranslation()
  const locale = i18n.resolvedLanguage ?? i18n.language
  return useMemo(
    () => ({
      bytes: (bytes) => formatBytes(bytes, locale),
      count: (count) => formatCount(count, locale),
      percent: (fraction) => formatPercent(fraction, locale),
      list: (items) => formatList(items, locale),
      date: (time) => formatDate(time, locale),
      dateTime: (time) => formatDateTime(time, locale),
      dateSpan: (oldest, newest) => formatDateSpan(oldest, newest, locale),
      timePrecision: (ns) => formatTimePrecision(ns, locale),
    }),
    [locale],
  )
}
