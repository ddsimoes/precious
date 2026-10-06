import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import type { EntryRow } from '@/api/entries'
import type { ReviewRow } from '@/api/opportunities'
import {
  card,
  copyOf,
  coverage,
  entryDetail,
  entryRow,
  folderRow,
  fotosSource,
  relationTo,
  reviewRow,
} from '@/test/fixtures'
import { jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'

const GiB = 1024 ** 3
const MiB = 1024 ** 2

const base = {
  'GET /api/session': () => jsonResponse(200, signedIn),
  'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource()] }),
  'GET /api/tags': () => jsonResponse(200, { tags: [] }),
}

function junk(id: string, name: string): EntryRow {
  return folderRow(id, name, {
    path: `Backup_PC_2004/${name}`,
    category: 'system_junk',
    family: 'disposable',
    triage: 'discard',
    total_bytes: Number(id) * MiB,
    total_files: 10,
  })
}

const thumbs = junk('21', 'Thumbs')
const recycler = junk('22', 'RECYCLER')
const temp = junk('23', 'Temp')

function commandBodies(requests: Request[], name: string) {
  return Promise.all(
    requests
      .filter((r) => r.method === 'POST' && new URL(r.url).pathname === `/api/commands/${name}`)
      .map((r) => r.clone().json() as Promise<unknown>),
  )
}

function listRequests(requests: Request[], list: string) {
  return requests
    .filter((r) => new URL(r.url).pathname === `/api/opportunities/${list}`)
    .map((r) => new URL(r.url).search)
}

// junkList serves the system junk list: its open rows are those not decided
// yet; decided=1 lists the others, with their decision.
function junkList(rows: EntryRow[]) {
  const decided = new Map<string, string>()
  return {
    decided,
    routes: {
      'GET /api/opportunities/system_junk': (request: Request) => {
        const showDecided = new URL(request.url).searchParams.get('decided') === '1'
        const items = rows
          .filter((row) => decided.has(row.id) === showDecided)
          .map((row) =>
            reviewRow(`r${row.id}`, { ...row, eff_decision: (decided.get(row.id) ?? 'undecided') as never }),
          )
        const open = rows.filter((row) => !decided.has(row.id))
        return jsonResponse(200, {
          card: card('system_junk', open.reduce((sum, row) => sum + row.total_bytes, 0), open.length),
          items,
          next_cursor: null,
        })
      },
      'POST /api/commands/set-decision': async (request: Request) => {
        const body = (await request.clone().json()) as { entry_id?: string; decision: string }
        if (body.entry_id !== undefined) {
          decided.set(body.entry_id, body.decision)
        }
        return jsonResponse(200, { applied: 1, skipped_count: 0, skipped: [] })
      },
    },
  }
}

function current() {
  return screen.getAllByRole('listitem').filter((item) => item.getAttribute('aria-current') === 'true')
}

describe('Opportunities', () => {
  it('lists the cards largest first, for all sources or one', async () => {
    const requests = stubApi({
      ...base,
      'GET /api/opportunities': (request) =>
        jsonResponse(200, {
          cards:
            new URL(request.url).searchParams.get('source') === 'fotos'
              ? [card('caches', 1 * GiB, 2)]
              : [card('leftovers', 0, 604), card('duplicates', 8 * GiB, 9), card('installers', 3 * GiB, 4)],
          coverage: coverage(),
          computed_at: '2026-10-06T10:00:00Z',
        }),
    })
    const { router } = renderApp('/opportunities')
    const user = userEvent.setup()

    const cards = within(await screen.findByRole('list', { name: 'Opportunity cards' }))
    expect(cards.getAllByRole('link').map((link) => [link.textContent, link.getAttribute('href')])).toEqual([
      ['Duplicate folders and files', '/opportunities/duplicates'],
      ['Old installers, disk images, and downloads', '/opportunities/installers'],
      ['Partial downloads, empty folders, and empty files', '/opportunities/leftovers'],
    ])
    expect(cards.getAllByRole('listitem')[0]).toHaveTextContent('Based on the same content')
    expect(cards.getAllByRole('listitem')[1]).toHaveTextContent('Based on the rules')
    // Empty files and folders hold no bytes: their count heads the card.
    expect(cards.getAllByRole('listitem')[2]).toHaveTextContent(/^Partial downloads, empty folders, and empty files604 items/)
    expect(cards.getAllByRole('listitem')[2]).not.toHaveTextContent(/0\sB/)
    expect(screen.getByText('80 GiB of 90 GiB checked (89%)')).toBeInTheDocument()
    expect(screen.getByText(/^Figures as of/)).toBeInTheDocument()

    await user.selectOptions(screen.getByRole('combobox', { name: 'Source' }), 'Fotos')
    expect(await screen.findByRole('link', { name: 'Caches, temporary files, and build output' })).toHaveAttribute(
      'href',
      '/opportunities/caches?source=fotos',
    )
    expect(router.state.location.search).toBe('?source=fotos')
    expect(requests.filter((r) => new URL(r.url).pathname === '/api/opportunities').map((r) => new URL(r.url).search)).toEqual(
      ['', '?source=fotos'],
    )
  })
})

describe('Review list', () => {
  it('shows each row with its size, dates, suggestion, and summary line', async () => {
    const office = folderRow('40', 'Microsoft Office', {
      path: 'Backup_PC_2004/C/Arquivos de programas/Microsoft Office',
      category: 'application_installation',
      family: 'programs',
      triage: 'review',
      total_bytes: 400 * MiB,
      total_files: 120,
      oldest: '2003-02-01T00:00:00Z',
      newest: '2004-11-30T00:00:00Z',
    })
    stubApi({
      ...base,
      'GET /api/opportunities/programs': () =>
        jsonResponse(200, {
          card: card('programs', 400 * MiB, 1),
          items: [
            reviewRow('r40', office, {
              summary: {
                category: 'application_installation',
                years: [2003, 2004],
                files: 120,
                bytes: 400 * MiB,
                signals: ['contains_user_material', 'camera_photo_present', 'database_present'],
              },
            }),
          ],
          next_cursor: null,
        }),
    })
    renderApp('/opportunities/programs')

    const rows = within(await screen.findByRole('list', { name: 'Rows of Installed programs and system copies' }))
    const row = rows.getAllByRole('listitem')[0]!
    expect(within(row).getByRole('link', { name: office.path })).toHaveAttribute('href', '/opportunities/programs?entry=40')
    expect(row).toHaveTextContent('400 MiB')
    expect(
      within(row).getByText(
        'Installed application · 2003–2004 · 120 files · 400 MiB · holds personal material and camera photos',
      ),
    ).toBeInTheDocument()
    expect(within(row).getByText('Fotos · Feb 1, 2003 – Nov 30, 2004 · Suggestion: Review')).toBeInTheDocument()
    expect(within(row).getByRole('group', { name: 'Decision for Microsoft Office' })).toBeInTheDocument()
    // The card's figures head the list.
    expect(screen.getByText(/1 item to review · Based on the rules/)).toHaveTextContent(
      '400 MiB 1 item to review · Based on the rules',
    )
    expect(screen.getByRole('button', { name: 'Select all rows' })).toBeInTheDocument()
  })

  it('decides and moves from the keyboard, but not while typing or in a dialog', async () => {
    const list = junkList([thumbs, recycler, temp])
    const requests = stubApi({
      ...base,
      ...list.routes,
      'GET /api/entries/22': () => jsonResponse(200, entryDetail(recycler)),
      'POST /api/commands/select-list': () =>
        jsonResponse(201, {
          selection_id: 's1',
          count: 3,
          bytes: 66 * MiB,
          kept: { count: 0, bytes: 0 },
          expires_at: '2026-10-06T11:00:00Z',
        }),
    })
    const { router } = renderApp('/opportunities/system_junk')
    const user = userEvent.setup()
    await screen.findByRole('link', { name: thumbs.path })

    // The first press of the next key selects the first row.
    await user.keyboard('j')
    expect(current()).toHaveLength(1)
    expect(current()[0]).toHaveTextContent(thumbs.path)

    // D discards it; it leaves the list, and the row that took its place is
    // selected.
    await user.keyboard('d')
    await waitFor(() => expect(screen.queryByRole('link', { name: thumbs.path })).not.toBeInTheDocument())
    expect(await commandBodies(requests, 'set-decision')).toEqual([{ entry_id: '21', decision: 'discard' }])
    await waitFor(() => expect(current()[0]).toHaveTextContent(recycler.path))
    expect(current()[0]).toHaveFocus()
    await user.keyboard('{ArrowDown}')
    expect(current()[0]).toHaveTextContent(temp.path)
    await user.keyboard('{ArrowUp}')
    expect(current()[0]).toHaveTextContent(recycler.path)

    // Enter opens the selected row's details.
    await user.keyboard('{Enter}')
    expect(router.state.location.search).toBe('?entry=22')
    expect(await screen.findByRole('complementary', { name: 'RECYCLER' })).toBeInTheDocument()

    // Keys typed into a field are the field's.
    screen.getByRole('combobox', { name: 'Source' }).focus()
    await user.keyboard('k')
    // And keys inside a dialog are the dialog's.
    await user.click(screen.getByRole('button', { name: 'Select all rows' }))
    const dialog = await screen.findByRole('alertdialog', { name: 'Select every row of this list?' })
    within(dialog).getByRole('button', { name: 'Cancel' }).focus()
    await user.keyboard('d')
    expect(await commandBodies(requests, 'set-decision')).toHaveLength(1)
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))

    // L sets later on the selected row.
    await user.click(screen.getByText(recycler.path, { selector: 'a' }).closest('li')!)
    await user.keyboard('l')
    await waitFor(async () =>
      expect(await commandBodies(requests, 'set-decision')).toEqual([
        { entry_id: '21', decision: 'discard' },
        { entry_id: '22', decision: 'later' },
      ]),
    )
  })

  it('decides consecutive rows from the keyboard without moving', async () => {
    const list = junkList([thumbs, recycler, temp])
    const requests = stubApi({ ...base, ...list.routes })
    renderApp('/opportunities/system_junk')
    const user = userEvent.setup()
    await screen.findByRole('link', { name: thumbs.path })

    await user.keyboard('j')
    await user.keyboard('d')
    await waitFor(() => expect(current()[0]).toHaveTextContent(recycler.path))
    await user.keyboard('k')
    await waitFor(() => expect(current()[0]).toHaveTextContent(temp.path))
    expect(await commandBodies(requests, 'set-decision')).toEqual([
      { entry_id: '21', decision: 'discard' },
      { entry_id: '22', decision: 'keep' },
    ])

    // Deciding the last row leaves nothing selected.
    await user.keyboard('l')
    expect(await screen.findByText('Nothing left to review in this list.')).toBeInTheDocument()
    expect(screen.queryAllByRole('listitem').filter((item) => item.getAttribute('aria-current') === 'true')).toEqual([])
  })

  it('loads the next page when the next key passes the last loaded row', async () => {
    const pages: Record<string, EntryRow[]> = { '': [thumbs, recycler], p2: [temp] }
    const requests = stubApi({
      ...base,
      'GET /api/opportunities/system_junk': (request) => {
        const after = new URL(request.url).searchParams.get('cursor') ?? ''
        return jsonResponse(200, {
          card: card('system_junk', 66 * MiB, 3),
          items: (pages[after] ?? []).map((row) => reviewRow(`r${row.id}`, row)),
          next_cursor: after === '' ? 'p2' : null,
        })
      },
    })
    renderApp('/opportunities/system_junk')
    const user = userEvent.setup()
    await screen.findByRole('link', { name: recycler.path })
    expect(screen.queryByRole('link', { name: temp.path })).not.toBeInTheDocument()

    await user.keyboard('jj')
    expect(current()[0]).toHaveTextContent(recycler.path)
    await user.keyboard('j')
    await waitFor(() => expect(current()[0]).toHaveTextContent(temp.path))
    expect(current()[0]).toHaveFocus()
    expect(listRequests(requests, 'system_junk')).toEqual(['?decided=0', '?decided=0&cursor=p2'])
  })

  it('shows decided rows on request', async () => {
    const list = junkList([thumbs, recycler])
    list.decided.set('21', 'discard')
    const requests = stubApi({ ...base, ...list.routes })
    const { router } = renderApp('/opportunities/system_junk')
    const user = userEvent.setup()

    await screen.findByRole('link', { name: recycler.path })
    expect(screen.queryByRole('link', { name: thumbs.path })).not.toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Decided rows' })).not.toBeInTheDocument()

    await user.click(screen.getByRole('checkbox', { name: 'Show decided rows' }))
    const decided = within(await screen.findByRole('region', { name: 'Decided rows' }))
    const row = (await decided.findByRole('link', { name: thumbs.path })).closest('li')!
    expect(row).toHaveTextContent('Decision: Discard')
    expect(router.state.location.search).toBe('?decided=1')
    expect(listRequests(requests, 'system_junk')).toEqual(['?decided=0', '?decided=1'])

    await user.click(screen.getByRole('checkbox', { name: 'Show decided rows' }))
    expect(screen.queryByRole('region', { name: 'Decided rows' })).not.toBeInTheDocument()
    expect(router.state.location.search).toBe('')
  })

  it('selects every open row with the usual confirmation and report', async () => {
    const list = junkList([thumbs, recycler, temp])
    const requests = stubApi({
      ...base,
      ...list.routes,
      'POST /api/commands/select-list': () =>
        jsonResponse(201, {
          selection_id: 's1',
          count: 3,
          bytes: 66 * MiB,
          kept: { count: 1, bytes: 22 * MiB },
          expires_at: '2026-10-06T11:00:00Z',
        }),
      'POST /api/commands/set-decision': () =>
        jsonResponse(200, {
          applied: 2,
          skipped_count: 1,
          skipped: [{ entry_id: '22', path: recycler.path, path_b64: recycler.path_b64 }],
        }),
    })
    renderApp('/opportunities/system_junk?source=fotos')
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: 'Select all rows' }))
    const dialog = within(await screen.findByRole('alertdialog', { name: 'Select every row of this list?' }))
    expect(dialog.getByText('3 items')).toBeInTheDocument()
    expect(dialog.getByText('66 MiB in total')).toBeInTheDocument()
    expect(dialog.getByText(/1 of them \(22 MiB\) is kept/)).toBeInTheDocument()
    expect(await commandBodies(requests, 'select-list')).toEqual([{ list: 'system_junk', source_id: 'fotos' }])
    await user.click(dialog.getByRole('button', { name: 'Select all' }))

    const bulk = within(screen.getByRole('region', { name: 'Change the selected items' }))
    expect(bulk.getByText('All 3 items of this list are selected (66 MiB).')).toBeInTheDocument()
    await user.click(within(bulk.getByRole('group', { name: 'Set decision' })).getByRole('button', { name: 'Discard' }))

    const report = within(await screen.findByRole('region', { name: 'Result of the last change' }))
    expect(report.getByText('Decision set on 2 items.')).toBeInTheDocument()
    expect(report.getByText(recycler.path)).toBeInTheDocument()
    expect(await commandBodies(requests, 'set-decision')).toEqual([{ selection_id: 's1', decision: 'discard' }])
  })

  it('expands duplicates into their copies, decided one by one, with no select all', async () => {
    const setup = entryRow({ id: '31', name: 'Setup.exe', path: 'Downloads/Setup.exe', copies: 3 })
    const setup1 = entryRow({ id: '32', name: 'Setup(1).exe', path: 'Downloads/Setup(1).exe', copies: 3 })
    const member = entryRow({
      id: 'm45',
      name: 'Setup.exe',
      path: 'Downloads/old.zip!Setup.exe',
      archive_id: '50',
      eff_decision: 'keep',
      decision: null,
    })
    const emule = folderRow('60', 'emule-0.47c', { path: 'Downloads/emule-0.47c' })
    const installer = entryRow({ id: '61', name: 'eMule0.47c-Installer.zip', path: 'Downloads/eMule0.47c-Installer.zip' })
    const rows: ReviewRow[] = [
      reviewRow('d1', installer, {
        bytes: 40 * MiB,
        relation: relationTo(emule),
        summary: { category: null, years: [2006, 2006], files: 120, bytes: 40 * MiB, signals: [] },
      }),
      reviewRow('d2', null, {
        bytes: 6 * MiB,
        copies: [copyOf(setup), copyOf(setup1), copyOf(member)],
        summary: { category: 'installer_download', years: [2004, 2004], files: 3, bytes: 9 * MiB, signals: [] },
      }),
    ]
    const requests = stubApi({
      ...base,
      'GET /api/opportunities/duplicates': () =>
        jsonResponse(200, { card: card('duplicates', 46 * MiB, 2), items: rows, next_cursor: null }),
      'POST /api/commands/set-decision': () => jsonResponse(200, { applied: 1, skipped_count: 0, skipped: [] }),
    })
    renderApp('/opportunities/duplicates')
    const user = userEvent.setup()

    const list = within(await screen.findByRole('list', { name: 'Rows of Duplicate folders and files' }))
    expect(screen.queryByRole('button', { name: 'Select all rows' })).not.toBeInTheDocument()
    expect(screen.getByText(/Copies are decided one at a time/)).toBeInTheDocument()

    const relationRow = list.getByText(`${installer.path} · ${emule.path}`).closest('li')!
    expect(relationRow).toHaveTextContent('40 MiB in extra copies')
    expect(relationRow).toHaveTextContent('Same content · 2006 · 120 files · 40 MiB')
    expect(within(relationRow).getByRole('link', { name: 'Compare' })).toHaveAttribute(
      'href',
      '/compare?left=61&right=60',
    )

    const groupRow = list.getByText('3 copies of Setup.exe').closest('li')!
    expect(groupRow).toHaveTextContent('6 MiB in extra copies')
    // Its title counts the copies: its summary does not count files.
    expect(within(groupRow).getByText('Installers and disk images · 2004 · 9 MiB')).toBeInTheDocument()
    const show = within(groupRow).getByRole('button', { name: 'Show copies' })
    expect(show).toHaveAttribute('aria-expanded', 'false')
    await user.click(show)
    const copies = within(within(groupRow).getByRole('list', { name: 'Copies' }))
    const items = copies.getAllByRole('listitem')
    expect(items.map((item) => within(item).getByRole('link').textContent)).toEqual([
      'Downloads/Setup.exe',
      'Downloads/Setup(1).exe',
      'Downloads/old.zip!Setup.exe',
    ])
    expect(within(items[2]!).queryByRole('group')).not.toBeInTheDocument()
    expect(items[2]).toHaveTextContent('Decided with its archive')
    await user.click(
      within(within(items[1]!).getByRole('group', { name: 'Decision for Downloads/Setup(1).exe' })).getByRole(
        'button',
        { name: 'Discard' },
      ),
    )
    await waitFor(async () =>
      expect(await commandBodies(requests, 'set-decision')).toEqual([{ entry_id: '32', decision: 'discard' }]),
    )

    // From the keyboard: a duplicates row has no decision of its own; Enter
    // shows its copies, and the keys decide the selected copy.
    await user.click(within(groupRow).getByRole('button', { name: 'Hide copies' }))
    await user.keyboard('{ArrowDown}{ArrowDown}')
    expect(current()[0]).toBe(groupRow)
    await user.keyboard('d')
    await user.keyboard('{Enter}')
    expect(within(groupRow).getByRole('button', { name: 'Hide copies' })).toHaveAttribute('aria-expanded', 'true')
    await user.keyboard('jk')
    await waitFor(async () =>
      expect(await commandBodies(requests, 'set-decision')).toEqual([
        { entry_id: '32', decision: 'discard' },
        { entry_id: '31', decision: 'keep' },
      ]),
    )
    // A member is decided with its archive: the keys skip it.
    await user.keyboard('jjd')
    expect(current()[0]).toHaveTextContent('Downloads/old.zip!Setup.exe')
    expect(await commandBodies(requests, 'set-decision')).toHaveLength(2)
  })

  it('names the folder an archive was unpacked in, with a Compare of the two', async () => {
    const zip = entryRow({ id: '61', name: 'eMule0.47c.zip', path: 'Downloads/eMule0.47c.zip', archive_state: 'complete' })
    const emule = folderRow('60', 'emule-0.47c', { path: 'Downloads/emule-0.47c' })
    stubApi({
      ...base,
      'GET /api/opportunities/unpacked_archives': () =>
        jsonResponse(200, {
          card: card('unpacked_archives', zip.total_bytes, 1),
          items: [reviewRow('u61', zip, { relation: relationTo(emule) })],
          next_cursor: null,
        }),
    })
    renderApp('/opportunities/unpacked_archives')

    const rows = within(await screen.findByRole('list', { name: 'Rows of Archives already unpacked' }))
    const row = rows.getAllByRole('listitem')[0]!
    expect(row).toHaveTextContent(`Unpacked in ${emule.path}`)
    expect(within(row).getByRole('link', { name: emule.path })).toHaveAttribute(
      'href',
      '/opportunities/unpacked_archives?entry=60',
    )
    expect(within(row).getByRole('link', { name: 'Compare' })).toHaveAttribute('href', '/compare?left=61&right=60')
    expect(within(row).getByRole('group', { name: `Decision for ${zip.name}` })).toBeInTheDocument()
  })

  it('answers an unknown list as not found', async () => {
    stubApi(base)
    renderApp('/opportunities/colors')
    expect(await screen.findByRole('heading', { name: 'Page not found' })).toBeInTheDocument()
  })
})
