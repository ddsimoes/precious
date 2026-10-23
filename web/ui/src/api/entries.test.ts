import { describe, expect, it, vi } from 'vitest'

import {
  childrenQueryKey,
  entryQueryKey,
  fetchChildren,
  fetchEntry,
  fetchTreemap,
  treemapQueryKey,
} from '@/api/entries'

// stubFetch answers every request with an empty object and records the
// asked URLs.
function stubFetch(): URL[] {
  const urls: URL[] = []
  vi.stubGlobal('fetch', async (input: RequestInfo | URL) => {
    urls.push(new URL(String(input), 'http://precious.test'))
    return Response.json({})
  })
  return urls
}

describe('entry fetchers', () => {
  it('ask for the children places and hide only when the view asks', async () => {
    const urls = stubFetch()

    await fetchChildren('7', 'bytes', 'desc', null)
    await fetchChildren('7', 'bytes', 'desc', null, undefined, { places: false, hide: false })
    await fetchChildren('7', 'bytes', 'desc', null, undefined, { places: true, hide: false })
    await fetchChildren('7', 'only_here', 'desc', 'c1', undefined, { places: true, hide: true })

    const params = urls.map((url) => Object.fromEntries(url.searchParams))
    expect(urls.map((url) => url.pathname)).toEqual(Array(4).fill('/api/entries/7/children'))
    expect(params).toEqual([
      { sort: 'bytes', order: 'desc' },
      { sort: 'bytes', order: 'desc' },
      { sort: 'bytes', order: 'desc', places: '1' },
      { sort: 'only_here', order: 'desc', cursor: 'c1', places: '1', hide: 'other_source' },
    ])
  })

  it('never sends hide without places, which the server refuses', async () => {
    const urls = stubFetch()

    await fetchChildren('7', 'name', 'asc', null, undefined, { places: false, hide: true })

    expect(Object.fromEntries(urls[0]?.searchParams ?? [])).toEqual({ sort: 'name', order: 'asc' })
  })

  it('ask for an entry and a treemap with places only when asked', async () => {
    const urls = stubFetch()

    await fetchEntry('7')
    await fetchEntry('7', undefined, false)
    await fetchEntry('7', undefined, true)
    await fetchTreemap('7')
    await fetchTreemap('7', undefined, false)
    await fetchTreemap('7', undefined, true)

    expect(urls.map((url) => `${url.pathname}${url.search}`)).toEqual([
      '/api/entries/7',
      '/api/entries/7',
      '/api/entries/7?places=1',
      '/api/entries/7/treemap',
      '/api/entries/7/treemap',
      '/api/entries/7/treemap?places=1',
    ])
  })
})

describe('entry query keys', () => {
  it('keep a separate cache for each places option', () => {
    expect(entryQueryKey('7', true)).not.toEqual(entryQueryKey('7'))
    expect(entryQueryKey('7', false)).toEqual(entryQueryKey('7'))
    expect(treemapQueryKey('7', true)).not.toEqual(treemapQueryKey('7'))
    expect(treemapQueryKey('7', false)).toEqual(treemapQueryKey('7'))
  })

  it('keep a separate cache for each children view, matching what is sent', () => {
    const plain = childrenQueryKey('7', 'bytes', 'desc')
    const withPlaces = childrenQueryKey('7', 'bytes', 'desc', { places: true, hide: false })
    const hidden = childrenQueryKey('7', 'bytes', 'desc', { places: true, hide: true })

    expect(new Set([plain, withPlaces, hidden].map((key) => JSON.stringify(key))).size).toBe(3)
    expect(childrenQueryKey('7', 'bytes', 'desc', { places: false, hide: false })).toEqual(plain)
    // hide is not sent without places, so it asks for the plain page.
    expect(childrenQueryKey('7', 'bytes', 'desc', { places: false, hide: true })).toEqual(plain)
  })
})
