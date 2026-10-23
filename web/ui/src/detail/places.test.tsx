import { screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import type { EntryDetail, EntryRow, PlaceSums } from '@/api/entries'
import { copyOf, coverage, entryDetail, entryRow, folderRow, fotosSource, usbSource } from '@/test/fixtures'
import { jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'

// The detail panel's places in the Map's Only here mode (R5c design D5, D8,
// D14; the inventory-explorer requirement "The detail panel shows places").

const MiB = 1024 ** 2
const GiB = 1024 ** 3

function sums(overrides: Partial<PlaceSums> = {}): PlaceSums {
  const none = { files: 0, bytes: 0 }
  return {
    only_copy: none,
    this_source: none,
    other_source: none,
    unchecked: none,
    unreadable: none,
    disposable: none,
    ...overrides,
  }
}

// placeFields are the R5c fields of an entry row, which the server sends
// only with places=1.
const placeFields = ['place', 'place_offline', 'disposable', 'places', 'member_places', 'other_copy', 'covered_by']

function withoutPlaces(detail: EntryDetail): EntryDetail {
  const entry = Object.fromEntries(Object.entries(detail.entry).filter(([key]) => !placeFields.includes(key)))
  const rest: Partial<EntryDetail> = { ...detail }
  delete rest.places_updating
  return { ...(rest as EntryDetail), entry: entry as unknown as EntryRow }
}

// show opens the panel of detail at path and answers its entry request as
// the server does: with the places only when places=1 is asked for.
async function show(detail: EntryDetail, path: string) {
  const requests = stubApi({
    'GET /api/session': () => jsonResponse(200, signedIn),
    'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource(), usbSource()] }),
    'GET /api/tags': () => jsonResponse(200, { tags: [] }),
    'GET /api/search': () => jsonResponse(200, { items: [], next_cursor: null, count: 0 }),
    [`GET /api/entries/${detail.entry.id}`]: (request) =>
      jsonResponse(
        200,
        new URL(request.url).searchParams.get('places') === '1' ? detail : withoutPlaces(detail),
      ),
  })
  renderApp(path)
  const panel = within(await screen.findByRole('complementary', { name: detail.entry.name }))
  return { panel, requests }
}

function entryRequests(requests: Request[], id: string) {
  return requests.map((r) => new URL(r.url)).filter((url) => url.pathname === `/api/entries/${id}`)
}

function items(list: HTMLElement) {
  return within(list)
    .getAllByRole('listitem')
    .map((item) => item.textContent.replace(/\s+/g, ' '))
}

const fotos2004 = folderRow('40', 'Fotos 2004', {
  path: 'Fotos 2004',
  place: 'only_copy',
  places: sums({
    only_copy: { files: 3, bytes: 2 * GiB },
    this_source: { files: 5, bytes: 1 * GiB },
    other_source: { files: 40, bytes: 6 * GiB },
    disposable: { files: 4, bytes: 2 * MiB },
  }),
})

// curriculo has one other copy, on the old disk.
const curriculo = entryRow({
  id: '8',
  name: 'curriculo.doc',
  path: 'Documentos/curriculo.doc',
  file_kind: 'document',
  content_state: 'hashed',
  copies: 2,
  place: 'other_source',
  place_offline: false,
  disposable: false,
})
const backupCopy = entryRow({ id: '901', source_id: 'old-disk', path: 'Backup/curriculo.doc' })

function curriculoDetail(row: EntryRow = curriculo): EntryDetail {
  return entryDetail(row, {
    content: {
      state: 'hashed',
      sha256: 'ab'.repeat(32),
      checked_at: null,
      copies: [copyOf(row), copyOf(backupCopy)],
      copies_count: 2,
    },
  })
}

describe('The detail panel shows places', () => {
  // The inventory-explorer scenario "A folder's places in the panel".
  it('lists a folder’s files and bytes per place and its disposable files, with the checked share', async () => {
    const { panel, requests } = await show(
      entryDetail(fotos2004, { coverage: coverage(), places_updating: false }),
      '/map/1?places=1&entry=40',
    )

    const places = within(await panel.findByRole('region', { name: 'Where copies are' }))
    expect(items(places.getByRole('list', { name: 'Where copies are' }))).toEqual([
      'Only copy2 GiB · 3 files',
      'Copies only on this source1 GiB · 5 files',
      'Has a copy on another source6 GiB · 40 files',
      'Disposable files2 MiB · 4 files',
    ])
    expect(places.getByText('On all disks, 89% of what could have a copy is checked.')).toBeInTheDocument()
    expect(places.queryByText(/^Updating/)).not.toBeInTheDocument()
    expect(entryRequests(requests, '40').map((url) => url.searchParams.get('places'))).toEqual(['1'])
  })

  it('says when a folder’s figures are not computed yet, or are being updated', async () => {
    const { panel } = await show(
      entryDetail(folderRow('41', 'Novos', { place: 'not_computed', places: null }), { places_updating: true }),
      '/map/1?places=1&entry=41',
    )
    const places = within(await panel.findByRole('region', { name: 'Where copies are' }))
    expect(places.getByText(/^Not computed yet/)).toBeInTheDocument()
    expect(places.getByText(/^Updating…/)).toBeInTheDocument()
    expect(places.queryByRole('list')).not.toBeInTheDocument()
  })

  // The inventory-explorer scenario "A file's place in the panel".
  it('says that a file has a copy on another source, above the copy’s path', async () => {
    const { panel } = await show(curriculoDetail(), '/map/1?places=1&entry=8')

    const copies = within(await panel.findByRole('region', { name: 'Copies' }))
    const sentence = await copies.findByText('It has a copy on another source.')
    const path = copies.getByRole('link', { name: 'Backup/curriculo.doc' })
    expect(sentence.compareDocumentPosition(path) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(within(copies.getByRole('list', { name: 'Other copies' })).getByRole('listitem')).toHaveTextContent(
      /^Backup\/curriculo\.docOld disk/,
    )
    expect(panel.queryByRole('region', { name: 'Where copies are' })).not.toBeInTheDocument()
  })

  it.each([
    ['other_source', true, 'It has a copy on another source, which is not connected.'],
    ['this_source', false, 'Its other copy is also on this source.'],
  ] as const)('words a file placed %s (not connected: %s)', async (place, offline, text) => {
    const row = { ...curriculo, place, place_offline: offline }
    const { panel } = await show(curriculoDetail(row), '/map/1?places=1&entry=8')
    expect(await within(await panel.findByRole('region', { name: 'Copies' })).findByText(text)).toBeInTheDocument()
  })

  it('says once that a file not checked yet has no known copies', async () => {
    const row = { ...curriculo, content_state: 'pending' as const, copies: null, place: 'unchecked' as const }
    const { panel } = await show(
      entryDetail(row, { content: { state: 'pending', sha256: null, checked_at: null, copies: [], copies_count: 0 } }),
      '/map/1?places=1&entry=8',
    )
    const copies = within(await panel.findByRole('region', { name: 'Copies' }))
    expect(
      await copies.findByText('Not checked yet, so it is not known whether it has another copy.'),
    ).toBeInTheDocument()
    expect(copies.queryByText('Not checked for copies yet.')).not.toBeInTheDocument()
  })

  it('says that a member with no copy of its own is covered by its archive’s copy', async () => {
    const member = entryRow({
      id: 'm70',
      name: 'leiame.txt',
      path: 'Downloads/pacote.zip/leiame.txt',
      file_kind: 'document',
      archive_id: '61',
      content_state: 'hashed',
      copies: 1,
      place: 'other_source',
      place_offline: false,
    })
    const { panel } = await show(
      entryDetail(member, {
        content: { state: 'hashed', sha256: 'cd'.repeat(32), checked_at: null, copies: [copyOf(member)], copies_count: 1 },
      }),
      '/map/61?places=1&entry=m70',
    )
    const copies = within(await panel.findByRole('region', { name: 'Copies' }))
    expect(await copies.findByText('The archive it is in has a copy on another source.')).toBeInTheDocument()
    expect(copies.queryByText('It has a copy on another source.')).not.toBeInTheDocument()
  })

  it('gives a complete archive’s places as its items’ unpacked sizes', async () => {
    const zip = entryRow({
      id: '61',
      name: 'pacote.zip',
      path: 'Downloads/pacote.zip',
      file_kind: 'archive',
      archive_state: 'complete',
      content_state: 'hashed',
      copies: 1,
      size: 40 * MiB,
      total_bytes: 40 * MiB,
      place: 'only_copy',
      // The split of its own 40 MiB, as its row in its folder shows it.
      places: sums({ only_copy: { files: 1, bytes: 30 * MiB }, other_source: { files: 0, bytes: 10 * MiB } }),
      member_places: sums({
        only_copy: { files: 12, bytes: 75 * MiB },
        other_source: { files: 8, bytes: 25 * MiB },
        disposable: { files: 1, bytes: 1 * MiB },
      }),
    })
    const { panel } = await show(
      entryDetail(zip, {
        archive: { format: 'zip', state: 'complete', detail: null, members: 21, unpacked_bytes: 101 * MiB },
        content: { state: 'hashed', sha256: 'ef'.repeat(32), checked_at: null, copies: [copyOf(zip)], copies_count: 1 },
      }),
      '/map/1?places=1&entry=61',
    )

    const places = within(await panel.findByRole('region', { name: 'Where copies are' }))
    expect(places.getByText('These are the sizes of the items inside, unpacked.')).toBeInTheDocument()
    expect(items(places.getByRole('list', { name: 'Where copies are' }))).toEqual([
      'Only copy75 MiB · 12 files',
      'Has a copy on another source25 MiB · 8 files',
      'Disposable files1 MiB · 1 file',
    ])
    // Its members place it; the archive file's own copies say no more.
    const copies = within(panel.getByRole('region', { name: 'Copies' }))
    expect(copies.getByText('No other copy found.')).toBeInTheDocument()
    expect(copies.queryByText(/^It is the only copy/)).not.toBeInTheDocument()
  })

  it.each([
    ['folder', folderRow('42', 'Antigos', { state: 'missing', place: null, places: sums() }), null],
    [
      'file',
      { ...curriculo, id: '43', name: 'velho.doc', state: 'missing' as const, place: null },
      curriculoDetail().content,
    ],
  ])('shows no place for a missing %s', async (_, row, content) => {
    const { panel } = await show(entryDetail(row, { content }), `/map/1?places=1&entry=${row.id}`)
    expect(await panel.findByText('This item was not found in the last scan.', { exact: false })).toBeInTheDocument()
    expect(panel.queryByRole('region', { name: 'Where copies are' })).not.toBeInTheDocument()
    expect(panel.queryByText(/^(It is|It has|Its other|The archive it is in|Not checked yet,|It could not)/)).toBeNull()
  })

  it('asks for no places outside the Only here mode', async () => {
    const { panel, requests } = await show(entryDetail(fotos2004), '/search?entry=40')
    expect(await panel.findByRole('region', { name: 'Classification' })).toBeInTheDocument()
    expect(panel.queryByRole('region', { name: 'Where copies are' })).not.toBeInTheDocument()
    expect(entryRequests(requests, '40').map((url) => url.searchParams.has('places'))).toEqual([false])
  })
})
