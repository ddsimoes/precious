import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { EntryRow, InsideItem } from '@/api/entries'
import { dominantFamily, mixedShare } from '@/lib/composition'
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

// The owner's smoke test (design D21): local is mostly photos, but holds a
// downloads folder and an installed program.
const root = folderRow('1', 'fotos', { path: '', path_b64: '', composition: [] })
const local = folderRow('20', 'local', {
  path: 'local',
  total_bytes: 100 * GiB,
  composition: [
    { family: 'personal', bytes: 98 * GiB, files: 9_000 },
    { family: 'programs', bytes: 2 * GiB, files: 300 },
  ],
})
// Almost all personal: the programs share stays under 1%.
const fotos = folderRow('21', 'Fotos', {
  path: 'Fotos',
  total_bytes: 1000 * GiB,
  composition: [
    { family: 'personal', bytes: 997 * GiB, files: 50_000 },
    { family: 'programs', bytes: 3 * GiB, files: 2 },
  ],
})
// A loose photo no rule matches: not classified, counted as personal.
const photo = entryRow({
  id: '40',
  name: 'IMG_0001.JPG',
  path: 'IMG_0001.JPG',
  file_kind: 'image',
  category: 'unknown',
  family: 'containers',
  triage: 'review',
  total_bytes: 2 * 1024 ** 2,
  composition: [{ family: 'personal', bytes: 2 * 1024 ** 2, files: 1 }],
})

const inside: InsideItem[] = [
  {
    entry_id: '31',
    path: 'local/downloads',
    path_b64: btoa('local/downloads'),
    category: 'download_collection',
    family: 'disposable',
    group: false,
    bytes: 1.5 * GiB,
    files: 2,
  },
  {
    entry_id: '32',
    path: 'local/homeplanner',
    path_b64: btoa('local/homeplanner'),
    category: 'application_installation',
    family: 'programs',
    group: true,
    bytes: 0.5 * GiB,
    files: 120,
  },
]

const stats = { dirs: 12, files: 9_300, unreadable: 0, mount_boundaries: 0, by_kind: [], by_year: [] }

function routes(children: EntryRow[] = [local, fotos, photo]) {
  return {
    'GET /api/session': () => jsonResponse(200, signedIn),
    'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource()] }),
    'GET /api/tags': () => jsonResponse(200, { tags: [] }),
    'GET /api/entries/1': () => jsonResponse(200, entryDetail(root, { ancestors: [], stats })),
    'GET /api/entries/1/treemap': () =>
      jsonResponse(200, { entry: root, items: children, other: { count: 0, bytes: 0 } }),
    'GET /api/entries/1/children': () => jsonResponse(200, { items: children, next_cursor: null }),
    'GET /api/entries/20': () => jsonResponse(200, entryDetail(local, { stats: { ...stats, inside } })),
    'GET /api/entries/31': () =>
      jsonResponse(
        200,
        entryDetail(
          folderRow('31', 'downloads', { path: 'local/downloads', category: 'download_collection', family: 'disposable' }),
          { stats: { ...stats, inside: [] } },
        ),
      ),
    'GET /api/entries/40': () => jsonResponse(200, entryDetail(photo)),
    'GET /api/search': () => jsonResponse(200, { items: children, next_cursor: null, count: children.length }),
  }
}

function rowOf(name: string) {
  const row = screen.getByRole('link', { name }).closest('[role="row"]')
  if (!(row instanceof HTMLElement)) {
    throw new Error(`no row for ${name}`)
  }
  return row
}

