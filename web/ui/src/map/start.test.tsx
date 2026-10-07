import { cleanup, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { Ancestor } from '@/api/entries'
import { rememberSource } from '@/app/sourceChoice'
import { entryDetail, entryRow, folderRow, fotosSource } from '@/test/fixtures'
import { jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'

// vi.mock is hoisted above the static imports, so its factory loads the
// fake itself.
vi.mock('@/map/echarts', async () => {
  const { FakeChart } = await import('@/test/fakeChart')
  return { init: () => new FakeChart() }
})

// The Map's start and its folder path (r2b design D9).

afterEach(() => {
  rememberSource(null)
})

const archiveSource = fotosSource({ id: 'archive', label: 'archive', root_entry_id: '20' })
const top = folderRow('20', '', { source_id: 'archive', path: '', path_b64: '' })
const oldDisk = folderRow('21', 'old-disk', { source_id: 'archive', path: 'old-disk' })
const home = folderRow('22', 'home', { source_id: 'archive', path: 'old-disk/home' })
const docs = folderRow('23', 'docs', { source_id: 'archive', path: 'old-disk/home/docs' })
const carta = entryRow({ id: '24', source_id: 'archive', name: 'carta.txt', path: 'old-disk/home/docs/carta.txt' })
const notas = entryRow({ id: '25', source_id: 'archive', name: 'notas.txt', path: 'old-disk/home/notas.txt' })

function ancestor(entry: { id: string; name: string }, onlyChild: boolean): Ancestor {
  return { id: entry.id, name: entry.name, name_b64: btoa(entry.name), only_child: onlyChild }
}

function folderRoutes(id: string, entryJSON: unknown, items: unknown[]) {
  return {
    [`GET /api/entries/${id}`]: () => jsonResponse(200, entryJSON),
    [`GET /api/entries/${id}/treemap`]: () => jsonResponse(200, { entry: {}, items, other: { count: 0, bytes: 0 } }),
    [`GET /api/entries/${id}/children`]: () => jsonResponse(200, { items, next_cursor: null }),
  }
}

// A source "archive" whose top holds only old-disk, which holds only home,
// which holds docs (holding only carta.txt) and notas.txt.
function routes() {
  return {
    'GET /api/session': () => jsonResponse(200, signedIn),
    'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource(), archiveSource] }),
    'GET /api/entries/1': () => jsonResponse(200, entryDetail(folderRow('1', ''), { ancestors: [] })),
    'GET /api/entries/1/treemap': () => jsonResponse(200, { entry: {}, items: [], other: { count: 0, bytes: 0 } }),
    'GET /api/entries/1/children': () => jsonResponse(200, { items: [], next_cursor: null }),
    ...folderRoutes('20', entryDetail(top, { ancestors: [], only_folder: '21' }), [oldDisk]),
    ...folderRoutes('21', entryDetail(oldDisk, { ancestors: [ancestor(top, true)], only_folder: '22' }), [home]),
    ...folderRoutes('22', entryDetail(home, { ancestors: [ancestor(top, true), ancestor(oldDisk, true)] }), [docs, notas]),
    ...folderRoutes(
      '23',
      entryDetail(docs, { ancestors: [ancestor(top, true), ancestor(oldDisk, true), ancestor(home, false)] }),
      [carta],
    ),
  }
}

function pathSteps() {
  const path = screen.getByRole('navigation', { name: 'Folder path' })
  return within(path)
    .getAllByRole('listitem')
    .map((item) => {
      const link = within(item).queryByRole('link')
      return link === null ? `[${item.textContent}]` : `${link.textContent} → ${link.getAttribute('href')}`
    })
}

describe('Map start', () => {
  // The inventory-explorer scenario "A source whose top holds one folder",
  // started on the remembered source.
  it('opens the remembered source on the folder below its chain of single folders', async () => {
    rememberSource('archive')
    const requests = stubApi(routes())
    const { router } = renderApp('/map')

    await waitFor(() => expect(router.state.location.pathname).toBe('/map/22'))
    expect(router.state.historyAction).toBe('REPLACE')
    expect(await screen.findByRole('table', { name: 'Contents of home' })).toBeInTheDocument()
    expect(pathSteps()).toEqual(['archive → /map/20', '[old-disk/home]'])
    // The start fetched each folder once; the Map reuses the last.
    expect(requests.filter((r) => new URL(r.url).pathname === '/api/entries/20')).toHaveLength(1)
    expect(requests.some((r) => new URL(r.url).pathname === '/api/entries/1')).toBe(false)

    // Below the chain, the chain stays one step that opens its deepest.
    await userEvent.click(screen.getByRole('link', { name: 'docs' }))
    expect(await screen.findByRole('table', { name: 'Contents of docs' })).toBeInTheDocument()
    expect(pathSteps()).toEqual(['archive → /map/20', 'old-disk/home → /map/22', '[docs]'])
  })

  it('falls back to the first source, and a source in the address wins', async () => {
    rememberSource('gone')
    stubApi(routes())
    const first = renderApp('/map')
    await waitFor(() => expect(first.router.state.location.pathname).toBe('/map/1'))
    cleanup()

    rememberSource('fotos')
    stubApi(routes())
    const addressed = renderApp('/map?source=archive')
    await waitFor(() => expect(addressed.router.state.location.pathname).toBe('/map/22'))
  })
})
