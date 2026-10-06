import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { EntryRow } from '@/api/entries'
import { FakeChart } from '@/test/fakeChart'
import { entryDetail, entryRow, folderRow, fotosSource } from '@/test/fixtures'
import { jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'

// vi.mock is hoisted above the static imports, so its factory loads the
// fake itself.
vi.mock('@/map/echarts', async () => {
  const { FakeChart } = await import('@/test/fakeChart')
  return { init: () => new FakeChart() }
})

const GiB = 1024 ** 3

beforeEach(() => {
  FakeChart.instances = []
})

const root = folderRow('1', 'fotos', { path: '', path_b64: '' })
const fotos = folderRow('2', 'Fotos', { total_bytes: 40 * GiB, eff_decision: 'keep', decision: 'keep' })
const downloads = folderRow('3', 'Downloads', {
  total_bytes: 20 * GiB,
  family: 'disposable',
  category: 'download_collection',
  eff_decision: 'discard',
  decision: 'discard',
  tag_ids: [9],
})
const natal = entryRow({ id: '4', name: 'NATAL.JPG', path: 'NATAL.JPG', file_kind: 'image', total_bytes: 5 * GiB, family: 'personal' })

function file(index: number): EntryRow {
  const name = `file ${index}`
  return entryRow({ id: String(100 + index), name, path: name, total_bytes: 1000 - index })
}

interface MapApi {
  children?: (request: Request) => Response
  other?: { count: number; bytes: number }
}

function mapRoutes({ children, other = { count: 0, bytes: 0 } }: MapApi = {}) {
  return {
    'GET /api/session': () => jsonResponse(200, signedIn),
    'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource()] }),
    'GET /api/tags': () => jsonResponse(200, { tags: [{ id: 9, name: 'old', own_count: 1 }] }),
    'GET /api/entries/1': () => jsonResponse(200, entryDetail(root, { ancestors: [] })),
    'GET /api/entries/1/treemap': () => jsonResponse(200, { entry: root, items: [fotos, downloads, natal], other }),
    'GET /api/entries/1/children': children ?? (() => jsonResponse(200, { items: [fotos, downloads, natal], next_cursor: null })),
    'GET /api/entries/2': () => jsonResponse(200, entryDetail(fotos)),
    'GET /api/entries/2/treemap': () =>
      jsonResponse(200, { entry: fotos, items: [natal], other: { count: 0, bytes: 0 } }),
    'GET /api/entries/2/children': () => jsonResponse(200, { items: [natal], next_cursor: null }),
    'GET /api/entries/4': () => jsonResponse(200, entryDetail(natal)),
  }
}

function requestsTo(requests: Request[], path: string) {
  return requests.filter((r) => new URL(r.url).pathname === path).map((r) => new URL(r.url).searchParams)
}

function table() {
  return screen.findByRole('table', { name: /^Contents of/ })
}

function rowOf(name: string) {
  const cell = screen.getByRole('link', { name })
  const row = cell.closest('[role="row"]')
  if (!(row instanceof HTMLElement)) {
    throw new Error(`no row for ${name}`)
  }
  return row
}

describe('Map screen', () => {
  it('starts at the top folder of the first source', async () => {
    stubApi(mapRoutes())
    const { router } = renderApp('/map')
    await waitFor(() => expect(router.state.location.pathname).toBe('/map/1'))
    expect(await screen.findByRole('table', { name: 'Contents of Fotos' })).toBeInTheDocument()
  })

  it('points to Sources when there is no source', async () => {
    stubApi({
      'GET /api/session': () => jsonResponse(200, signedIn),
      'GET /api/sources': () => jsonResponse(200, { sources: [] }),
    })
    renderApp('/map')
    expect(await screen.findByRole('link', { name: 'Add a source' })).toHaveAttribute('href', '/sources')
  })

  it('shows the remaining children as one area that does not open', async () => {
    stubApi(mapRoutes({ other: { count: 700, bytes: 3 * GiB } }))
    const { router } = renderApp('/map/1')
    await table()

    await waitFor(() => expect(FakeChart.latest().data()).toHaveLength(4))
    const tiles = FakeChart.latest().data()
    expect(tiles.map((tile) => tile.name)).toEqual(['Fotos', 'Downloads', 'NATAL.JPG', '700 more items'])
    expect(tiles[3]).toMatchObject({ id: 'other', value: 3 * GiB })

    const areas = within(screen.getByRole('list', { name: 'Areas of the treemap' }))
    expect(areas.getByText('700 more items, 3 GiB').closest('button')).toBeNull()
    expect(areas.getByRole('button', { name: /^Open Fotos \(40\sGiB\)$/ })).toBeInTheDocument()
    expect(within(screen.getByRole('list', { name: 'Legend' })).getByText('Other items')).toBeInTheDocument()

    act(() => FakeChart.latest().emit('click', 'other'))
    act(() => FakeChart.latest().emit('mouseover', 'other'))
    expect(router.state.location.pathname).toBe('/map/1')
    expect(router.state.location.search).toBe('')
    expect(document.querySelector('[data-hovered]')).toBeNull()
  })

  it('keeps the treemap and the table in step', async () => {
    const requests = stubApi(mapRoutes())
    const { router } = renderApp('/map/1')
    await table()
    await waitFor(() => expect(FakeChart.latest().data()).toHaveLength(3))
    const chart = FakeChart.latest()

    // Hovering a row marks its area.
    await userEvent.hover(rowOf('Downloads'))
    await waitFor(() => expect(chart.data().find((tile) => tile.id === '3')?.itemStyle.borderWidth).toBe(3))
    expect(chart.data().find((tile) => tile.id === '2')?.itemStyle.borderWidth).toBe(1)
    await userEvent.unhover(rowOf('Downloads'))

    // Hovering an area marks its row.
    act(() => chart.emit('mouseover', '2'))
    expect(rowOf('Fotos')).toHaveAttribute('data-hovered', 'true')
    act(() => chart.emit('mouseout', null))
    expect(rowOf('Fotos')).not.toHaveAttribute('data-hovered')

    // Selecting a row opens its details and marks its area.
    await userEvent.click(within(rowOf('NATAL.JPG')).getAllByRole('cell')[1]!)
    expect(router.state.location.search).toBe('?entry=4')
    expect(await screen.findByRole('complementary', { name: 'NATAL.JPG' })).toBeInTheDocument()
    expect(rowOf('NATAL.JPG')).toHaveAttribute('aria-selected', 'true')
    await waitFor(() => expect(chart.data().find((tile) => tile.id === '4')?.itemStyle.borderColor).toBe('#0f172a'))

    // Clicking a folder's area opens it in both.
    act(() => chart.emit('click', '2'))
    await waitFor(() => expect(router.state.location.pathname).toBe('/map/2'))
    expect(await screen.findByRole('table', { name: 'Contents of Fotos' })).toBeInTheDocument()
    const path = within(screen.getByRole('navigation', { name: 'Folder path' }))
    expect(path.getByRole('link', { name: 'Fotos' })).toHaveAttribute('href', '/map/1')
    expect(requestsTo(requests, '/api/entries/2/treemap')).toHaveLength(1)
    expect(requestsTo(requests, '/api/entries/2/children')).toHaveLength(1)
    await waitFor(() => expect(FakeChart.latest().data().map((tile) => tile.name)).toEqual(['NATAL.JPG']))
  })

  it('opens a folder from its name in the table', async () => {
    stubApi(mapRoutes())
    const { router } = renderApp('/map/1?color=decision')
    await table()
    await userEvent.click(screen.getByRole('link', { name: 'Fotos' }))
    expect(router.state.location.pathname).toBe('/map/2')
    expect(router.state.location.search).toBe('?color=decision')
  })

  it('sorts on the server, toggling the order', async () => {
    const requests = stubApi(mapRoutes())
    renderApp('/map/1')
    await table()
    const header = () => screen.getByRole('columnheader', { name: /Files/ })
    expect(screen.getByRole('columnheader', { name: /Size/ })).toHaveAttribute('aria-sort', 'descending')

    await userEvent.click(within(header()).getByRole('button'))
    await waitFor(() => expect(header()).toHaveAttribute('aria-sort', 'descending'))
    await userEvent.click(within(header()).getByRole('button'))
    await waitFor(() => expect(header()).toHaveAttribute('aria-sort', 'ascending'))
    await userEvent.click(within(screen.getByRole('columnheader', { name: /Name/ })).getByRole('button'))

    expect(
      requestsTo(requests, '/api/entries/1/children').map((p) => `${p.get('sort')} ${p.get('order')}`),
    ).toEqual(['bytes desc', 'files desc', 'files asc', 'name asc'])
  })

  it('loads the next page by cursor when scrolled to the end', async () => {
    const first = Array.from({ length: 200 }, (_, i) => file(i))
    const second = Array.from({ length: 50 }, (_, i) => file(200 + i))
    const requests = stubApi(
      mapRoutes({
        children: (request) =>
          new URL(request.url).searchParams.get('cursor') === 'page-2'
            ? jsonResponse(200, { items: second, next_cursor: null })
            : jsonResponse(200, { items: first, next_cursor: 'page-2' }),
      }),
    )
    renderApp('/map/1')
    await table()
    expect(screen.getByRole('link', { name: 'file 0' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'file 199' })).not.toBeInTheDocument()
    expect(requestsTo(requests, '/api/entries/1/children')).toHaveLength(1)

    const scroller = screen.getAllByRole('rowgroup')[1]!
    scroller.scrollTop = 200 * 36 - 640
    fireEvent.scroll(scroller)
    expect(await screen.findByRole('link', { name: 'file 199' })).toBeInTheDocument()
    await waitFor(() => expect(requestsTo(requests, '/api/entries/1/children')).toHaveLength(2))
    expect(requestsTo(requests, '/api/entries/1/children')[1]?.get('cursor')).toBe('page-2')

    scroller.scrollTop = 250 * 36 - 640
    fireEvent.scroll(scroller)
    expect(await screen.findByRole('link', { name: 'file 249' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument()
    expect(requestsTo(requests, '/api/entries/1/children')).toHaveLength(2)
  })

  it('colors by decision and by tag, with a legend', async () => {
    stubApi(mapRoutes())
    renderApp('/map/1')
    await table()
    await waitFor(() => expect(FakeChart.latest().data()).toHaveLength(3))
    const colors = () => Object.fromEntries(FakeChart.latest().data().map((tile) => [tile.name, tile.itemStyle.color]))
    expect(colors()).toEqual({ Fotos: '#16a34a', Downloads: '#ea580c', 'NATAL.JPG': '#16a34a' })

    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Color by' }), 'decision')
    await waitFor(() => expect(colors()).toEqual({ Fotos: '#16a34a', Downloads: '#dc2626', 'NATAL.JPG': '#a1a1aa' }))
    expect(
      within(screen.getByRole('list', { name: 'Legend' }))
        .getAllByRole('listitem')
        .map((item) => item.textContent),
    ).toEqual(['Undecided', 'Keep', 'Discard', 'Later'])

    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Color by' }), 'tag')
    await userEvent.selectOptions(await screen.findByRole('combobox', { name: 'Tag to show' }), 'old')
    await waitFor(() => expect(colors()).toEqual({ Fotos: '#d4d4d8', Downloads: '#7c3aed', 'NATAL.JPG': '#d4d4d8' }))
  })
})
