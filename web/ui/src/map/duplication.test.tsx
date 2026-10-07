import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

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
const MiB = 1024 ** 2

beforeEach(() => {
  FakeChart.instances = []
})

const root = folderRow('1', 'fotos', { path: '', path_b64: '' })
// Fotos - Copia: 10 GiB, all checked, 9 GiB of it with another copy.
const copia = folderRow('5', 'Fotos - Copia', {
  candidate_bytes: 10 * GiB,
  checked_bytes: 10 * GiB,
  duplicated_bytes: 9 * GiB,
})
// Documentos: checking is still under way, 1 GiB found so far.
const documentos = folderRow('6', 'Documentos', {
  candidate_bytes: 6 * GiB,
  checked_bytes: 3 * GiB,
  duplicated_bytes: 1 * GiB,
})
// Novos: no figures yet.
const novos = folderRow('7', 'Novos')
const curriculo = entryRow({ id: '8', name: 'curriculo.doc', path: 'curriculo.doc', content_state: 'hashed', copies: 3 })
const unico = entryRow({ id: '9', name: 'unico.txt', path: 'unico.txt', content_state: 'unique_size', copies: 1 })
const pendente = entryRow({ id: '10', name: 'pendente.bin', path: 'pendente.bin', content_state: 'pending', copies: null })
const ilegivel = entryRow({ id: '11', name: 'ilegivel.jpg', path: 'ilegivel.jpg', content_state: 'unreadable', copies: null })
const vazio = entryRow({ id: '13', name: 'vazio.txt', path: 'vazio.txt', size: 0, total_bytes: 0 })
// An archive read completely opens as a folder; one not read is a file.
const emuleZip = entryRow({
  id: '61',
  name: 'eMule0.47c-Installer.zip',
  path: 'eMule0.47c-Installer.zip',
  file_kind: 'archive',
  archive_state: 'complete',
  content_state: 'hashed',
  copies: 1,
  eff_decision: 'keep',
  decision: 'keep',
})
const oldRar = entryRow({ id: '62', name: 'old.zip', path: 'old.zip', file_kind: 'archive', archive_state: null })

const rows = [copia, documentos, novos, curriculo, unico, pendente, ilegivel, vazio, emuleZip, oldRar]

// Members of the archive: a folder and a file, decided with the archive.
const memberDir = folderRow('m70', 'emule-0.47c', {
  path: 'eMule0.47c-Installer.zip!emule-0.47c',
  archive_id: '61',
  decision: null,
  eff_decision: 'keep',
})
const memberFile = entryRow({
  id: 'm71',
  name: 'readme.txt',
  path: 'eMule0.47c-Installer.zip!readme.txt',
  archive_id: '61',
  decision: null,
  eff_decision: 'keep',
  content_state: 'hashed',
  copies: 2,
})

function routes() {
  return {
    'GET /api/session': () => jsonResponse(200, signedIn),
    'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource()] }),
    'GET /api/tags': () => jsonResponse(200, { tags: [] }),
    'GET /api/entries/1': () => jsonResponse(200, entryDetail(root, { ancestors: [] })),
    'GET /api/entries/1/treemap': () => jsonResponse(200, { entry: root, items: rows, other: { count: 0, bytes: 0 } }),
    'GET /api/entries/1/children': () => jsonResponse(200, { items: rows, next_cursor: null }),
    'GET /api/entries/61': () =>
      jsonResponse(
        200,
        entryDetail(emuleZip, {
          archive: { format: 'zip', state: 'complete', detail: null, members: 2, unpacked_bytes: 5 * MiB },
        }),
      ),
    'GET /api/entries/61/treemap': () =>
      jsonResponse(200, { entry: emuleZip, items: [memberDir, memberFile], other: { count: 0, bytes: 0 } }),
    'GET /api/entries/61/children': () => jsonResponse(200, { items: [memberDir, memberFile], next_cursor: null }),
    'GET /api/entries/m71': () =>
      jsonResponse(
        200,
        entryDetail(memberFile, {
          ancestors: [
            { id: '1', name: '', name_b64: '' },
            { id: '61', name: 'eMule0.47c-Installer.zip', name_b64: btoa('eMule0.47c-Installer.zip') },
          ],
          content: { state: 'hashed', sha256: 'ab'.repeat(32), checked_at: null, copies: [], copies_count: 1 },
        }),
      ),
  }
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
  return within(rowOf(name)).getAllByRole('cell')[headers.indexOf(column)]
}

