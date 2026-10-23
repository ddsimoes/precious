import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { Coverage } from '@/api/content'
import type { EntryDetail, EntryRow, OtherCopy, PlaceSums } from '@/api/entries'
import { FakeChart } from '@/test/fakeChart'
import { coverage, entryDetail, entryRow, folderRow, fotosSource, usbSource } from '@/test/fixtures'
import { jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'

// vi.mock is hoisted above the static imports, so its factory loads the
// fake itself.
vi.mock('@/map/echarts', async () => {
  const { FakeChart } = await import('@/test/fakeChart')
  return { init: () => new FakeChart() }
})

// The Map's Only here mode (R5c design D7, D8, D9), over answers shaped as
// the read API's with places=1: fotos, its other online source backup, and
// the offline Old disk.

const GiB = 1024 ** 3

beforeEach(() => {
  FakeChart.instances = []
})

// sums are a folder's PlaceSums: bytes by place, and one file per place
// holding bytes unless files says otherwise.
function sums(bytes: Partial<Record<keyof PlaceSums, number>>, files: Partial<Record<keyof PlaceSums, number>> = {}) {
  const amount = (key: keyof PlaceSums) => ({
    bytes: bytes[key] ?? 0,
    files: files[key] ?? ((bytes[key] ?? 0) > 0 ? 1 : 0),
  })
  return {
    only_copy: amount('only_copy'),
    this_source: amount('this_source'),
    other_source: amount('other_source'),
    unchecked: amount('unchecked'),
    unreadable: amount('unreadable'),
    disposable: amount('disposable'),
  } satisfies PlaceSums
}

function otherCopy(ref: string, path: string, overrides: Partial<OtherCopy> = {}): OtherCopy {
  return {
    ref,
    source_id: 'fotos',
    path,
    path_b64: btoa(path),
    archive_id: null,
    offline: false,
    via_archive: false,
    more: 0,
    ...overrides,
  } as OtherCopy
}

// placed is a row as children answer it with places=1.
function placed(row: EntryRow, overrides: Partial<EntryRow>): EntryRow {
  return {
    ...row,
    place: null,
    place_offline: false,
    disposable: false,
    places: null,
    other_copy: null,
    covered_by: null,
    ...overrides,
  }
}

const backupSource = fotosSource({ id: 'backup', label: 'Backup disk', root_entry_id: '800' })

const rootSums = sums({
  only_copy: 2 * GiB,
  this_source: 1 * GiB,
  other_source: 4 * GiB,
  unchecked: 2 * GiB,
  disposable: 1 * GiB,
})
const root = placed(folderRow('1', 'fotos', { path: '', path_b64: '' }), { place: 'only_copy', places: rootSums })

const unico = placed(folderRow('5', 'Unico'), { place: 'only_copy', places: rootSums })
const mesma = placed(folderRow('6', 'Mesma'), {
  place: 'this_source',
  places: sums({ this_source: 1 * GiB, other_source: 1 * GiB }),
})
const copiada = placed(folderRow('7', 'Copiada'), {
  place: 'other_source',
  places: sums({ other_source: 5 * GiB, disposable: 1024 }, { disposable: 3 }),
  covered_by: {
    kind: 'same',
    other: { id: '321', source_id: 'backup', path: 'Fotos 2004', path_b64: btoa('Fotos 2004') },
  },
})
const dentro = placed(folderRow('8', 'Dentro'), {
  place: 'other_source',
  places: sums({ other_source: 1 * GiB }),
  covered_by: { kind: 'inside', other: { id: '322', source_id: 'fotos', path: 'Tudo', path_b64: btoa('Tudo') } },
})
const pendentes = placed(folderRow('9', 'Pendentes'), { place: 'unchecked', places: sums({ unchecked: 1 * GiB }) })
const parcial = placed(folderRow('10', 'Parcial', { partial: true }), {
  place: 'unreadable',
  places: sums({ other_source: 1 * GiB }),
})
const fechada = placed(folderRow('11', 'Fechada', { state: 'unreadable', total_bytes: 0, total_files: 0 }), {
  place: 'unreadable',
  places: sums({}),
})
const lixo = placed(folderRow('12', 'Lixo'), { place: 'none', places: sums({ disposable: 1024 }, { disposable: 2 }) })
const vazia = placed(folderRow('13', 'Vazia', { total_bytes: 0, total_files: 0 }), { place: 'none', places: sums({}) })
const novos = placed(folderRow('14', 'Novos'), { place: 'not_computed' })

function file(id: string, name: string, overrides: Partial<EntryRow>): EntryRow {
  return placed(entryRow({ id, name, name_b64: btoa(name), path: name, path_b64: btoa(name) }), overrides)
}

const carta = file('20', 'carta.doc', { place: 'only_copy' })
const foto = file('21', 'foto.jpg', {
  place: 'this_source',
  other_copy: otherCopy('40', 'Outra/foto.jpg', { more: 1 }),
})
const musica = file('22', 'musica.mp3', {
  place: 'other_source',
  place_offline: true,
  other_copy: otherCopy('50', 'Musica/musica.mp3', { source_id: 'old-disk', offline: true }),
})
const nota = file('23', 'nota.txt', {
  place: 'this_source',
  other_copy: otherCopy('m456', 'Arquivo/backup.tar.gz!docs/nota.txt', {
    archive_id: '45',
    archive_path: 'Arquivo/backup.tar.gz',
    member_path: 'docs/nota.txt',
  }),
})
const novo = file('24', 'novo.bin', { place: 'unchecked' })
const ruim = file('25', 'ruim.jpg', { place: 'unreadable' })
const vazio = file('26', 'vazio.txt', { place: 'none', size: 0, total_bytes: 0 })
const atalho = file('27', 'atalho', { place: 'none', kind: 'symlink', file_kind: null, size: 0, total_bytes: 0 })
const thumbs = file('28', 'Thumbs.db', {
  place: 'other_source',
  disposable: true,
  other_copy: otherCopy('60', 'Thumbs.db', { source_id: 'backup' }),
})
const sumiu = file('29', 'sumiu.txt', { state: 'missing', place: null })

const folders = [unico, mesma, copiada, dentro, pendentes, parcial, fechada, lixo, vazia, novos]
const files = [carta, foto, musica, nota, novo, ruim, vazio, atalho, thumbs, sumiu]
const rows = [...folders, ...files]

// The archive Backup.zip, open: its members' unpacked sums, and a member
// whose only copy is the zip's own copy on the backup source.
const zip = placed(
  entryRow({
    id: '61',
    name: 'Backup.zip',
    path: 'Backup.zip',
    file_kind: 'archive',
    archive_state: 'complete',
    content_state: 'hashed',
  }),
  {
    place: 'other_source',
    places: sums({ other_source: 1 * GiB }),
    member_places: sums({ other_source: 3 * GiB, unchecked: 1 * GiB }),
  },
)
const member = placed(
  entryRow({ id: 'm71', name: 'leiame.txt', path: 'Backup.zip!leiame.txt', archive_id: '61', decision: null }),
  {
    place: 'other_source',
    other_copy: otherCopy('62', 'Copias/Backup.zip', { source_id: 'backup', via_archive: true }),
  },
)

interface Answers {
  root?: Partial<EntryDetail>
  children?: EntryRow[]
}

function routes({ root: rootDetail = {}, children = rows }: Answers = {}) {
  return {
    'GET /api/session': () => jsonResponse(200, signedIn),
    'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource(), backupSource, usbSource()] }),
    'GET /api/tags': () => jsonResponse(200, { tags: [] }),
    'GET /api/entries/1': () =>
      jsonResponse(200, entryDetail(root, { ancestors: [], places_updating: false, ...rootDetail })),
    'GET /api/entries/1/treemap': () =>
      jsonResponse(200, { entry: root, items: folders, other: { count: 0, bytes: 0 } }),
    'GET /api/entries/1/children': () => jsonResponse(200, { items: children, next_cursor: null }),
    'GET /api/entries/5': () => jsonResponse(200, entryDetail(unico, { places_updating: false })),
    'GET /api/entries/5/treemap': () =>
      jsonResponse(200, { entry: unico, items: [carta], other: { count: 0, bytes: 0 } }),
    'GET /api/entries/5/children': () => jsonResponse(200, { items: [carta], next_cursor: null }),
    'GET /api/entries/61': () => jsonResponse(200, entryDetail(zip, { places_updating: false })),
    'GET /api/entries/61/treemap': () =>
      jsonResponse(200, { entry: zip, items: [member], other: { count: 0, bytes: 0 } }),
    'GET /api/entries/61/children': () => jsonResponse(200, { items: [member], next_cursor: null }),
  }
}

function requestsTo(requests: Request[], path: string) {
  return requests.filter((r) => new URL(r.url).pathname === path).map((r) => new URL(r.url).searchParams)
}

function rowOf(name: string) {
  const row = screen.getByRole('link', { name }).closest('[role="row"]')
  if (!(row instanceof HTMLElement)) {
    throw new Error(`no row for ${name}`)
  }
  return row
}

function cellOf(name: string, column: string) {
  const headers = screen.getAllByRole('columnheader').map((header) => header.textContent)
  const cell = within(rowOf(name)).getAllByRole('cell')[headers.indexOf(column)]
  if (cell === undefined) {
    throw new Error(`no ${column} cell for ${name}`)
  }
  return cell
}

// placeOf is the text of a row's Where copies are cell, without its bar.
function placeOf(name: string) {
  return cellOf(name, 'Where copies are').textContent
}

function table() {
  return screen.findByRole('table', { name: /^Contents of/ })
}

describe('Map Only here mode', () => {
  it('states each place in words, with the emphasis of the place', async () => {
    stubApi(routes())
    renderApp('/map/1?places=1')
    await table()

    const expected: [string, string, string | null][] = [
      ['Unico', '2 GiB only copy', 'strong'],
      ['Mesma', '1 GiB only on this source', 'light'],
      ['Copiada', 'All also on another source, apart from 3 disposable files', 'muted'],
      ['Dentro', 'All also on another source', 'muted'],
      ['Pendentes', 'Not checked yet', 'unknown'],
      ['Parcial', 'Partly unreadable', 'unknown'],
      ['Fechada', 'Could not be read', 'unknown'],
      ['Lixo', 'Only disposable files', 'muted'],
      ['Vazia', 'Nothing with content', 'muted'],
      ['Novos', 'Not computed yet', 'unknown'],
      ['carta.doc', 'Only copy', 'strong'],
      ['foto.jpg', '3 copies, all on this source', 'light'],
      ['musica.mp3', 'Copy on another source (the other source is not connected)', 'muted'],
      ['nota.txt', '2 copies, all on this source', 'light'],
      ['novo.bin', 'Not checked yet', 'unknown'],
      ['ruim.jpg', 'Could not be read', 'unknown'],
      ['vazio.txt', 'Empty', 'muted'],
      ['atalho', 'No content (link)', 'muted'],
      ['Thumbs.db', 'Copy on another source · disposable', 'muted'],
      ['sumiu.txt', '', null],
    ]
    for (const [name, text, emphasis] of expected) {
      expect(placeOf(name)?.replace(/\s+/g, ' '), name).toBe(text)
      expect(rowOf(name).dataset.emphasis, name).toBe(emphasis ?? undefined)
    }
    // An only copy's name is bold; others are not.
    expect(screen.getByRole('link', { name: 'carta.doc' })).toHaveClass('font-bold')
    expect(screen.getByRole('link', { name: 'Unico' })).toHaveClass('font-bold')
    expect(screen.getByRole('link', { name: 'foto.jpg' })).not.toHaveClass('font-bold')
    // The mode's column takes the place of Has copies.
    expect(screen.queryByRole('columnheader', { name: 'Has copies' })).not.toBeInTheDocument()
  })

  it('draws a folder’s bytes by place as a bar, saying each share in words', async () => {
    stubApi(routes())
    renderApp('/map/1?places=1')
    await table()

    const bar = within(cellOf('Unico', 'Where copies are')).getByRole('img')
    expect(bar).toHaveAccessibleName(
      /^2\sGiB only copy, 1\sGiB only on this source, 4\sGiB with a copy on another source, 2\sGiB not checked yet, 1\sGiB disposable files$/,
    )
    const widths = Object.fromEntries(
      Array.from(bar.querySelectorAll<HTMLElement>('[data-place]')).map((part) => [
        part.dataset.place,
        part.style.width,
      ]),
    )
    expect(widths).toEqual({
      only_copy: '20%',
      this_source: '10%',
      other_source: '40%',
      unchecked: '20%',
      disposable: '10%',
    })
    // A folder not computed yet, and a file, have no bar.
    expect(within(cellOf('Novos', 'Where copies are')).queryByRole('img')).not.toBeInTheDocument()
    expect(within(cellOf('carta.doc', 'Where copies are')).queryByRole('img')).not.toBeInTheDocument()
  })

  it('names another copy under the name, with a link to it', async () => {
    stubApi(routes())
    renderApp('/map/1?places=1')
    await table()

    const fotoCopy = within(rowOf('foto.jpg')).getByRole('link', { name: 'Also at Outra/foto.jpg' })
    expect(fotoCopy).toHaveAttribute('href', '/map/1?places=1&entry=40')
    expect(rowOf('foto.jpg')).toHaveTextContent('+1 more')

    const member = within(rowOf('nota.txt')).getByRole('link', { name: 'Also inside backup.tar.gz › nota.txt' })
    expect(member).toHaveAccessibleDescription('Also inside Arquivo/backup.tar.gz › docs/nota.txt')
    expect(member).toHaveAttribute('href', '/map/1?places=1&entry=m456')

    // A copy on another source names the source.
    expect(within(rowOf('Thumbs.db')).getByRole('link', { name: 'Also at Thumbs.db on Backup disk' })).toHaveAttribute(
      'href',
      '/map/1?places=1&entry=60',
    )
  })

  it('says when the other source is not connected', async () => {
    stubApi(routes())
    renderApp('/map/1?places=1')
    await table()

    const row = within(rowOf('musica.mp3'))
    expect(cellOf('musica.mp3', 'Where copies are')).toHaveTextContent(
      'Copy on another source (the other source is not connected)',
    )
    expect(row.getByRole('link', { name: 'Also at Musica/musica.mp3 on Old disk' })).toHaveAttribute(
      'href',
      '/map/1?places=1&entry=50',
    )
    expect(rowOf('musica.mp3')).toHaveTextContent('Also at Musica/musica.mp3 on Old disk (not connected)')
    // A copy on a connected source says nothing of the kind.
    expect(rowOf('Thumbs.db')).not.toHaveTextContent('not connected')
  })

  it('names the folder holding all of a folder’s content, with a Compare link by decimal IDs', async () => {
    stubApi(routes())
    renderApp('/map/1?places=1')
    await table()

    const copiadaRow = within(rowOf('Copiada'))
    expect(copiadaRow.getByRole('link', { name: 'Same as Fotos 2004 on Backup disk' })).toHaveAttribute(
      'href',
      '/map/1?places=1&entry=321',
    )
    expect(copiadaRow.getByRole('link', { name: 'Compare Copiada with Fotos 2004' })).toHaveAttribute(
      'href',
      '/compare?left=7&right=321',
    )
    const dentroRow = within(rowOf('Dentro'))
    expect(dentroRow.getByRole('link', { name: 'All of it is in Tudo' })).toBeInTheDocument()
    expect(dentroRow.getByRole('link', { name: 'Compare Dentro with Tudo' })).toHaveAttribute(
      'href',
      '/compare?left=8&right=322',
    )
  })

  it('totals the open folder by place above the table', async () => {
    stubApi(routes())
    renderApp('/map/1?places=1')
    await table()

    const totals = within(screen.getByRole('group', { name: 'In this folder:' }))
    expect(totals.getAllByRole('listitem').map((item) => item.textContent?.replace(/\s+/g, ' '))).toEqual([
      '2 GiB only copy',
      '1 GiB only on this source',
      '4 GiB with a copy on another source',
      '2 GiB not checked yet',
      '1 GiB disposable files',
    ])
    expect(totals.queryByRole('status')).not.toBeInTheDocument()
  })

  it('R2.7 Only-copy figures name the checked share', async () => {
    const eighty: Coverage = coverage({
      candidate: { bytes: 90 * GiB, files: 200_000 },
      checked: { bytes: 72 * GiB, files: 160_000 },
      unchecked: { bytes: 17 * GiB, files: 39_000 },
      unreadable: { bytes: 1 * GiB, files: 1_000 },
    })
    stubApi(routes({ root: { coverage: eighty } }))
    renderApp('/map/1?places=1')
    await table()

    const totals = screen.getByRole('group', { name: 'In this folder:' })
    expect(totals).toHaveTextContent(
      'On all sources, 80% of the content that could have a copy has been checked; these figures may still change.',
    )
    // A file not checked yet is never called an only copy.
    expect(placeOf('novo.bin')).toBe('Not checked yet')
    expect(placeOf('Pendentes')).toBe('Not checked yet')
    expect(rowOf('novo.bin').dataset.emphasis).toBe('unknown')
  })

  it('says nothing of the checked share once everything is checked', async () => {
    const all = coverage({
      checked: { bytes: 90 * GiB, files: 200_000 },
      unchecked: { bytes: 0, files: 0 },
      unreadable: { bytes: 0, files: 0 },
    })
    stubApi(routes({ root: { coverage: all } }))
    renderApp('/map/1?places=1')
    await table()

    expect(screen.getByRole('group', { name: 'In this folder:' })).not.toHaveTextContent('On all sources')
  })

  it('says “Updating…” while the figures are being recomputed, and when a folder is not computed yet', async () => {
    stubApi(routes({ root: { places_updating: true, entry: placed(root, { place: 'not_computed', places: null }) } }))
    renderApp('/map/1?places=1')
    await table()

    const totals = within(screen.getByRole('group', { name: 'In this folder:' }))
    expect(totals.getByRole('status')).toHaveTextContent('Updating…')
    expect(totals.getByText('Not computed yet')).toBeInTheDocument()
    // Its file rows still show their places.
    expect(placeOf('carta.doc')).toBe('Only copy')
  })

  it('gives an open archive’s unpacked sizes, and a member inside a copy of the archive', async () => {
    stubApi(routes())
    renderApp('/map/61?places=1')
    await screen.findByRole('table', { name: 'Contents of Backup.zip' })

    const totals = within(screen.getByRole('group', { name: 'In this archive, unpacked sizes:' }))
    expect(totals.getAllByRole('listitem').map((item) => item.textContent?.replace(/\s+/g, ' '))).toEqual([
      '3 GiB with a copy on another source',
      '1 GiB not checked yet',
    ])
    expect(
      within(rowOf('leiame.txt')).getByRole('link', {
        name: 'Inside a copy of Backup.zip at Copias/Backup.zip on Backup disk',
      }),
    ).toHaveAttribute('href', '/map/61?places=1&entry=62')
  })
})

// folderRoutes answer the Map opened at row, which lists nothing.
function folderRoutes(row: EntryRow, detail: Partial<EntryDetail> = {}) {
  return {
    ...routes(),
    [`GET /api/entries/${row.id}`]: () => jsonResponse(200, entryDetail(row, { places_updating: false, ...detail })),
    [`GET /api/entries/${row.id}/treemap`]: () =>
      jsonResponse(200, { entry: row, items: [], other: { count: 0, bytes: 0 } }),
    [`GET /api/entries/${row.id}/children`]: () => jsonResponse(200, { items: [], next_cursor: null }),
  }
}

function listed(group: HTMLElement) {
  return within(group)
    .getAllByRole('listitem')
    .map((item) => item.textContent.replace(/\s+/g, ' '))
}

describe('Map Only here figures and names that read plainly', () => {
  const montagem = placed(folderRow('15', 'Montagem', { mount_boundary: true, total_bytes: 0, total_files: 0 }), {
    place: 'unreadable',
    places: sums({}),
  })

  it.each([
    ['could not be read', fechada, 'Could not be read'],
    ['is a mount point', montagem, 'Partly unreadable'],
  ])('never calls a folder that %s empty (I7)', async (_, row, words) => {
    stubApi(folderRoutes(row))
    renderApp(`/map/${row.id}?places=1&entry=${row.id}`)
    await table()

    const totals = screen.getByRole('group', { name: 'In this folder:' })
    expect(totals).toHaveTextContent(words)
    expect(totals).not.toHaveTextContent('Nothing with content')
    const panel = within(await screen.findByRole('complementary', { name: row.name }))
    const places = within(await panel.findByRole('region', { name: 'Where copies are' }))
    expect(places.getByText(words)).toBeInTheDocument()
    expect(places.queryByText('Nothing here has content to copy.')).not.toBeInTheDocument()
  })

  it('says a folder is partly unreadable beside its figures', async () => {
    stubApi(folderRoutes(parcial))
    renderApp('/map/10?places=1')
    await table()

    const totals = screen.getByRole('group', { name: 'In this folder:' })
    expect(totals).toHaveTextContent('Partly unreadable')
    expect(listed(totals)).toEqual(['1 GiB with a copy on another source'])
  })

  it('totals an open archive by its unpacked sizes while its own split is not computed yet', async () => {
    const fresh = { ...zip, place: 'not_computed' as const, places: null }
    stubApi({
      ...routes(),
      'GET /api/entries/61': () => jsonResponse(200, entryDetail(fresh, { places_updating: true })),
    })
    renderApp('/map/61?places=1')
    await screen.findByRole('table', { name: 'Contents of Backup.zip' })

    const totals = screen.getByRole('group', { name: 'In this archive, unpacked sizes:' })
    expect(within(totals).queryByText('Not computed yet')).not.toBeInTheDocument()
    expect(listed(totals)).toEqual(['3 GiB with a copy on another source', '1 GiB not checked yet'])
  })

  it('gives the same checked share in the totals and, once, in the panel', async () => {
    const almost: Coverage = coverage({
      candidate: { bytes: 1000 * GiB, files: 200_000 },
      checked: { bytes: 996 * GiB, files: 199_000 },
      unchecked: { bytes: 4 * GiB, files: 1_000 },
      unreadable: { bytes: 0, files: 0 },
    })
    stubApi({
      ...routes({ root: { coverage: almost } }),
      'GET /api/entries/5': () => jsonResponse(200, entryDetail(unico, { places_updating: false, coverage: almost })),
    })
    renderApp('/map/1?places=1&entry=5')
    await table()

    expect(screen.getByRole('group', { name: 'In this folder:' })).toHaveTextContent(
      'On all sources, 99% of the content that could have a copy has been checked',
    )
    const panel = within(await screen.findByRole('complementary', { name: 'Unico' }))
    await panel.findByRole('region', { name: 'Where copies are' })
    await panel.findByRole('region', { name: 'Related folders' })
    expect(panel.getAllByText(/^On all disks,/).map((claim) => claim.textContent)).toEqual([
      'On all disks, 99% of what could have a copy is checked.',
    ])
  })

  it('names the archive of a member copy whose folder has a ! in its name', async () => {
    const bang = file('30', 'nota2.txt', {
      place: 'this_source',
      other_copy: otherCopy('m457', 'Importante!/backup.zip!docs/nota2.txt', {
        archive_id: '46',
        archive_path: 'Importante!/backup.zip',
        member_path: 'docs/nota2.txt',
      }),
    })
    stubApi(routes({ children: [bang] }))
    renderApp('/map/1?places=1')
    await table()

    const member = within(rowOf('nota2.txt')).getByRole('link', { name: 'Also inside backup.zip › nota2.txt' })
    expect(member).toHaveAccessibleDescription('Also inside Importante!/backup.zip › docs/nota2.txt')
    expect(member).toHaveAttribute('href', '/map/1?places=1&entry=m457')
  })

  it('names a member’s own archive by its name when its path has a !', async () => {
    const aberto = placed(
      entryRow({
        id: '63',
        name: 'Backup.zip',
        path: 'Fotos!/Backup.zip',
        file_kind: 'archive',
        archive_state: 'complete',
        content_state: 'hashed',
      }),
      {
        place: 'other_source',
        places: sums({ other_source: 1 * GiB }),
        member_places: sums({ other_source: 1 * GiB }),
      },
    )
    const leiame = placed(
      entryRow({
        id: 'm72',
        name: 'leiame.txt',
        path: 'Fotos!/Backup.zip!leiame.txt',
        archive_id: '63',
        decision: null,
      }),
      {
        place: 'other_source',
        other_copy: otherCopy('64', 'Copias/Backup (1).zip', { source_id: 'backup', via_archive: true }),
      },
    )
    stubApi({
      ...routes(),
      'GET /api/entries/63': () => jsonResponse(200, entryDetail(aberto, { places_updating: false })),
      'GET /api/entries/63/treemap': () =>
        jsonResponse(200, { entry: aberto, items: [leiame], other: { count: 0, bytes: 0 } }),
      'GET /api/entries/63/children': () => jsonResponse(200, { items: [leiame], next_cursor: null }),
    })
    renderApp('/map/63?places=1')
    await screen.findByRole('table', { name: 'Contents of Backup.zip' })

    expect(
      within(rowOf('leiame.txt')).getByRole('link', {
        name: 'Inside a copy of Backup.zip at Copias/Backup (1).zip on Backup disk',
      }),
    ).toHaveAttribute('href', '/map/63?places=1&entry=64')
  })

  it('keeps qualifiers whole or cut with the whole sentence as the title, never silently', async () => {
    const desktop = file('33', 'desktop.ini', {
      place: 'other_source',
      place_offline: true,
      disposable: true,
      other_copy: otherCopy('51', 'Windows/desktop.ini', { source_id: 'old-disk', offline: true }),
    })
    stubApi(routes({ children: [copiada, desktop] }))
    renderApp('/map/1?places=1')
    await table()

    // The words after a place's sentence give way with an ellipsis, under
    // a title that holds the whole sentence.
    for (const [row, words, extra] of [
      ['desktop.ini', 'Copy on another source', '(the other source is not connected) · disposable'],
      ['Copiada', 'All also on another source,', 'apart from 3 disposable files'],
    ] as const) {
      const cell = within(cellOf(row, 'Where copies are'))
      expect(cell.getByText(extra)).toHaveClass('min-w-0', 'max-w-full', 'truncate')
      expect(cell.getByTitle(`${words} ${extra}`)).toContainElement(cell.getByText(extra))
    }
  })

  it('lets the sentence words under a name give way first, keeps the end of each name, and the source whole', async () => {
    stubApi(routes())
    renderApp('/map/1?places=1')
    await table()

    for (const [row, words, path, source, long] of [
      ['musica.mp3', 'Also at', 'Musica/musica.mp3', 'on Old disk', true],
      ['Copiada', 'Same as', 'Fotos 2004', 'on Backup disk', false],
    ] as const) {
      const link = within(rowOf(row)).getByRole('link', { name: `${words} ${path} ${source}` })
      // The words shrink first, and far more than the name, down to nothing.
      const sentence = within(link).getByText(words)
      expect(sentence).toHaveClass('min-w-0', 'shrink-[1000000]', 'truncate')
      // A long name is cut from its start, so its end stays, and never below
      // about ten characters; a name as short as that is never cut.
      const name = within(link).getByText(path).closest('[dir="rtl"]')
      expect(name).toHaveClass('truncate')
      if (long) {
        expect(name).toHaveClass('min-w-[min(100%,10ch)]')
        expect(name).not.toHaveClass('shrink-0')
      } else {
        expect(name).toHaveClass('shrink-0')
      }
      expect(within(link).getByText(source)).toHaveClass('shrink-0')
      // The link never gets narrower than the name's kept end and the
      // source, and clips what still does not fit, on a line that never
      // wraps, so that it never draws over what follows it.
      const kept = Math.min(path.length, 10) + source.length
      expect(link.style.minWidth).toMatch(new RegExp(`^min\\(100%, .*\\b${kept}ch\\b`))
      expect(link).toHaveClass('flex', 'shrink-[1000000]', 'overflow-hidden')
      expect(link.parentElement).toHaveClass('flex', 'min-w-0', 'whitespace-nowrap')
    }
    // What follows the link gives way only after it, with an ellipsis and
    // a title.
    const offline = within(rowOf('musica.mp3')).getByText('(not connected)')
    expect(offline).toHaveClass('min-w-0', 'truncate')
    expect(offline).not.toHaveClass('shrink-[1000000]')
    expect(offline).toHaveAttribute('title', '(not connected)')
    expect(offline.parentElement).toBe(
      within(rowOf('musica.mp3')).getByRole('link', { name: /^Also at / }).parentElement,
    )
  })

  it('names another copy by the words it shows, and describes it by its whole path', async () => {
    const funda = file('31', 'funda.jpg', {
      place: 'this_source',
      other_copy: otherCopy('41', 'Fotos/2004/Praia/funda.jpg'),
    })
    const ponto = file('32', '00412', {
      place: 'this_source',
      other_copy: otherCopy('m458', 'Backups/2010/levantamento.zip!gps/pontos/00412', {
        archive_id: '47',
        archive_path: 'Backups/2010/levantamento.zip',
        member_path: 'gps/pontos/00412',
      }),
    })
    const coberta = placed(folderRow('16', 'Coberta'), {
      place: 'other_source',
      places: sums({ other_source: 1 * GiB }),
      covered_by: {
        kind: 'inside',
        other: { id: '323', source_id: 'fotos', path: 'Arquivo/2004/Tudo', path_b64: btoa('Arquivo/2004/Tudo') },
      },
    })
    stubApi(routes({ children: [coberta, funda, ponto] }))
    renderApp('/map/1?places=1')
    await table()

    // A loose copy shows its folder's name and its own; a member copy its
    // archive's file name and its own; a covering folder its parent's name
    // and its own. The whole sentence is the description and the tooltip.
    for (const [row, shown, whole] of [
      ['funda.jpg', 'Also at Praia/funda.jpg', 'Also at Fotos/2004/Praia/funda.jpg'],
      ['00412', 'Also inside levantamento.zip › 00412', 'Also inside Backups/2010/levantamento.zip › gps/pontos/00412'],
      ['Coberta', 'All of it is in 2004/Tudo', 'All of it is in Arquivo/2004/Tudo'],
    ] as const) {
      const link = within(rowOf(row)).getByRole('link', { name: new RegExp(`^${shown.split(' ')[0]} `) })
      expect(link).toHaveAccessibleName(shown)
      expect(link).toHaveAccessibleDescription(whole)
      expect(link).toHaveAttribute('title', whole)
    }

    // Compare, beside the note on the same line, never shrinks.
    const covering = within(rowOf('Coberta')).getByRole('link', { name: 'All of it is in 2004/Tudo' })
    const compare = within(rowOf('Coberta')).getByRole('link', { name: 'Compare Coberta with Arquivo/2004/Tudo' })
    expect(compare).toHaveClass('shrink-0')
    expect(compare.parentElement).toBe(covering.parentElement)
  })

  it('says next to the hide option that it hides empty items and system files too', async () => {
    stubApi(routes())
    renderApp('/map/1?places=1')
    await table()

    const hint = 'Empty items and system files are hidden too.'
    expect(
      screen.getByRole('checkbox', { name: 'Hide what has a copy on another source' }),
    ).toHaveAccessibleDescription(hint)
    expect(screen.getByText(hint)).toBeVisible()
  })
})

describe('Map Only here address', () => {
  it('turns on with the treemap colored by place, asking for the places', async () => {
    const requests = stubApi(routes())
    const { router } = renderApp('/map/1?color=kind')
    await table()
    expect(requestsTo(requests, '/api/entries/1/children').at(-1)?.get('places')).toBeNull()
    // Without the mode, Has copies is shown and the hide option is not.
    expect(screen.getByRole('columnheader', { name: 'Has copies' })).toBeInTheDocument()
    expect(screen.queryByRole('checkbox', { name: 'Hide what has a copy on another source' })).not.toBeInTheDocument()

    const toggle = screen.getByRole('switch', { name: 'Only here' })
    // The switch carries a short hint as its description.
    expect(toggle).toHaveAccessibleDescription(/\S/)
    await userEvent.click(toggle)
    expect(new URLSearchParams(router.state.location.search).get('places')).toBe('1')
    expect(new URLSearchParams(router.state.location.search).get('color')).toBe('places')
    await screen.findByRole('columnheader', { name: 'Where copies are' })
    await waitFor(() => expect(requestsTo(requests, '/api/entries/1/children').at(-1)?.get('places')).toBe('1'))
    expect(requestsTo(requests, '/api/entries/1').at(-1)?.get('places')).toBe('1')
    expect(requestsTo(requests, '/api/entries/1/treemap').at(-1)?.get('places')).toBe('1')

    // Hiding asks the server to leave out what has a copy elsewhere.
    await userEvent.click(screen.getByRole('checkbox', { name: 'Hide what has a copy on another source' }))
    expect(new URLSearchParams(router.state.location.search).get('hide')).toBe('other_source')
    await waitFor(() =>
      expect(requestsTo(requests, '/api/entries/1/children').at(-1)?.get('hide')).toBe('other_source'),
    )
  })

  it('turns off taking away hide, the only-here sort, and the places color', async () => {
    const requests = stubApi(routes())
    const { router } = renderApp('/map/1?places=1&hide=other_source&sort=only_here&order=desc&color=places')
    await table()
    expect(requestsTo(requests, '/api/entries/1/children').at(-1)?.get('sort')).toBe('only_here')

    await userEvent.click(screen.getByRole('switch', { name: 'Only here' }))
    const params = new URLSearchParams(router.state.location.search)
    expect(params.get('places')).toBeNull()
    expect(params.get('hide')).toBeNull()
    expect(params.get('sort')).toBe('bytes')
    expect(params.get('color')).toBe('family')
    await screen.findByRole('columnheader', { name: 'Has copies' })
    const last = requestsTo(requests, '/api/entries/1/children').at(-1)
    expect(last?.get('places')).toBeNull()
    expect(last?.get('hide')).toBeNull()
    expect(last?.get('sort')).toBe('bytes')
  })

  it('keeps another color and sort when turned off', async () => {
    stubApi(routes())
    const { router } = renderApp('/map/1?places=1&color=kind&sort=files')
    await table()

    await userEvent.click(screen.getByRole('switch', { name: 'Only here' }))
    const params = new URLSearchParams(router.state.location.search)
    expect(params.get('color')).toBe('kind')
    expect(params.get('sort')).toBe('files')
  })

  it('stays on when drilling into a folder', async () => {
    const requests = stubApi(routes())
    const { router } = renderApp('/map/1?places=1&hide=other_source&sort=only_here')
    await table()

    const link = screen.getByRole('link', { name: 'Unico' })
    expect(link).toHaveAttribute('href', '/map/5?places=1&hide=other_source&sort=only_here')
    await userEvent.click(link)
    await screen.findByRole('table', { name: 'Contents of Unico' })
    expect(router.state.location.search).toBe('?places=1&hide=other_source&sort=only_here')
    const last = requestsTo(requests, '/api/entries/5/children').at(-1)
    expect(last?.get('places')).toBe('1')
    expect(last?.get('hide')).toBe('other_source')
    expect(last?.get('sort')).toBe('only_here')
    expect(screen.getByRole('switch', { name: 'Only here' })).toBeChecked()
  })

  it('sorts by what is only on this source from the place column, largest first', async () => {
    const requests = stubApi(routes())
    const { router } = renderApp('/map/1?places=1')
    await table()

    await userEvent.click(screen.getByRole('button', { name: 'Where copies are' }))
    expect(router.state.location.search).toBe('?places=1&sort=only_here&order=desc')
    await waitFor(() =>
      expect(screen.getByRole('columnheader', { name: /Where copies are/ })).toHaveAttribute('aria-sort', 'descending'),
    )
    const last = requestsTo(requests, '/api/entries/1/children').at(-1)
    expect(`${last?.get('sort')} ${last?.get('order')}`).toBe('only_here desc')
  })

  it('falls back to sorting by size for sort=only_here without the mode', async () => {
    const requests = stubApi(routes())
    renderApp('/map/1?sort=only_here')
    await table()

    expect(requestsTo(requests, '/api/entries/1/children').map((p) => p.get('sort'))).toEqual(['bytes'])
    expect(screen.getByRole('columnheader', { name: /Size/ })).toHaveAttribute('aria-sort', 'descending')
  })

  it('asks the treemap for places whenever it is colored by them, mode or not', async () => {
    const requests = stubApi(routes())
    renderApp('/map/1?color=places')
    await table()

    expect(requestsTo(requests, '/api/entries/1/treemap').at(-1)?.get('places')).toBe('1')
    expect(requestsTo(requests, '/api/entries/1/children').at(-1)?.get('places')).toBeNull()
    expect(requestsTo(requests, '/api/entries/1').at(-1)?.get('places')).toBeNull()
    await waitFor(() => expect(FakeChart.latest().data()).toHaveLength(folders.length))
    const colors = Object.fromEntries(
      FakeChart.latest()
        .data()
        .map((tile) => [tile.name, tile.itemStyle.color]),
    )
    expect(colors.Unico).toBe('#ef4444')
    expect(colors.Copiada).toBe('#4ade80')
    // Each area also says its place in words.
    const areas = within(screen.getByRole('list', { name: 'Areas of the treemap' }))
    expect(areas.getByRole('button', { name: /^Open Unico \(10\sGiB, Only copy\)$/ })).toBeInTheDocument()
  })
})

describe('Map Only here column in a narrow card', () => {
  let width = 0
  beforeEach(() => {
    // Every observed element reports its size once, as a browser does when
    // observing starts: the card is `width` wide.
    vi.stubGlobal(
      'ResizeObserver',
      class {
        private readonly callback: ResizeObserverCallback
        constructor(callback: ResizeObserverCallback) {
          this.callback = callback
        }
        observe(target: Element) {
          const size = { inlineSize: width, blockSize: 640 }
          this.callback(
            [{ target, borderBoxSize: [size], contentBoxSize: [size] } as unknown as ResizeObserverEntry],
            this as unknown as ResizeObserver,
          )
        }
        unobserve() {}
        disconnect() {}
      },
    )
    vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockImplementation(() => width)
  })
  afterEach(() => {
    vi.restoreAllMocks()
  })

  const headers = () => screen.getAllByRole('columnheader').map((header) => header.textContent)

  it('gives the place column its own track, and the name, which has a second line, the most room', async () => {
    stubApi(routes())
    width = 100 * 16
    renderApp('/map/1?places=1')
    await table()
    await waitFor(() =>
      expect(headers()).toEqual([
        'Name',
        'Size▼',
        'Files',
        'Type or category',
        'Changed',
        'Where copies are',
        'Suggestion',
        'Decision',
      ]),
    )
    const tracks = screen.getAllByRole('row')[0]?.style.gridTemplateColumns.split(/ (?![^(]*\))/)
    expect(tracks?.[0]).toBe('minmax(13rem, 3fr)')
    expect(tracks?.[5]).toBe('minmax(12rem, 1fr)')
  })

  it('hides Files before the place column', async () => {
    stubApi(routes())
    width = 42 * 16
    renderApp('/map/1?places=1')
    await table()
    await waitFor(() => expect(headers()).toEqual(['Name', 'Size▼', 'Type or category', 'Where copies are']))
  })

  it('hides Type or category before the place column, which always stays', async () => {
    stubApi(routes())
    width = 30 * 16
    renderApp('/map/1?places=1')
    await table()
    await waitFor(() => expect(headers()).toEqual(['Name', 'Size▼', 'Where copies are']))
  })

  it('keeps Type or category outside the mode at the same width', async () => {
    stubApi(routes())
    width = 30 * 16
    renderApp('/map/1')
    await table()
    await waitFor(() => expect(headers()).toEqual(['Name', 'Size▼', 'Type or category']))
  })
})
