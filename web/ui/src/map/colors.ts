import type { TFunction } from 'i18next'

import { decisions, families, fileKinds, lastChange, displayKind, type EntryRow } from '@/api/entries'
import type { Decision, Family, FileKind } from '@/api/home'
import { dominantFamily } from '@/lib/composition'

// The Map's color modes (spec §11.2): each paints a treemap area by one
// property of its entry, and names its colors in a legend.

export type ColorMode = 'family' | 'kind' | 'age' | 'decision' | 'tag'

export const colorModes: ColorMode[] = ['family', 'kind', 'age', 'decision', 'tag']

export interface LegendItem {
  key: string
  label: string
  color: string
}

// ColorContext is what a mode needs beyond the entry itself.
export interface ColorContext {
  // now is the time ages are measured from, in milliseconds.
  now: number
  // tagId is the tag the tag mode shows, or null before one is chosen.
  tagId: number | null
  // folderHasTag is set when the shown folder carries the tag itself or
  // inherits it, so every entry inside inherits it too.
  folderHasTag: boolean
}

// neutral paints what a mode has no value for.
export const neutralColor = '#d4d4d8'
// otherColor paints the treemap's area of the remaining children.
export const otherColor = '#e4e4e7'

// familyColors paints the families in the treemap, its legend, and the
// composition bars.
export const familyColors: Record<Family, string> = {
  personal: '#16a34a',
  programs: '#2563eb',
  disposable: '#ea580c',
  containers: '#a1a1aa',
}

const kindColors: Record<FileKind, string> = {
  image: '#0d9488',
  video: '#7c3aed',
  audio: '#db2777',
  document: '#2563eb',
  source: '#65a30d',
  archive: '#a16207',
  installer: '#ea580c',
  executable: '#dc2626',
  system: '#52525b',
  other: '#a1a1aa',
}

const decisionColors: Record<Decision, string> = {
  undecided: '#a1a1aa',
  keep: '#16a34a',
  discard: '#dc2626',
  later: '#d97706',
}

// ageBands are the upper bounds, in years since the last change, of each
// age color, newest first.
const ageBands = [
  { key: 'year1', years: 1, color: '#1d4ed8' },
  { key: 'year3', years: 3, color: '#3b82f6' },
  { key: 'year5', years: 5, color: '#93c5fd' },
  { key: 'year10', years: 10, color: '#fcd34d' },
  { key: 'older', years: Infinity, color: '#b45309' },
] as const

const tagColor = '#7c3aed'
const yearMs = 365.25 * 24 * 3600 * 1000

export function colorOf(row: EntryRow, mode: ColorMode, context: ColorContext): string {
  switch (mode) {
    case 'family': {
      // The dominant family of the content, so a folder of loose photos is
      // personal (design D21); a server without compositions gives the
      // category's family.
      const family = dominantFamily(row.composition) ?? row.family
      return family === null ? neutralColor : familyColors[family]
    }
    case 'kind': {
      const kind = displayKind(row)
      return kind === null ? neutralColor : kindColors[kind]
    }
    case 'age': {
      const time = lastChange(row)
      if (time === null) {
        return neutralColor
      }
      const years = (context.now - Date.parse(time)) / yearMs
      return (ageBands.find((band) => years < band.years) ?? ageBands[4]).color
    }
    case 'decision':
      return decisionColors[row.eff_decision]
    case 'tag':
      return context.tagId !== null && (context.folderHasTag || row.tag_ids.includes(context.tagId))
        ? tagColor
        : neutralColor
  }
}

export function legendOf(mode: ColorMode, t: TFunction): LegendItem[] {
  switch (mode) {
    case 'family':
      return [
        ...families.map((f) => ({ key: f, label: t(`home.family.${f}`), color: familyColors[f] })),
        { key: 'none', label: t('map.unclassified'), color: neutralColor },
      ]
    case 'kind':
      return fileKinds.map((k) => ({ key: k, label: t(`entry.fileKind.${k}`), color: kindColors[k] }))
    case 'age':
      return [
        ...ageBands.map((band) => ({ key: band.key, label: t(`map.age.${band.key}`), color: band.color })),
        { key: 'unknown', label: t('map.age.unknown'), color: neutralColor },
      ]
    case 'decision':
      return decisions.map((d) => ({ key: d, label: t(`home.decision.${d}`), color: decisionColors[d] }))
    case 'tag':
      return [
        { key: 'has', label: t('map.hasTag'), color: tagColor },
        { key: 'lacks', label: t('map.lacksTag'), color: neutralColor },
      ]
  }
}