describe('Map duplication', () => {
  it('shows each row’s percent duplicated', async () => {
    stubApi(routes())
    renderApp('/map/1')
    await screen.findByRole('table', { name: 'Contents of Fotos' })

    expect(cellOf('Fotos - Copia', 'Duplicated')).toHaveTextContent('90%')
    expect(cellOf('Documentos', 'Duplicated')).toHaveTextContent('10% so far')
    expect(cellOf('Novos', 'Duplicated')).toHaveTextContent('Not checked')
    expect(cellOf('curriculo.doc', 'Duplicated')).toHaveTextContent('3 copies')
    expect(cellOf('unico.txt', 'Duplicated')).toHaveTextContent('No other copy')
    expect(cellOf('pendente.bin', 'Duplicated')).toHaveTextContent('Not checked')
    expect(cellOf('ilegivel.jpg', 'Duplicated')).toHaveTextContent('Could not be read')
    expect(cellOf('vazio.txt', 'Duplicated')).toHaveTextContent('')
  })

  describe('in a narrow card', () => {
    let width = 0
    beforeEach(() => {
      // Every observed element reports its size once, as a browser does
      // when observing starts: the card is `width` wide.
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

    it('hides Changed and Suggestion first', async () => {
      stubApi(routes())
      width = 52 * 16
      renderApp('/map/1')
      await screen.findByRole('table', { name: 'Contents of Fotos' })
      const headers = () => screen.getAllByRole('columnheader').map((header) => header.textContent)
      await waitFor(() =>
        expect(headers()).toEqual(['Name', 'Size▼', 'Files', 'Type or category', 'Duplicated', 'Decision']),
      )
    })

    it('hides Decision before Duplicated, keeping name, size, and category longest', async () => {
      stubApi(routes())
      width = 45 * 16
      renderApp('/map/1')
      await screen.findByRole('table', { name: 'Contents of Fotos' })
      const headers = () => screen.getAllByRole('columnheader').map((header) => header.textContent)
      await waitFor(() => expect(headers()).toEqual(['Name', 'Size▼', 'Files', 'Type or category', 'Duplicated']))
    })

    it('hides Duplicated after Decision', async () => {
      stubApi(routes())
      width = 40 * 16
      renderApp('/map/1')
      await screen.findByRole('table', { name: 'Contents of Fotos' })
      const headers = () => screen.getAllByRole('columnheader').map((header) => header.textContent)
      await waitFor(() => expect(headers()).toEqual(['Name', 'Size▼', 'Files', 'Type or category']))
    })
  })

  it('colors the treemap by duplication, with a legend of its bands', async () => {
    stubApi(routes())
    const { router } = renderApp('/map/1')
    const user = userEvent.setup()
    await screen.findByRole('table', { name: 'Contents of Fotos' })

    await user.selectOptions(screen.getByRole('combobox', { name: 'Color by' }), 'Duplication')
    expect(router.state.location.search).toBe('?color=duplication')
    const colors = () => Object.fromEntries(FakeChart.latest().data().map((tile) => [tile.name, tile.itemStyle.color]))
    await waitFor(() =>
      expect(colors()).toEqual({
        'Fotos - Copia': '#dc2626',
        Documentos: '#93c5fd',
        Novos: '#93c5fd',
        'curriculo.doc': '#dc2626',
        'unico.txt': '#16a34a',
        'pendente.bin': '#93c5fd',
        'ilegivel.jpg': '#93c5fd',
        'vazio.txt': '#d4d4d8',
        'eMule0.47c-Installer.zip': '#16a34a',
        'old.zip': '#d4d4d8',
      }),
    )
    const legend = within(screen.getByRole('list', { name: 'Legend' }))
    expect(legend.getAllByRole('listitem').map((item) => item.textContent)).toEqual([
      'No other copy',
      'Less than 25% duplicated',
      '25% to 50% duplicated',
      '50% to 75% duplicated',
      '75% or more duplicated',
      'Not checked yet',
      'Nothing to check',
    ])
  })
})

describe('Map inside archives', () => {
  it('drills into an archive read completely, and shows its members', async () => {
    stubApi(routes())
    const { router } = renderApp('/map/1')
    await screen.findByRole('table', { name: 'Contents of Fotos' })

    // An archive read completely leads in like a folder; one not read opens
    // its details.
    expect(screen.getByRole('link', { name: 'eMule0.47c-Installer.zip' })).toHaveAttribute('href', '/map/61')
    expect(screen.getByRole('link', { name: 'old.zip' })).toHaveAttribute('href', '/map/1?entry=62')

    await waitFor(() => expect(FakeChart.latest().data()).toHaveLength(rows.length))
    act(() => FakeChart.latest().emit('click', '61'))
    expect(router.state.location.pathname).toBe('/map/61')
    expect(await screen.findByRole('table', { name: 'Contents of eMule0.47c-Installer.zip' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'emule-0.47c' })).toHaveAttribute('href', '/map/m70')
    expect(cellOf('readme.txt', 'Decision')).toHaveTextContent('With archive')
    expect(within(cellOf('readme.txt', 'Decision')!).getByTitle('Keep (with its archive)')).toBeInTheDocument()
    // Search has no items inside archives.
    expect(screen.queryByRole('link', { name: 'Search in this folder' })).not.toBeInTheDocument()
  })

  it('shows a member decided with its archive, without decision or tag controls', async () => {
    stubApi(routes())
    renderApp('/map/61?entry=m71')

    const panel = within(await screen.findByRole('complementary', { name: 'readme.txt' }))
    expect(await panel.findByText(/^Inside the archive/)).toHaveTextContent('Inside the archive eMule0.47c-Installer.zip')
    expect(within(panel.getByText(/^Inside the archive/)).getByRole('link')).toHaveAttribute('href', '/map/61?entry=61')
    const decision = within(panel.getByRole('region', { name: 'Decision' }))
    expect(decision.getByText('Decided with the archive: Keep')).toBeInTheDocument()
    expect(decision.queryByRole('button')).not.toBeInTheDocument()
    expect(panel.queryByRole('region', { name: 'Tags' })).not.toBeInTheDocument()
    expect(panel.getByRole('region', { name: 'Copies' })).toBeInTheDocument()
  })

  it('names the archive’s listing in its details', async () => {
    stubApi(routes())
    renderApp('/map/1?entry=61')

    const panel = within(await screen.findByRole('complementary', { name: 'eMule0.47c-Installer.zip' }))
    const archive = within(await panel.findByRole('region', { name: 'Archive' }))
    expect(archive.getByText('ZIP')).toBeInTheDocument()
    expect(archive.getByText('Read completely')).toBeInTheDocument()
    expect(archive.getByText('5 MiB')).toBeInTheDocument()
    expect(archive.getByRole('link', { name: 'Open as a folder' })).toHaveAttribute('href', '/map/61')
    expect(panel.getByRole('link', { name: 'Show in Map' })).toHaveAttribute('href', '/map/61?entry=61')
    expect(panel.getByRole('button', { name: 'Compare with…' })).toBeInTheDocument()
  })
})
