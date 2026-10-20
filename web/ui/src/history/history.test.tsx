import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import type { Action } from '@/api/organize'
import { MockEventSource } from '@/test/eventSource'
import { action, actionItem, folderRow, fotosSource, scanEvent } from '@/test/fixtures'
import { jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'

type Route = (request: Request) => Response | Promise<Response>

const documentos = folderRow('40', 'Documentos')

const moved = action(
  {
    id: '12',
    state: 'done',
    destination: documentos,
    files: 3,
    bytes: 3 * 1024 ** 2,
    started_at: '2026-10-07T10:01:00Z',
    finished_at: '2026-10-07T10:02:00Z',
    undo: { possible: true, reason: null },
  },
  { done: 3, conflict: 1 },
)
const waiting = action({ id: '13', state: 'queued', destination: documentos, bulk: true }, { planned: 2 })
const stuck = action(
  { id: '14', kind: 'rename', state: 'stopped', finished_at: '2026-10-07T09:00:00Z' },
  { manual_recovery: 1 },
)
const undone = action(
  { id: '11', state: 'done', destination: documentos, undo: { possible: false, reason: 'already_undone' } },
  { done: 1 },
)

const recoveryItem = actionItem('301', 'Docs/a.txt', 'Docs/b.txt', {
  state: 'manual_recovery',
  found: { from: 'same', to: 'other' },
})

function routes(history: () => Action[], extra: Record<string, Route> = {}): Record<string, Route> {
  return {
    'GET /api/session': () => jsonResponse(200, signedIn),
    'GET /api/sources': () =>
      jsonResponse(200, { sources: [fotosSource({ writes: { enabled: true, unavailable: null } })] }),
    'GET /api/history': () => jsonResponse(200, { items: history(), next_cursor: null }),
    'GET /api/history/14/items': () => jsonResponse(200, { items: [recoveryItem], next_cursor: null }),
    ...extra,
  }
}

function bodies(requests: Request[], name: string) {
  return Promise.all(
    requests
      .filter((r) => r.method === 'POST' && new URL(r.url).pathname === `/api/commands/${name}`)
      .map((r) => r.clone().json() as Promise<unknown>),
  )
}

function historyGets(requests: Request[]) {
  return requests.filter((r) => new URL(r.url).pathname === '/api/history').length
}

function card(name: string) {
  return within(screen.getByRole('article', { name }))
}

describe('History', () => {
  it('lists the actions newest first, with their state and counts', async () => {
    stubApi(routes(() => [waiting, moved, stuck, undone]))
    renderApp('/history')

    const list = within(await screen.findByRole('list', { name: 'Changes' }))
    expect(list.getAllByRole('heading', { level: 2 }).map((h) => h.textContent)).toEqual([
      'Move into “Documentos”',
      'Move into “Documentos”',
      'Rename',
      'Move into “Documentos”',
    ])
    const done = within(list.getAllByRole('article')[1]!)
    expect(done.getByText('Done')).toBeInTheDocument()
    expect(
      within(done.getByRole('list', { name: 'Items' }))
        .getAllByRole('listitem')
        .map((li) => li.textContent),
    ).toEqual(['Done: 3', 'Left as they were: 1'])
    await waitFor(() =>
      expect(done.getByText(/Oct 7, 2026/)).toHaveTextContent(/^Fotos · Oct 7, 2026, 10:02\sAM · 3 files, 3\sMiB$/),
    )
    expect(done.getByRole('button', { name: 'Undo' })).toBeInTheDocument()
    expect(done.queryByRole('button', { name: 'Cancel' })).not.toBeInTheDocument()

    const queued = within(list.getAllByRole('article')[0]!)
    expect(queued.getByText('Waiting for its turn')).toBeInTheDocument()
    expect(queued.getByRole('button', { name: 'Cancel' })).toBeInTheDocument()
    expect(queued.queryByRole('button', { name: 'Undo' })).not.toBeInTheDocument()

    const old = within(list.getAllByRole('article')[3]!)
    expect(old.getByText('Undone')).toBeInTheDocument()
    expect(old.queryByRole('button', { name: 'Undo' })).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'History', current: 'page' })).toHaveAttribute('href', '/history')
  })

  it('undoes an action, and lists its items on demand', async () => {
    const undo = action({ id: '20', kind: 'undo', undo_of: '12' }, { planned: 3 })
    const requests = stubApi(
      routes(() => [moved], {
        'GET /api/history/12/items': () =>
          jsonResponse(200, {
            items: [
              actionItem('1', 'Fotos/2004', 'Documentos/2004', { state: 'done' }),
              actionItem('2', 'Fotos/x.jpg', 'Documentos/x.jpg', { state: 'conflict', reason: 'name_taken' }),
            ],
            next_cursor: null,
          }),
        'POST /api/commands/plan-undo': () => jsonResponse(201, { action: undo, items: [], next_cursor: null }),
        'POST /api/commands/run-action': () =>
          jsonResponse(202, { action: { ...undo, state: 'queued' }, job_id: '9', state: 'queued' }),
        'GET /api/history/20': () => jsonResponse(200, { ...undo, state: 'running' }),
      }),
    )
    renderApp('/history')
    await screen.findByRole('article', { name: 'Move into “Documentos”' })
    const moveCard = card('Move into “Documentos”')

    await userEvent.click(moveCard.getByRole('button', { name: 'Show items' }))
    const items = within(await moveCard.findByRole('list', { name: 'Items of this change' }))
    expect(items.getAllByRole('listitem').map((li) => li.textContent)).toEqual([
      'Fotos/2004 → Documentos/2004Done',
      'Fotos/x.jpg → Documentos/x.jpgLeft as it is · Name taken',
    ])

    await userEvent.click(moveCard.getByRole('button', { name: 'Undo' }))
    expect(await moveCard.findByText('Undoing…')).toBeInTheDocument()
    expect(await bodies(requests, 'plan-undo')).toEqual([{ action_id: '12' }])
    expect(await bodies(requests, 'run-action')).toEqual([{ action_id: '20' }])
  })

  it('asks for a destination when an undo has conflicts', async () => {
    const conflicted = action({ id: '21', kind: 'undo', undo_of: '12' }, { planned: 2, conflict: 1 })
    const redirected = action({ id: '22', kind: 'undo', undo_of: '12', destination: documentos }, { planned: 3 })
    const requests = stubApi(
      routes(() => [moved], {
        'POST /api/commands/plan-undo': async (request) => {
          const body = (await request.clone().json()) as { destination_id?: string }
          return body.destination_id === undefined
            ? jsonResponse(201, {
                action: conflicted,
                items: [actionItem('5', 'Documentos/x.jpg', 'Fotos/x.jpg', { state: 'conflict', reason: 'name_taken' })],
                next_cursor: null,
              })
            : jsonResponse(201, {
                action: redirected,
                items: [actionItem('6', 'Documentos/x.jpg', 'Documentos/Old/x.jpg')],
                next_cursor: null,
              })
        },
        'GET /api/entries/1/children': () => jsonResponse(200, { items: [documentos], next_cursor: null }),
        'GET /api/entries/40/children': () => jsonResponse(200, { items: [], next_cursor: null }),
      }),
    )
    renderApp('/history')
    await screen.findByRole('article', { name: 'Move into “Documentos”' })

    await userEvent.click(card('Move into “Documentos”').getByRole('button', { name: 'Undo' }))
    const preview = within(await screen.findByRole('alertdialog', { name: 'Undo' }))
    expect(preview.getByText('Documentos/x.jpg → Fotos/x.jpg')).toBeInTheDocument()
    await userEvent.click(preview.getByRole('button', { name: 'Choose a folder for them…' }))
    const chooser = within(
      await screen.findByRole('dialog', { name: 'Choose a folder for the items that cannot go back' }),
    )
    await userEvent.click(await chooser.findByRole('button', { name: 'Documentos' }))
    await userEvent.click(chooser.getByRole('button', { name: 'Move here' }))

    expect(await screen.findByRole('alertdialog', { name: 'Undo into “Documentos”' })).toBeInTheDocument()
    expect(await bodies(requests, 'plan-undo')).toEqual([{ action_id: '12' }, { action_id: '12', destination_id: '40' }])
    expect(requests.some((r) => new URL(r.url).pathname === '/api/commands/run-action')).toBe(false)
  })

  it('cancels a queued action', async () => {
    let history = [waiting]
    const requests = stubApi(
      routes(() => history, {
        'POST /api/commands/cancel-action': () => {
          history = [{ ...waiting, state: 'stopped', counts: { ...waiting.counts, planned: 0, not_attempted: 2 } }]
          return jsonResponse(200, { action: history[0] })
        },
      }),
    )
    renderApp('/history')
    await screen.findByRole('article', { name: 'Move into “Documentos”' })

    await userEvent.click(card('Move into “Documentos”').getByRole('button', { name: 'Cancel' }))
    expect(await card('Move into “Documentos”').findByText('Stopped')).toBeInTheDocument()
    expect(card('Move into “Documentos”').getByText('Not attempted: 2')).toBeInTheDocument()
    expect(card('Move into “Documentos”').queryByRole('button', { name: 'Cancel' })).not.toBeInTheDocument()
    expect(await bodies(requests, 'cancel-action')).toEqual([{ action_id: '13' }])
  })

  it('shows an item that needs a check with both paths and what was found, and resolves it', async () => {
    let history = [stuck]
    const requests = stubApi(
      routes(() => history, {
        'POST /api/commands/resolve-recovery': () => {
          history = [{ ...stuck, counts: { ...stuck.counts, manual_recovery: 0, resolved: 1 } }]
          return jsonResponse(200, { action: history[0], scan: { job_id: '50', coalesced: false } })
        },
      }),
    )
    renderApp('/history')
    await screen.findByRole('article', { name: 'Rename' })
    const rename = card('Rename')

    expect(rename.getByText('Needs your check: 1')).toBeInTheDocument()
    const check = within(await rename.findByRole('list', { name: 'Items to check' }))
    expect(check.getByText('Before: Docs/a.txt')).toBeInTheDocument()
    expect(check.getByText('After: Docs/b.txt')).toBeInTheDocument()
    expect(
      check.getByText('Found at the place before: the item. At the place after: something else.'),
    ).toBeInTheDocument()
    const itemsRequest = requests.find((r) => new URL(r.url).pathname === '/api/history/14/items')
    expect(new URL(itemsRequest?.url ?? '').searchParams.getAll('state')).toEqual(['manual_recovery'])

    await userEvent.click(check.getByRole('button', { name: 'I fixed it' }))
    await waitFor(() => expect(rename.queryByText('Needs your check')).not.toBeInTheDocument())
    expect(await bodies(requests, 'resolve-recovery')).toEqual([{ item_id: '301' }])
  })

  it('offers no Undo for a cleanup or a purge, names them, and exports every action as CSV', async () => {
    const cleanup = action(
      {
        id: '30',
        kind: 'cleanup',
        state: 'done',
        list: 'system_junk',
        finished_at: '2026-10-08T10:00:00Z',
        entries: { ...moved.counts, done: 2, blocked: 1, conflict: 0 },
        // Even an answer that says otherwise offers no Undo for these kinds.
        undo: { possible: true, reason: null },
      },
      { done: 6, blocked: 1 },
    )
    const purge = action({
      id: '31',
      kind: 'purge',
      state: 'done',
      finished_at: '2026-10-08T11:00:00Z',
      entries: { ...moved.counts, done: 2, conflict: 0 },
      undo: { possible: false, reason: 'not_undoable_kind' },
      deleted_files: 3,
      deleted_bytes: 3 * 1024 ** 2,
      freed_bytes: 2 * 1024 ** 2,
    })
    const requests = stubApi(
      routes(() => [purge, cleanup, moved], {
        'GET /api/history/30/items': (request) =>
          jsonResponse(200, {
            items:
              new URL(request.url).searchParams.getAll('op').join() === 'rename'
                ? [
                    actionItem('1', 'Thumbs.db', '.precious-quarantine/30/1/Thumbs.db', { state: 'done' }),
                    actionItem('2', 'Fotos', '.precious-quarantine/30/2/Fotos', {
                      state: 'blocked',
                      reason: 'holds_kept',
                    }),
                  ]
                : [],
            next_cursor: null,
          }),
      }),
    )
    renderApp('/history')

    await screen.findByRole('article', { name: 'Move the discarded items of “System junk” to quarantine' })
    const quarantine = card('Move the discarded items of “System junk” to quarantine')
    expect(quarantine.queryByRole('button', { name: 'Undo' })).not.toBeInTheDocument()
    expect(
      within(quarantine.getByRole('list', { name: 'Items' }))
        .getAllByRole('listitem')
        .map((li) => li.textContent),
    ).toEqual(['Done: 2', 'Blocked: 1'])
    expect(quarantine.getByRole('link', { name: 'Export CSV' })).toHaveAttribute('href', '/api/history/30/export.csv')
    await userEvent.click(quarantine.getByRole('button', { name: 'Show items' }))
    expect(
      await quarantine.findByText('Fotos → .precious-quarantine/30/2/Fotos'),
    ).toBeInTheDocument()
    expect(quarantine.getByText('Blocked by kept items · Holds kept items')).toBeInTheDocument()

    const deleted = card('Delete for good')
    expect(deleted.queryByRole('button', { name: 'Undo' })).not.toBeInTheDocument()
    expect(deleted.getByText(/^Deleted 3 files, 3\sMiB\. Space freed: 2\sMiB\.$/)).toBeInTheDocument()
    expect(deleted.getByRole('link', { name: 'Export CSV' })).toHaveAttribute('href', '/api/history/31/export.csv')
    expect(card('Move into “Documentos”').getByRole('link', { name: 'Export CSV' })).toHaveAttribute(
      'href',
      '/api/history/12/export.csv',
    )
    const listing = requests.find((r) => new URL(r.url).pathname === '/api/history/30/items')
    expect(new URL(listing?.url ?? '').searchParams.getAll('op')).toEqual(['rename'])
  })

  it('shows what changed on disk when a purge stopped at its comparison', async () => {
    const purge = action({
      id: '32',
      kind: 'purge',
      state: 'stopped',
      finished_at: '2026-10-08T11:00:00Z',
      entries: { ...moved.counts, done: 0, conflict: 0, not_attempted: 1 },
      undo: { possible: false, reason: 'not_undoable_kind' },
    })
    const verify = actionItem('1', '', '', {
      op: 'verify',
      from: null,
      to: null,
      state: 'changed',
      reason: 'copy_changed',
      detail: 'Docs/x.txt',
    })
    const deletion = actionItem('2', '.precious-quarantine/50/2/2004', '', {
      op: 'purge',
      to: null,
      state: 'not_attempted',
    })
    stubApi(
      routes(() => [purge], {
        'GET /api/history/32/items': (request) => {
          const ops = new URL(request.url).searchParams.getAll('op')
          return jsonResponse(200, {
            items: [verify, deletion].filter((item) => ops.includes(item.op)),
            next_cursor: null,
          })
        },
      }),
    )
    renderApp('/history')

    await screen.findByRole('article', { name: 'Delete for good' })
    const stopped = card('Delete for good')
    expect(
      within(stopped.getByRole('list', { name: 'Items' }))
        .getAllByRole('listitem')
        .map((li) => li.textContent),
    ).toEqual(['Done: 0', 'Not attempted: 1'])
    await userEvent.click(stopped.getByRole('button', { name: 'Show items' }))
    expect(await stopped.findByText(/A copy it relied on changed since the check/)).toBeInTheDocument()
    expect(stopped.getByText('Docs/x.txt')).toBeInTheDocument()
    expect(stopped.getByText('Delete for good: .precious-quarantine/50/2/2004')).toBeInTheDocument()
  })

  it('follows organize job events', async () => {
    let history = [waiting]
    const requests = stubApi(routes(() => history))
    renderApp('/history')
    await screen.findByText('Waiting for its turn')
    const stream = MockEventSource.latest()
    act(() => stream.open())
    // A progress event refetches the history; one that ends refetches it
    // with everything a move changes.
    const before = historyGets(requests)
    act(() => stream.emit('job', scanEvent({ job_id: '9', kind: 'organize', progress: { items: 2, done: 1 } }), '1'))
    await waitFor(() => expect(historyGets(requests)).toBe(before + 1))

    history = [{ ...waiting, state: 'done', counts: { ...waiting.counts, planned: 0, done: 2 } }]
    act(() =>
      stream.emit(
        'job',
        scanEvent({ job_id: '9', kind: 'organize', state: 'succeeded', progress: { items: 2, done: 2 } }),
        '2',
      ),
    )
    expect(await screen.findByText('Done: 2')).toBeInTheDocument()
    expect(historyGets(requests)).toBe(before + 2)
  })

  it('titles the date actions, shows each file’s old and new time, explains not_owner, and offers Undo for both', async () => {
    const fotos = folderRow('30', 'Fotos')
    const setDates = action(
      { id: '60', kind: 'set_mtime', state: 'done', bulk: true, undo: { possible: true, reason: null } },
      { done: 2, failed: 1 },
    )
    const byDate = action(
      {
        id: '61',
        kind: 'date_organize',
        state: 'done',
        bulk: true,
        destination: fotos,
        template: '{year}/{month}',
        rename: true,
        undo: { possible: true, reason: null },
      },
      { done: 3 },
    )
    const path = (n: number) => `Viagens/2008-03 Ouro Preto/DSCN000${n}.JPG`
    const undo = action({ id: '62', kind: 'undo', undo_of: '60', bulk: true }, { planned: 2 })
    const requests = stubApi(
      routes(() => [setDates, byDate], {
        'GET /api/history/60/items': () =>
          jsonResponse(200, {
            items: [
              actionItem('1', path(1), path(1), {
                op: 'set_mtime',
                to: null,
                state: 'done',
                mtime: { from: '2011-01-15T10:00:00Z', to: '2008-03-22T17:00:00.12Z' },
              }),
              actionItem('2', path(2), path(2), {
                op: 'set_mtime',
                to: null,
                state: 'failed',
                reason: 'not_owner',
                detail: 'operation not permitted',
                mtime: { from: '2011-01-15T10:00:00Z', to: '2008-03-22T17:10:00.34Z' },
              }),
            ],
            next_cursor: null,
          }),
        'POST /api/commands/plan-undo': () => jsonResponse(201, { action: undo, items: [], next_cursor: null }),
      }),
    )
    renderApp('/history')

    const list = within(await screen.findByRole('list', { name: 'Changes' }))
    expect(list.getAllByRole('heading', { level: 2 }).map((h) => h.textContent)).toEqual([
      'Set file dates',
      'Organize by date into “Fotos”',
    ])
    const organized = card('Organize by date into “Fotos”')
    expect(organized.getByText('Folders: {year}/{month} · Files renamed to their date and time')).toBeInTheDocument()
    expect(organized.getByRole('button', { name: 'Undo' })).toBeInTheDocument()

    const dates = card('Set file dates')
    expect(dates.getByText('Not done: 1')).toBeInTheDocument()
    await userEvent.click(dates.getByRole('button', { name: 'Show items' }))
    const items = within(await dates.findByRole('list', { name: 'Items of this change' }))
    expect(items.getAllByRole('listitem').map((li) => (li.textContent ?? '').replace(/\s/g, ' '))).toEqual([
      `${path(1)}: Jan 15, 2011, 10:00:00 AM → Mar 22, 2008, 5:00:00 PMDone`,
      `${path(2)}: Jan 15, 2011, 10:00:00 AM → Mar 22, 2008, 5:10:00 PMFailed · The file belongs to another user on the server, so Precious may not set its date. The operator guide explains how to allow it.operation not permitted`,
    ])

    // Undo of a set-file-dates action is previewed: it sets the times back.
    await userEvent.click(dates.getByRole('button', { name: 'Undo' }))
    expect(await screen.findByRole('alertdialog', { name: 'Undo' })).toBeInTheDocument()
    expect(await bodies(requests, 'plan-undo')).toEqual([{ action_id: '60' }])
  })
})
