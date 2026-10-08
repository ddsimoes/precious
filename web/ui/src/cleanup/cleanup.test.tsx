import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import { checkQueryKey, type Check, type CheckFile } from '@/api/cleanup'
import type { Action } from '@/api/organize'
import type { Source } from '@/api/sources'
import { MockEventSource } from '@/test/eventSource'
import {
  action,
  actionItem,
  card,
  check,
  checkFile,
  entryCounts,
  entryRow,
  folderRow,
  fotosSource,
  quarantined,
} from '@/test/fixtures'
import { errorResponse, jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'

type Route = (request: Request) => Response | Promise<Response>

const MiB = 1024 ** 2

const writable = fotosSource({ writes: { enabled: true, unavailable: null } })

const emptyQuarantine = { items: [], next_cursor: null, total: { files: 0, bytes: 0 } }

function routes(sources: Source[], extra: Record<string, Route> = {}): Record<string, Route> {
  return {
    'GET /api/session': () => jsonResponse(200, signedIn),
    'GET /api/sources': () => jsonResponse(200, { sources }),
    'GET /api/quarantine': () => jsonResponse(200, emptyQuarantine),
    ...extra,
  }
}

function commands(requests: Request[], name?: string) {
  return requests.filter(
    (r) =>
      r.method === 'POST' &&
      new URL(r.url).pathname.startsWith('/api/commands/') &&
      (name === undefined || new URL(r.url).pathname === `/api/commands/${name}`),
  )
}

function commandNames(requests: Request[]) {
  return commands(requests).map((r) => new URL(r.url).pathname.slice('/api/commands/'.length))
}

function bodies(requests: Request[], name: string) {
  return Promise.all(commands(requests, name).map((r) => r.clone().json() as Promise<unknown>))
}

// expectSigned checks that every command carried the session's CSRF token
// and its own idempotency key.
function expectSigned(requests: Request[]) {
  const sent = commands(requests)
  expect(sent.length).toBeGreaterThan(0)
  for (const request of sent) {
    expect(request.headers.get('X-CSRF-Token')).toBe('session-token')
    expect(request.headers.get('Idempotency-Key')).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
  }
  expect(new Set(sent.map((r) => r.headers.get('Idempotency-Key'))).size).toBe(sent.length)
}

function lines(list: HTMLElement) {
  return within(list)
    .getAllByRole('listitem')
    .map((item) => item.textContent.replace(/\s+/g, ' '))
}

// The cleanup plan of fotos: setup.exe planned, and the folder Fotos/2004
// blocked by two kept files inside it.
const cleanupPlan = action(
  {
    id: '50',
    kind: 'cleanup',
    ground: 'discard',
    bulk: true,
    files: 1,
    bytes: 3 * MiB,
    expires_at: '2026-10-09T10:00:00Z',
    entries: entryCounts({ planned: 1, blocked: 1 }),
  },
  { planned: 5, blocked: 1 },
)
const summary = { with_copy_bytes: 2 * MiB, no_copy_bytes: MiB, unchecked_bytes: 0, personal_items: 1 }
const planItems = [
  actionItem('301', 'Downloads/setup.exe', '.precious-quarantine/50/1/setup.exe', { bytes: 3 * MiB }),
  actionItem('302', 'Fotos/2004', '.precious-quarantine/50/2/2004', {
    state: 'blocked',
    reason: 'holds_kept',
    kept_count: 2,
    bytes: 10 * MiB,
    files: 2,
  }),
]
const keptA = entryRow({ id: '401', name: 'a.jpg', path: 'Fotos/2004/a.jpg', eff_decision: 'keep' })
const keptB = entryRow({ id: '402', name: 'b.jpg', path: 'Fotos/2004/b.jpg', eff_decision: 'keep' })

function planRoutes(planned: Action, extra: Record<string, Route> = {}): Record<string, Route> {
  return {
    'POST /api/commands/plan-cleanup': () =>
      jsonResponse(201, { action: planned, items: planItems, next_cursor: null, summary }),
    // The preview lists one step per entry.
    'GET /api/history/50/items': (request) =>
      new URL(request.url).searchParams.getAll('op').join() === 'rename'
        ? jsonResponse(200, { items: planItems, next_cursor: null })
        : jsonResponse(400, { error: { code: 'invalid_request', message: 'op' } }),
    'GET /api/history/50/items/302/kept': (request) =>
      new URL(request.url).searchParams.get('cursor') === 'k2'
        ? jsonResponse(200, { count: 2, items: [keptB], next_cursor: null })
        : jsonResponse(200, { count: 2, items: [keptA], next_cursor: 'k2' }),
    'POST /api/commands/run-action': () =>
      jsonResponse(202, { action: { ...planned, state: 'queued' }, job_id: '8', state: 'queued' }),
    'GET /api/history/50': () =>
      jsonResponse(200, { ...planned, state: 'done', entries: entryCounts({ done: 1, blocked: 1 }) }),
    ...extra,
  }
}

describe('Cleanup plans', () => {
  it('R4.1: drafting from a source shows the blocked item with its kept entries, and runs only on approve', async () => {
    const requests = stubApi(routes([writable], planRoutes(cleanupPlan)))
    renderApp('/cleanup?source=fotos')
    expect(await screen.findByRole('link', { name: 'Cleanup', current: 'page' })).toHaveAttribute('href', '/cleanup')

    await userEvent.click(await screen.findByRole('button', { name: 'Draft a cleanup plan' }))
    const preview = within(await screen.findByRole('alertdialog', { name: 'Move discarded items to quarantine' }))
    expect(lines(preview.getByRole('list', { name: 'Items' }))).toEqual([
      '1 item will move to quarantine',
      '1 item is blocked: it holds kept items',
      'In total: 1 file, 3 MiB',
    ])
    const known = within(preview.getByRole('region', { name: 'What is known of copies' }))
    expect(known.getByText('2 MiB has a copy elsewhere')).toBeInTheDocument()
    expect(known.getByText('1 MiB has no known copy')).toBeInTheDocument()
    expect(known.getByText('1 item holds personal material')).toBeInTheDocument()
    expect(preview.getByText(/can be run for 24 hours/)).toBeInTheDocument()

    expect(lines(await preview.findByRole('list', { name: 'Planned' }))).toEqual(['Downloads/setup.exe3 MiB · 1 file'])
    const blocked = within(preview.getByRole('list', { name: 'Blocked by kept items' }))
    expect(blocked.getByText('Fotos/2004')).toBeInTheDocument()
    expect(blocked.getByText('Holds kept items')).toBeInTheDocument()
    await userEvent.click(blocked.getByRole('button', { name: 'Show the 2 kept items inside' }))
    expect(lines(await blocked.findByRole('list', { name: 'Kept items inside' }))).toEqual(['Fotos/2004/a.jpg'])
    await userEvent.click(blocked.getByRole('button', { name: 'Load more' }))
    await waitFor(() =>
      expect(lines(blocked.getByRole('list', { name: 'Kept items inside' }))).toEqual([
        'Fotos/2004/a.jpg',
        'Fotos/2004/b.jpg',
      ]),
    )

    expect(preview.getByRole('link', { name: 'Export CSV' })).toHaveAttribute('href', '/api/history/50/export.csv')

    // Drafting changed nothing: only the plan was asked for.
    expect(commandNames(requests)).toEqual(['plan-cleanup'])
    expect(await bodies(requests, 'plan-cleanup')).toEqual([{ source_id: 'fotos' }])

    await userEvent.click(preview.getByRole('button', { name: 'Approve and run' }))
    expect(await screen.findByText('Moved to quarantine.')).toBeInTheDocument()
    expect(screen.getByText('Not everything was changed: see History.')).toBeInTheDocument()
    expect(commandNames(requests)).toEqual(['plan-cleanup', 'run-action'])
    expect(await bodies(requests, 'run-action')).toEqual([{ action_id: '50' }])
    expectSigned(requests)
  })

  it('closing the preview runs nothing', async () => {
    const requests = stubApi(routes([writable], planRoutes(cleanupPlan)))
    renderApp('/cleanup?source=fotos')
    await userEvent.click(await screen.findByRole('button', { name: 'Draft a cleanup plan' }))
    const preview = within(await screen.findByRole('alertdialog', { name: 'Move discarded items to quarantine' }))
    await userEvent.click(preview.getByRole('button', { name: 'Close' }))
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    expect(commandNames(requests)).toEqual(['plan-cleanup'])
  })

  it('explains a folder of the owner’s own that takes the quarantine’s name, and offers no plan', async () => {
    stubApi(routes([fotosSource({ writes: { enabled: true, unavailable: null }, quarantine: { files: 0, bytes: 0, name_taken: true } })]))
    renderApp('/cleanup?source=fotos')
    expect(await screen.findByText(/has a folder named “.precious-quarantine” at its top/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Draft a cleanup plan' })).not.toBeInTheDocument()
  })

  it('offers no plan while changes are not allowed, and says where to allow them', async () => {
    stubApi(routes([fotosSource()]))
    renderApp('/cleanup?source=fotos')
    expect(await screen.findByRole('button', { name: 'Draft a cleanup plan' })).toBeDisabled()
    expect(screen.getByText(/to draft a plan\.$/)).toHaveTextContent(
      'Allow changes on this source on the Sources screen to draft a plan.',
    )
    expect(within(screen.getByRole('main')).getByRole('link', { name: 'Sources' })).toHaveAttribute('href', '/sources')
  })

  it('drafts a plan from a review list for the chosen source only', async () => {
    const listPlan = action({
      ...cleanupPlan,
      list: 'system_junk',
      entries: entryCounts({ planned: 1 }),
    })
    const requests = stubApi(
      routes([writable], {
        ...planRoutes(listPlan),
        'GET /api/opportunities/system_junk': () =>
          jsonResponse(200, { card: card('system_junk', 3 * MiB, 1), items: [], next_cursor: null }),
      }),
    )
    const { router } = renderApp('/opportunities/system_junk?source=')
    const draft = await screen.findByRole('button', { name: 'Draft a cleanup plan from this list' })
    expect(draft).toBeDisabled()
    expect(screen.getByText('Choose one source above to draft a plan from this list.')).toBeInTheDocument()

    await screen.findByRole('option', { name: 'Fotos' })
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Source' }), 'Fotos')
    await waitFor(() => expect(router.state.location.search).toBe('?source=fotos'))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Draft a cleanup plan from this list' })).toBeEnabled())
    await userEvent.click(screen.getByRole('button', { name: 'Draft a cleanup plan from this list' }))
    expect(
      await screen.findByRole('alertdialog', { name: 'Move the discarded items of “System junk” to quarantine' }),
    ).toBeInTheDocument()
    expect(await bodies(requests, 'plan-cleanup')).toEqual([{ source_id: 'fotos', list: 'system_junk' }])
    expectSigned(requests)
  })
})

describe('The quarantine', () => {
  const photos = quarantined('61', 'Fotos/2004', { bytes: 10 * MiB, files: 2 })
  const unknown = quarantined('62', null)
  const documentos = folderRow('40', 'Documentos')

  it('R4.3: lists the items newest first, and a restore whose place is taken asks for a folder', async () => {
    const conflicted = action({
      id: '80',
      kind: 'restore',
      bulk: true,
      entries: entryCounts({ conflict: 1 }),
    })
    const conflictItem = actionItem('501', '.precious-quarantine/50/2/2004', 'Fotos/2004', {
      state: 'conflict',
      reason: 'name_taken',
    })
    const redirected = action({
      id: '81',
      kind: 'restore',
      bulk: true,
      destination: documentos,
      entries: entryCounts({ planned: 1 }),
    })
    const redirectedItem = actionItem('502', '.precious-quarantine/50/2/2004', 'Documentos/2004')
    const requests = stubApi(
      routes([writable], {
        'GET /api/quarantine': () =>
          jsonResponse(200, { items: [photos, unknown], next_cursor: null, total: { files: 3, bytes: 13 * MiB } }),
        'POST /api/commands/plan-restore': async (request) => {
          const body = (await request.clone().json()) as { destination_id?: string }
          return body.destination_id === undefined
            ? jsonResponse(201, { action: conflicted, items: [conflictItem], next_cursor: null })
            : jsonResponse(201, { action: redirected, items: [redirectedItem], next_cursor: null })
        },
        'GET /api/history/80/items': () => jsonResponse(200, { items: [conflictItem], next_cursor: null }),
        'GET /api/history/81/items': () => jsonResponse(200, { items: [redirectedItem], next_cursor: null }),
        'GET /api/entries/1/children': () => jsonResponse(200, { items: [documentos], next_cursor: null }),
        'GET /api/entries/40/children': () => jsonResponse(200, { items: [], next_cursor: null }),
        'POST /api/commands/run-action': () =>
          jsonResponse(202, { action: { ...redirected, state: 'queued' }, job_id: '8', state: 'queued' }),
        'GET /api/history/81': () => jsonResponse(200, { ...redirected, state: 'done' }),
      }),
    )
    renderApp('/cleanup?source=fotos')

    const list = await screen.findByRole('list', { name: 'Quarantine of Fotos' })
    expect(lines(list)).toEqual([
      'Fotos/200410 MiB · 2 files · quarantined Oct 8, 2026, 9:00 AM' + 'Not checked',
      'found-62origin unknown · 3 MiB · 1 file · date unknown' + 'Not checked',
    ])
    expect(screen.getByText('In quarantine: 3 files, 13 MiB')).toBeInTheDocument()

    await userEvent.click(within(list).getByRole('checkbox', { name: 'Fotos/2004' }))
    await userEvent.click(screen.getByRole('button', { name: 'Restore' }))
    const preview = within(await screen.findByRole('alertdialog', { name: 'Restore from quarantine' }))
    expect(lines(preview.getByRole('list', { name: 'Items' }))).toEqual([
      '0 items will be restored',
      '1 item cannot go back: its place is taken or gone',
    ])
    expect(lines(await preview.findByRole('list', { name: 'Place taken or gone' }))).toEqual([
      'Fotos/20041 KiB · 1 file' + 'Name taken',
    ])
    expect(preview.getByRole('button', { name: 'Restore' })).toBeDisabled()

    await userEvent.click(preview.getByRole('button', { name: 'Choose a folder for them…' }))
    const chooser = within(
      await screen.findByRole('dialog', { name: 'Choose a folder for the items that cannot go back' }),
    )
    await userEvent.click(await chooser.findByRole('button', { name: 'Documentos' }))
    await chooser.findByText('This folder has no subfolders.')
    await userEvent.click(chooser.getByRole('button', { name: 'Move here' }))

    const next = within(await screen.findByRole('alertdialog', { name: 'Restore into “Documentos”' }))
    expect(lines(await next.findByRole('list', { name: 'Planned' }))).toEqual(['Documentos/20041 KiB · 1 file'])
    await userEvent.click(next.getByRole('button', { name: 'Restore' }))
    expect(await screen.findByText('Restored.')).toBeInTheDocument()

    expect(commandNames(requests)).toEqual(['plan-restore', 'plan-restore', 'run-action'])
    expect(await bodies(requests, 'plan-restore')).toEqual([
      { entry_ids: ['61'] },
      { entry_ids: ['61'], destination_id: '40' },
    ])
    expect(await bodies(requests, 'run-action')).toEqual([{ action_id: '81' }])
    expectSigned(requests)
  })

  it('starts a check of the selected items and opens its report', async () => {
    const requests = stubApi(
      routes([writable], {
        'GET /api/quarantine': () =>
          jsonResponse(200, { items: [photos, unknown], next_cursor: null, total: { files: 3, bytes: 13 * MiB } }),
        'POST /api/commands/check-purge': () => jsonResponse(202, { check_id: '9', job_id: '31' }),
        'GET /api/checks/9': () => jsonResponse(200, check({ state: 'running', finished_at: null })),
      }),
    )
    const { router } = renderApp('/cleanup?source=fotos')
    await screen.findByRole('list', { name: 'Quarantine of Fotos' })
    await userEvent.click(screen.getByRole('button', { name: 'Select all shown' }))
    expect(screen.getByText('2 selected')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Check before deleting' }))

    await waitFor(() => expect(router.state.location.pathname).toBe('/cleanup/checks/9'))
    expect(await screen.findByText('Checking: reading every file and looking for its copies…')).toBeInTheDocument()
    expect(await bodies(requests, 'check-purge')).toEqual([{ entry_ids: ['61', '62'] }])
    expectSigned(requests)

    // The check's job events bring its progress, and refetch the check.
    const before = requests.filter((r) => new URL(r.url).pathname === '/api/checks/9').length
    const stream = MockEventSource.latest()
    act(() => {
      stream.open()
      stream.emit(
        'job',
        {
          job_id: '31',
          kind: 'purge_check',
          source_id: 'fotos',
          state: 'running',
          cancel_requested: false,
          progress: { files: 1, bytes: MiB, of_files: 3, of_bytes: 4 * MiB },
          attempts: 1,
        },
        '5',
      )
    })
    expect(await screen.findByText('1 of 3 files read, 1 MiB of 4 MiB')).toBeInTheDocument()
    await waitFor(() =>
      expect(requests.filter((r) => new URL(r.url).pathname === '/api/checks/9').length).toBeGreaterThan(before),
    )
  })
})

describe('The check report', () => {
  const photo = checkFile('101', '.precious-quarantine/50/2/2004/a.jpg', {
    class: 'possibly_valuable',
    size: 2 * MiB,
  })
  const junk = checkFile('102', '.precious-quarantine/50/2/2004/Thumbs.db', { class: 'likely_junk' })
  const twin = checkFile('103', '.precious-quarantine/50/2/2004/c.jpg', {
    verdict: 'safe',
    class: null,
    size: 1024,
    copy: { source_id: 'fotos', path: 'Backup/c.jpg', path_b64: btoa('Backup/c.jpg'), hard_link: false },
  })

  function reportRoutes(current: () => Check, files: () => CheckFile[], extra: Record<string, Route> = {}) {
    return routes([writable], {
      'GET /api/checks/9': () => jsonResponse(200, current()),
      'GET /api/checks/9/files': () => jsonResponse(200, { items: files(), next_cursor: null }),
      ...extra,
    })
  }

  it('R4.7: gates the purge until the junk group and each valuable file are confirmed', async () => {
    let current = check()
    let files = [photo, junk, twin]
    const requests = stubApi(
      reportRoutes(
        () => current,
        () => files,
        {
          'POST /api/commands/confirm-purge': async (request) => {
            const body = (await request.clone().json()) as { group?: string; file_ids?: string[] }
            if (body.group === 'likely_junk') {
              current = { ...current, junk_confirmed: true, unconfirmed: { files: 1, bytes: 2 * MiB } }
            } else {
              current = {
                ...current,
                confirmed: { files: 1, bytes: 2 * MiB },
                unconfirmed: { files: 0, bytes: 0 },
                allowed: true,
              }
              files = [{ ...photo, confirmed: true }, junk, twin]
            }
            return jsonResponse(200, { check: current })
          },
        },
      ),
    )
    renderApp('/cleanup/checks/9')

    expect(lines(await screen.findByRole('list', { name: 'What the check found' }))).toEqual([
      'Has a verified copy1 file · 1 KiB',
      'Copy only on a disk not connected0 files · 0 B',
      'No copy found2 files · 3 MiB',
      'Could not be read0 files · 0 B',
      'Archive not opened0 files · 0 B',
      'Nothing to copy (folders, links, empty files)1 file · 0 B',
    ])
    expect(lines(screen.getByRole('list', { name: 'Files with no copy, by what they look like' }))).toEqual([
      'Possibly valuable1 file · 2 MiB',
      'Uncertain0 files · 0 B',
      'Likely junk1 file · 1 MiB',
    ])
    const fileList = within(await screen.findByRole('list', { name: 'Files' }))
    expect(fileList.getByText('Copy: Backup/c.jpg on Fotos')).toBeInTheDocument()
    expect(fileList.getByText('No copy found · Likely junk · Waits for the likely junk confirmation')).toBeInTheDocument()
    const purge = screen.getByRole('button', { name: 'Delete for good…' })
    expect(purge).toBeDisabled()
    expect(screen.getByText('Still to confirm: 2 files, 3 MiB')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: /^Confirm all likely junk \(1 file, 1\sMiB\)$/ }))
    expect(await screen.findByText('The likely junk is confirmed.')).toBeInTheDocument()
    expect(screen.getByText('Still to confirm: 1 file, 2 MiB')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Delete for good…' })).toBeDisabled()

    // One click per file: only the photo needs its own.
    const photoRow = within(fileList.getByText('.precious-quarantine/50/2/2004/a.jpg').closest('li')!)
    expect(fileList.getAllByRole('button', { name: 'Confirm' })).toHaveLength(1)
    expect(photoRow.getByRole('button', { name: 'Move out…' })).toBeInTheDocument()
    expect(photoRow.getByRole('button', { name: 'Restore its item' })).toBeInTheDocument()
    await userEvent.click(photoRow.getByRole('button', { name: 'Confirm' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Delete for good…' })).toBeEnabled())
    await waitFor(() => expect(fileList.queryByRole('button', { name: 'Confirm' })).not.toBeInTheDocument())

    expect(await bodies(requests, 'confirm-purge')).toEqual([
      { check_id: '9', group: 'likely_junk' },
      { check_id: '9', file_ids: ['101'] },
    ])
    expect(commandNames(requests)).toEqual(['confirm-purge', 'confirm-purge'])
    expectSigned(requests)
  })

  it('moves a file out of the quarantine with an individual move', async () => {
    const documentos = folderRow('40', 'Documentos')
    const moved = action({ id: '85', destination: documentos }, { planned: 1 })
    const requests = stubApi(
      reportRoutes(
        () => check(),
        () => [photo],
        {
          'GET /api/entries/1/children': () => jsonResponse(200, { items: [documentos], next_cursor: null }),
          'GET /api/entries/40/children': () => jsonResponse(200, { items: [], next_cursor: null }),
          'POST /api/commands/plan-move': () => jsonResponse(201, { action: moved, items: [], next_cursor: null }),
          'POST /api/commands/run-action': () =>
            jsonResponse(202, { action: { ...moved, state: 'queued' }, job_id: '8', state: 'queued' }),
          'GET /api/history/85': () => jsonResponse(200, { ...moved, state: 'done' }),
        },
      ),
    )
    renderApp('/cleanup/checks/9')
    await userEvent.click(await screen.findByRole('button', { name: 'Move out…' }))
    const chooser = within(await screen.findByRole('dialog', { name: 'Move “a.jpg” out of quarantine to…' }))
    await userEvent.click(await chooser.findByRole('button', { name: 'Documentos' }))
    await chooser.findByText('This folder has no subfolders.')
    await userEvent.click(chooser.getByRole('button', { name: 'Move here' }))
    expect(await screen.findByText('Moved.')).toBeInTheDocument()
    expect(await bodies(requests, 'plan-move')).toEqual([{ entry_id: 'e101', destination_id: '40' }])
  })

  it('names the file each row’s buttons act on', async () => {
    stubApi(reportRoutes(() => check(), () => [photo, junk]))
    renderApp('/cleanup/checks/9')
    const actions = within(await screen.findByRole('group', { name: /a\.jpg/ }))
    expect(actions.getByRole('button', { name: 'Confirm' })).toBeInTheDocument()
    expect(actions.getByRole('button', { name: 'Move out…' })).toBeInTheDocument()
    expect(actions.getByRole('button', { name: 'Restore its item' })).toBeInTheDocument()
  })

  it('offers no Confirm for a file whose item could not be read, which stays in quarantine', async () => {
    const lost = checkFile('104', '.precious-quarantine/50/3/broken/b.raw', {
      item: quarantined('64', 'Old/broken').entry,
      verdict: 'unreadable',
      class: null,
      item_readable: false,
    })
    const damaged = checkFile('105', '.precious-quarantine/50/2/2004/d.raw', { verdict: 'unreadable', class: null })
    stubApi(reportRoutes(() => check(), () => [lost, damaged]))
    renderApp('/cleanup/checks/9')
    const fileList = within(await screen.findByRole('list', { name: 'Files' }))

    const lostRow = within(fileList.getByText('.precious-quarantine/50/3/broken/b.raw').closest('li')!)
    expect(
      lostRow.getByText('Stays in quarantine: its item could not be read, so it is never deleted.'),
    ).toBeInTheDocument()
    expect(lostRow.queryByRole('button', { name: 'Confirm' })).not.toBeInTheDocument()
    expect(lostRow.getByRole('button', { name: 'Restore its item' })).toBeInTheDocument()

    // A file that could not be read, in an item that could, still waits for
    // its own confirmation.
    const damagedRow = within(fileList.getByText('.precious-quarantine/50/2/2004/d.raw').closest('li')!)
    expect(damagedRow.getByRole('button', { name: 'Confirm' })).toBeInTheDocument()
    expect(damagedRow.queryByText(/Stays in quarantine/)).not.toBeInTheDocument()
  })

  it('offers nothing to confirm or delete once nothing of the set is left in quarantine', async () => {
    stubApi(reportRoutes(() => check({ items: 0, allowed: true }), () => [photo]))
    renderApp('/cleanup/checks/9')
    expect(await screen.findByText('Nothing of this set is left in quarantine.')).toBeInTheDocument()
    await screen.findByRole('list', { name: 'Files' })
    expect(screen.queryByRole('button', { name: 'Delete for good…' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^Confirm/ })).not.toBeInTheDocument()
  })

  it('checks again what is left of a stale check’s set, named by the check', async () => {
    const requests = stubApi(
      reportRoutes(
        () => check({ state: 'stale', stale_reason: 'copy_changed' }),
        () => [photo, junk],
        {
          // The server finds what is left of the set: the quarantine is not read.
          'GET /api/quarantine': () => errorResponse(500, 'internal'),
          'POST /api/commands/check-purge': () => jsonResponse(202, { check_id: '10', job_id: '32' }),
          'GET /api/checks/10': () => jsonResponse(200, check({ id: '10', state: 'running' })),
        },
      ),
    )
    const { router } = renderApp('/cleanup/checks/9')
    expect(await screen.findByText(/This check is out of date/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Delete for good…' })).toBeDisabled()
    expect(screen.queryByRole('button', { name: 'Confirm' })).not.toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: 'Check again' }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/cleanup/checks/10'))
    expect(await bodies(requests, 'check-purge')).toEqual([{ check_id: '9' }])
    expect(requests.some((r) => new URL(r.url).pathname === '/api/quarantine')).toBe(false)
    expectSigned(requests)
  })

  it('says so when checking again finds nothing of the set left in quarantine', async () => {
    const requests = stubApi(
      reportRoutes(
        () => check({ state: 'stale', stale_reason: 'copy_changed' }),
        () => [photo],
        {
          'POST /api/commands/check-purge': () =>
            errorResponse(404, 'not_found', 'no item of check 9 is in the quarantine any more'),
        },
      ),
    )
    const { router } = renderApp('/cleanup/checks/9')
    await userEvent.click(await screen.findByRole('button', { name: 'Check again' }))
    expect(await screen.findByText('Nothing of this set is left in quarantine.')).toBeInTheDocument()
    expect(screen.queryByText(/no item of check 9/)).not.toBeInTheDocument()
    expect(router.state.location.pathname).toBe('/cleanup/checks/9')
    expect(await bodies(requests, 'check-purge')).toEqual([{ check_id: '9' }])
  })

  // purgeRoutes is a check 9 that allows its purge, and the purge it plans,
  // which ends as ended says.
  function purgeRoutes(
    source: Source,
    ended: Partial<Action> = {
      state: 'done',
      entries: entryCounts({ done: 2 }),
      deleted_files: 3,
      deleted_bytes: 3 * MiB,
      freed_bytes: 2 * MiB,
    },
  ) {
    const plan = action({
      id: '70',
      kind: 'purge',
      check_id: '9',
      files: 3,
      bytes: 3 * MiB,
      entries: entryCounts({ planned: 2 }),
    })
    return {
      ...routes([source], {
        'GET /api/checks/9': () =>
          jsonResponse(200, check({ allowed: true, junk_confirmed: true, unconfirmed: { files: 0, bytes: 0 } })),
        'GET /api/checks/9/files': () => jsonResponse(200, { items: [], next_cursor: null }),
      }),
      'POST /api/commands/plan-purge': () => jsonResponse(201, { action: plan, items: [], next_cursor: null }),
      'POST /api/commands/run-action': () =>
        jsonResponse(202, { action: { ...plan, state: 'queued' }, job_id: '8', state: 'queued' }),
      'GET /api/history/70': () => jsonResponse(200, { ...plan, ...ended }),
    }
  }

  async function runPurge() {
    await userEvent.click(await screen.findByRole('button', { name: 'Delete for good…' }))
    await userEvent.click(
      within(await screen.findByRole('alertdialog', { name: 'Delete for good?' })).getByRole('button', {
        name: 'Delete for good',
      }),
    )
  }

  it('shows what changed on disk when a purge stops at its comparison', async () => {
    const verify = actionItem('701', '', '', {
      op: 'verify',
      from: null,
      to: null,
      state: 'changed',
      reason: 'copy_changed',
      detail: 'Docs/x.txt',
    })
    stubApi({
      ...purgeRoutes(writable, { state: 'stopped', entries: entryCounts({ not_attempted: 2 }) }),
      'GET /api/history/70/items': (request) =>
        new URL(request.url).searchParams.getAll('op').join() === 'verify'
          ? jsonResponse(200, { items: [verify], next_cursor: null })
          : errorResponse(400, 'invalid_request', 'op'),
    })
    renderApp('/cleanup/checks/9')
    await runPurge()
    expect(await screen.findByText('Stopped before the end.')).toBeInTheDocument()
    const why = within(await screen.findByRole('list', { name: 'Why it stopped' }))
    expect(why.getByText(/A copy it relied on changed since the check/)).toBeInTheDocument()
    expect(why.getByText('Docs/x.txt')).toBeInTheDocument()
  })

  it('keeps nothing of one check’s purge on the report of another', async () => {
    const ready = check({ allowed: true, junk_confirmed: true, unconfirmed: { files: 0, bytes: 0 } })
    const other = check({ id: '10', state: 'stale', stale_reason: 'copy_changed' })
    stubApi({
      ...purgeRoutes(writable),
      'GET /api/checks/10': () => jsonResponse(200, other),
      'GET /api/checks/10/files': () => jsonResponse(200, { items: [], next_cursor: null }),
    })
    const { router, queryClient } = renderApp('/cleanup/checks/9')
    // Both checks are known, so the report shows the other at once.
    queryClient.setQueryData(checkQueryKey('9'), ready)
    queryClient.setQueryData(checkQueryKey('10'), other)

    await userEvent.click(await screen.findByRole('button', { name: 'Delete for good…' }))
    expect(await screen.findByRole('alertdialog', { name: 'Delete for good?' })).toBeInTheDocument()
    await act(() => router.navigate('/cleanup/checks/10'))
    expect(await screen.findByText(/This check is out of date/)).toBeInTheDocument()
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()

    await act(() => router.navigate('/cleanup/checks/9'))
    await runPurge()
    expect(await screen.findByText('Deleted for good.')).toBeInTheDocument()
    await act(() => router.navigate('/cleanup/checks/10'))
    expect(await screen.findByText(/This check is out of date/)).toBeInTheDocument()
    expect(screen.queryByText('Deleted for good.')).not.toBeInTheDocument()
    expect(screen.queryByText('Space freed: 2 MiB')).not.toBeInTheDocument()
  })

  it('R4.5: deletes for good after a last confirmation, and shows the space freed and the ZFS note', async () => {
    const requests = stubApi(purgeRoutes(writable))
    renderApp('/cleanup/checks/9')
    await userEvent.click(await screen.findByRole('button', { name: 'Delete for good…' }))
    const confirm = within(await screen.findByRole('alertdialog', { name: 'Delete for good?' }))
    expect(
      confirm.getByText('This deletes 2 items from the quarantine, with 3 files and 3 MiB. It cannot be undone.'),
    ).toBeInTheDocument()
    expect(commandNames(requests)).toEqual(['plan-purge'])
    expect(await bodies(requests, 'plan-purge')).toEqual([{ check_id: '9' }])

    await userEvent.click(confirm.getByRole('button', { name: 'Delete for good' }))
    expect(await screen.findByText('Deleted for good.')).toBeInTheDocument()
    expect(screen.getByText('Deleted 3 files, 3 MiB.')).toBeInTheDocument()
    expect(screen.getByText('Space freed: 2 MiB')).toBeInTheDocument()
    expect(screen.getByText(/This source is on ZFS: snapshots/)).toBeInTheDocument()
    expect(await bodies(requests, 'run-action')).toEqual([{ action_id: '70' }])
    expectSigned(requests)
  })

  it('has no snapshot note off ZFS, and keeping the files runs nothing', async () => {
    const ext4 = fotosSource({
      writes: { enabled: true, unavailable: null },
      volume: { kind: 'uuid', id: 'u-1', label: null, fs_type: 'ext4', strong: true },
    })
    const requests = stubApi(purgeRoutes(ext4))
    renderApp('/cleanup/checks/9')
    await userEvent.click(await screen.findByRole('button', { name: 'Delete for good…' }))
    await userEvent.click(
      within(await screen.findByRole('alertdialog', { name: 'Delete for good?' })).getByRole('button', {
        name: 'Keep them',
      }),
    )
    expect(commandNames(requests)).toEqual(['plan-purge'])

    await userEvent.click(screen.getByRole('button', { name: 'Delete for good…' }))
    await userEvent.click(
      within(await screen.findByRole('alertdialog', { name: 'Delete for good?' })).getByRole('button', {
        name: 'Delete for good',
      }),
    )
    expect(await screen.findByText('Space freed: 2 MiB')).toBeInTheDocument()
    expect(screen.queryByText(/ZFS/)).not.toBeInTheDocument()
  })
})
