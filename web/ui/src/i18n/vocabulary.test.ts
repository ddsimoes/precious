import { describe, expect, it } from 'vitest'

import { en } from '@/i18n/en'

// Internal terms that must never reach the interface (spec §11.11, design D14).
const banned = [
  'atomic',
  'expanded',
  'descriptor',
  'epoch',
  'intent revision',
  'frontier',
  'coverage scope',
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

describe('vocabulary', () => {
  it('keeps internal terms out of the en catalog', () => {
    expect(catalogStrings(en).length).toBeGreaterThan(0)
    expect(violations(en)).toEqual([])
  })

  it('catches every banned term in any case and nesting', () => {
    const sample = {
      a: 'An Atomic folder',
      b: { c: 'EXPANDED view', d: ['the descriptor'] },
      e: 'new Epoch',
      f: 'intent  Revision 4',
      g: 'scan frontier',
      h: 'Coverage scope',
      ok: 'Folders and files',
    }
    expect(violations(sample)).toHaveLength(7)
  })
})
