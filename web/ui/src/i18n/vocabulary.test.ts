import { describe, expect, it } from 'vitest'

import { en } from '@/i18n/en'

// Internal terms that must never reach the interface (spec §11.11, design
// D14, and R3 design D16 for organizing).
const banned = [
  'atomic',
  'expanded',
  'descriptor',
  'epoch',
  'intent revision',
  'frontier',
  'coverage scope',
  'journal',
  'executor',
]

const bannedPattern = new RegExp(
  banned.map((term) => term.replace(/ /g, '\\s+')).join('|'),
  'i',
)

// catalogStrings lists every string of a catalog with its dotted key.
function catalogStrings(node: unknown, key = ''): Array<[string, string]> {
  if (typeof node === 'string') {
    return [[key, node]]
  }
  if (typeof node === 'object' && node !== null) {
    return Object.entries(node).flatMap(([k, v]) => catalogStrings(v, key === '' ? k : `${key}.${k}`))
  }
  return []
}

function violations(catalog: unknown): string[] {
  return catalogStrings(catalog)
    .filter(([, text]) => bannedPattern.test(text))
    .map(([key, text]) => `${key}: ${text}`)
}

// The share of a folder that has a copy elsewhere is named "Has copies", not
// "Duplicated", which reads as the space that could be freed (ADR 0010). The
// word alone is matched: "duplicates" and "Duplicate folders" stay.
const duplicatedPattern = /\bduplicated\b/i

function duplicatedLabels(catalog: unknown): string[] {
  return catalogStrings(catalog)
    .filter(([, text]) => duplicatedPattern.test(text))
    .map(([key, text]) => `${key}: ${text}`)
}

describe('vocabulary', () => {
  it('keeps internal terms out of the en catalog', () => {
    expect(catalogStrings(en).length).toBeGreaterThan(0)
    expect(violations(en)).toEqual([])
  })

  it('keeps internal terms out of the cleanup screens (R4)', () => {
    const r4 = {
      cleanup: en.cleanup,
      detail: en.detail.quarantine,
      reasons: en.organize.reason,
      errors: en.errors.codes,
      history: en.history,
    }
    expect(catalogStrings(en.cleanup).length).toBeGreaterThan(100)
    expect(violations(r4)).toEqual([])
    expect(duplicatedLabels(r4)).toEqual([])
    // The interface says "delete for good", never the internal "purge".
    expect(catalogStrings(r4).filter(([, text]) => /\bpurge/i.test(text))).toEqual([])
  })

  it('keeps internal terms out of the dates screens (R5)', () => {
    const r5 = {
      dates: en.dates,
      reasons: en.organize.reason,
      items: en.organize.item,
      titles: [en.organize.title, en.organize.titleNoDestination, en.organize.status],
      history: en.history,
    }
    expect(catalogStrings(en.dates).length).toBeGreaterThan(100)
    expect(violations(r5)).toEqual([])
    // The owner reads "modification time", "camera information", and "set
    // file dates", never the API's names for them.
    const apiNames = /\bmtime\b|set_mtime|date_organize|\bexif\b|metadata|utimensat|\bowner\b|effective/i
    expect(catalogStrings(r5).filter(([, text]) => apiNames.test(text))).toEqual([])
  })

  it('catches every banned term in any case and nesting', () => {
    const sample = {
      a: 'An Atomic folder',
      b: { c: 'EXPANDED view', d: ['the descriptor'] },
      e: 'new Epoch',
      f: 'intent  Revision 4',
      g: 'scan frontier',
      h: 'Coverage scope',
      i: 'the Journal of moves',
      j: 'an executor step',
      ok: 'Folders and files',
    }
    expect(violations(sample)).toHaveLength(9)
  })

  it('names the duplicated share "Has copies", never "Duplicated"', () => {
    expect(duplicatedLabels(en)).toEqual([])
    expect(en.map.columns.duplicated).toBe('Has copies')
    expect(en.map.colorBy.duplication).toBe('Has copies')
    expect(en.detail.duplicated).toBe('Has copies')
  })

  it('catches "Duplicated" as a label or a legend band, but not "duplicates"', () => {
    const sample = {
      a: 'Duplicated',
      b: { c: 'Less than 25% duplicated' },
      ok: 'Duplicate folders and files',
      ok2: 'Folders with duplicates',
    }
    expect(duplicatedLabels(sample)).toEqual(['a: Duplicated', 'b.c: Less than 25% duplicated'])
  })
})
