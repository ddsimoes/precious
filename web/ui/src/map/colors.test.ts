import i18n from 'i18next'
import { describe, expect, it } from 'vitest'

import { places, type Place } from '@/api/entries'
import { colorOf, legendOf, neutralColor, type ColorContext } from '@/map/colors'
import { entryRow, folderRow } from '@/test/fixtures'

const context: ColorContext = { now: Date.parse('2026-01-01T00:00:00Z'), tagId: null, folderHasTag: false }

const legend = legendOf('places', i18n.t.bind(i18n))

function legendColor(key: Place | 'disposable'): string | undefined {
  return legend.find((item) => item.key === key)?.color
}

describe('places colors', () => {
  it('paints each place in its own color, the one its legend shows', () => {
    const colors = places.map((place) => colorOf(folderRow('5', 'Fotos', { place }), 'places', context))

    expect(new Set(colors).size).toBe(places.length)
    expect(colors).not.toContain(neutralColor)
    places.forEach((place, i) => expect(colors[i]).toBe(legendColor(place)))
  })

  it('paints files and folders of one place alike', () => {
    for (const place of places) {
      const file = entryRow({ place, disposable: false })
      expect(colorOf(file, 'places', context)).toBe(colorOf(folderRow('5', 'Fotos', { place }), 'places', context))
    }
  })

  it('paints disposable files apart, whatever their place', () => {
    const disposable = legendColor('disposable')
    const placeColors = places.map((place) => legendColor(place))

    expect(disposable).toBeDefined()
    expect(placeColors).not.toContain(disposable)
    for (const place of ['only_copy', 'this_source', 'other_source', 'unchecked'] as const) {
      expect(colorOf(entryRow({ place, disposable: true }), 'places', context)).toBe(disposable)
    }
  })

  it('paints not computed apart from not checked and from no place', () => {
    const notComputed = colorOf(folderRow('5', 'Fotos', { place: 'not_computed' }), 'places', context)

    expect(notComputed).not.toBe(colorOf(folderRow('5', 'Fotos', { place: 'unchecked' }), 'places', context))
    expect(notComputed).not.toBe(neutralColor)
  })

  it('paints a row without a place neutral: fetched without places, or missing', () => {
    expect(colorOf(entryRow(), 'places', context)).toBe(neutralColor)
    expect(colorOf(entryRow({ place: null, state: 'missing' }), 'places', context)).toBe(neutralColor)
  })

  it('names each place and the disposable files in the legend', () => {
    expect(legend.map((item) => item.key)).toEqual([...places, 'disposable'])
    for (const item of legend) {
      expect(item.label).not.toBe('')
      expect(item.label).not.toContain('map.place')
    }
    expect(new Set(legend.map((item) => item.label)).size).toBe(legend.length)
  })
})