describe('composition', () => {
  it('finds the dominant family and whether a folder is mixed', () => {
    expect(dominantFamily(undefined)).toBeNull()
    expect(dominantFamily([])).toBeNull()
    // Ties go in the order personal, programs, disposable, containers.
    expect(
      dominantFamily([
        { family: 'containers', bytes: 5, files: 1 },
        { family: 'programs', bytes: 5, files: 1 },
      ]),
    ).toBe('programs')
    expect(mixedShare(local.composition)).toEqual({ family: 'personal', fraction: 0.98 })
    expect(mixedShare(fotos.composition)).toBeNull()
    expect(mixedShare(photo.composition)).toBeNull()
    expect(mixedShare(undefined)).toBeNull()
  })

  it('draws a bar of each folder row in the Map table and states a mixed folder’s share', async () => {
    stubApi(routes())
    renderApp('/map/1')
    await screen.findByRole('table', { name: 'Contents of Fotos' })

    const localRow = within(rowOf('local'))
    expect(localRow.getByRole('img', { name: 'By category: Personal and valuable 98%, Programs and system 2%' })).toBeInTheDocument()
    expect(localRow.getByText('Personal media · 98% personal')).toBeInTheDocument()
    const segments = localRow.getByRole('img').querySelectorAll('[data-family]')
    expect([...segments].map((s) => [s.getAttribute('data-family'), (s as HTMLElement).style.width])).toEqual([
      ['personal', '98%'],
      ['programs', '2%'],
    ])

    // Under 1% elsewhere, the folder is not mixed; its bar still shows it.
    const fotosRow = within(rowOf('Fotos'))
    expect(
      fotosRow.getByRole('img', { name: 'By category: Personal and valuable more than 99%, Programs and system less than 1%' }),
    ).toBeInTheDocument()
    expect(fotosRow.getByText('Personal media')).toBeInTheDocument()

    // A file row has no bar.
    expect(within(rowOf('IMG_0001.JPG')).queryByRole('img')).toBeNull()
  })

  it('colors an unclassified photo personal in the treemap', async () => {
    stubApi(routes())
    renderApp('/map/1')
    await screen.findByRole('table', { name: 'Contents of Fotos' })
    await waitFor(() => expect(FakeChart.latest().data()).toHaveLength(3))
    const colors = Object.fromEntries(FakeChart.latest().data().map((tile) => [tile.name, tile.itemStyle.color]))
    expect(colors).toEqual({ local: '#16a34a', Fotos: '#16a34a', 'IMG_0001.JPG': '#16a34a' })
  })

  it('falls back to the category family for a server without compositions', async () => {
    const old = { ...photo }
    delete old.composition
    stubApi(routes([old]))
    renderApp('/map/1')
    await screen.findByRole('table', { name: 'Contents of Fotos' })
    await waitFor(() => expect(FakeChart.latest().data()).toHaveLength(1))
    expect(FakeChart.latest().data()[0]?.itemStyle.color).toBe('#a1a1aa')
  })

  it('draws the bar in Search results too', async () => {
    stubApi(routes())
    renderApp('/search')
    const results = within(await screen.findByRole('region', { name: 'Results' }))
    expect(
      await results.findByRole('img', { name: 'By category: Personal and valuable 98%, Programs and system 2%' }),
    ).toBeInTheDocument()
  })

  it('shows a folder’s composition and what is inside it in the detail panel, each item a link', async () => {
    stubApi(routes())
    const { router } = renderApp('/map/1?entry=20')
    const panel = within(await screen.findByRole('complementary', { name: 'local' }))

    const byFamily = within(await panel.findByRole('list', { name: 'Size by category' }))
    // Sizes keep their number and unit together with a no-break space.
    const texts = (items: HTMLElement[]) => items.map((item) => item.textContent.replaceAll('\u00a0', ' '))
    expect(texts(byFamily.getAllByRole('listitem'))).toEqual([
      'Personal and valuable 98%98 GiB · 9,000 files',
      'Programs and system 2%2 GiB · 300 files',
    ])
    expect(panel.getByText('Personal media · 98% personal')).toBeInTheDocument()

    const list = within(panel.getByRole('list', { name: 'Inside this folder' }))
    expect(texts(list.getAllByRole('listitem'))).toEqual([
      'downloadsDownloads · 1.5 GiB · 2 files',
      'homeplannerInstalled application · 512 MiB · 120 files · Treated as a whole',
    ])
    await userEvent.click(list.getByRole('link', { name: 'downloads' }))
    expect(router.state.location.search).toBe('?entry=31')
    expect(await screen.findByRole('complementary', { name: 'downloads' })).toBeInTheDocument()
    expect(await screen.findByText('Nothing inside stands out from the rest.')).toBeInTheDocument()
  })

  it('names the family of an unclassified file by its file type', async () => {
    stubApi(routes())
    renderApp('/map/1?entry=40')
    const panel = within(await screen.findByRole('complementary', { name: 'IMG_0001.JPG' }))
    const facts = within(await panel.findByRole('region', { name: 'Classification' }))
    expect(facts.getByText('Not classified')).toBeInTheDocument()
    expect(facts.getByText('Counted as Personal and valuable by its file type')).toBeInTheDocument()
    expect(facts.queryByText('Containers')).toBeNull()
  })
})
