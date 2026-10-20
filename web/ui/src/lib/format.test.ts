import { describe, expect, it } from 'vitest'

import {
  formatBytes,
  formatCount,
  formatDate,
  formatDateSpan,
  formatDateTime,
  formatLocal,
  formatTimePrecision,
} from '@/lib/format'

const nbsp = '\u00a0'

describe('formatBytes', () => {
  it.each([
    [0, `0${nbsp}B`],
    [512, `512${nbsp}B`],
    [1023, `1,023${nbsp}B`],
    [1024, `1${nbsp}KiB`],
    [1536, `1.5${nbsp}KiB`],
    [10 * 1024 + 300, `10${nbsp}KiB`],
    [1024 ** 2, `1${nbsp}MiB`],
    // 1023.9 KiB rounds to 1024 KiB, which reads as 1 MiB.
    [Math.round(1023.9 * 1024), `1${nbsp}MiB`],
    [120 * 1024 ** 3, `120${nbsp}GiB`],
    [Math.round(1.25 * 1024 ** 4), `1.3${nbsp}TiB`],
    [3 * 1024 ** 5, `3${nbsp}PiB`],
  ])('formats %d bytes in English as %s', (bytes, want) => {
    expect(formatBytes(bytes, 'en')).toBe(want)
  })

  it('follows the locale for separators', () => {
    expect(formatBytes(1536, 'pt-BR')).toBe(`1,5${nbsp}KiB`)
    expect(formatBytes(1023, 'de')).toBe(`1.023${nbsp}B`)
  })
})

describe('formatCount', () => {
  it('groups digits by locale', () => {
    expect(formatCount(410_000, 'en')).toBe('410,000')
    expect(formatCount(1_234_567, 'de')).toBe('1.234.567')
    expect(formatCount(0, 'en')).toBe('0')
  })
})

describe('dates', () => {
  // The test run's time zone is UTC (vite.config.ts).
  it('formats a date by locale', () => {
    expect(formatDate('2024-03-05T14:30:00Z', 'en')).toBe('Mar 5, 2024')
    expect(formatDate('2024-03-05T14:30:00Z', 'pt-BR')).toBe('5 de mar. de 2024')
  })

  it('formats a date and time by locale', () => {
    expect(formatDateTime('2024-03-05T14:30:00Z', 'en')).toMatch(/^Mar 5, 2024, 2:30\sPM$/u)
    expect(formatDateTime('2024-03-05T14:30:00Z', 'pt-BR')).toBe('5 de mar. de 2024, 14:30')
  })

  it('formats a span of dates with only its known ends', () => {
    const [a, b] = ['2003-02-01T00:00:00Z', '2004-11-30T08:00:00Z']
    expect(formatDateSpan(a, b, 'en')).toBe('Feb 1, 2003 – Nov 30, 2004')
    expect(formatDateSpan(b, '2004-11-30T20:00:00Z', 'en')).toBe('Nov 30, 2004')
    expect(formatDateSpan(null, b, 'en')).toBe('Nov 30, 2004')
    expect(formatDateSpan(a, null, 'en')).toBe('Feb 1, 2003')
    expect(formatDateSpan(null, null, 'en')).toBeNull()
  })
})

describe('formatTimePrecision', () => {
  it.each([
    [1, '1 nanosecond'],
    [100, '100 nanoseconds'],
    [1_000, '1 microsecond'],
    [10_000_000, '10 milliseconds'],
    [2_000_000_000, '2 seconds'],
  ])('formats %d ns as %s', (ns, want) => {
    expect(formatTimePrecision(ns, 'en')).toBe(want)
  })
})

describe('formatLocal', () => {
  it.each([
    ['2006', 'year', null, '2006'],
    ['2008-03', 'month', null, 'March 2008'],
    ['2011-04-16', 'day', null, 'Apr 16, 2011'],
    ['2008-03-22T14:00:00', 'second', -180, 'Mar 22, 2008, 2:00:00 PM UTC−03:00'],
    ['2010-07-17T10:00:00', 'second', 330, 'Jul 17, 2010, 10:00:00 AM UTC+05:30'],
  ] as const)('formats %s to the %s as it was on the clock', (local, precision, offset, want) => {
    expect(formatLocal(local, precision, offset, 'en').replace(/\s/g, ' ')).toBe(want)
  })
})
