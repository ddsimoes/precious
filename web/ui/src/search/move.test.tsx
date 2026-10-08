import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import { action, actionItem, entryRow, folderRow, fotosSource, usbSource } from '@/test/fixtures'
import { jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'

type Route = (request: Request) => Response | Promise<Response>

const tmpFiles = Array.from({ length: 4 }, (_, i) =>
  entryRow({ id: String(300 + i), name: `f${i}.tmp`, path: `Backup_PC_2004/f${i}.tmp` }),
)
const documentos = folderRow('40', 'Documentos')

// The bulk move of the four results into Documentos: two planned, one whose
// name is taken there, and one kept file that would lose its keep. The
// first page holds three items; the fourth comes with Load more.
const planned = action(
  { id: '90', bulk: true, destination: documentos, files: 2, bytes: 2048 },
  { planned: 2, conflict: 1, refused: 1 },
)
const firstPage = [
  actionItem('1', 'Backup_PC_2004/f0.tmp', 'Documentos/f0.tmp'),
  actionItem('2', 'Backup_PC_2004/f1.tmp', 'Documentos/f1.tmp', { state: 'conflict', reason: 'name_taken' }),
  actionItem('3', 'Backup_PC_2004/f2.tmp', 'Documentos/f2.tmp', { state: 'refused', reason: 'would_lose_keep' }),
]
const secondPage = [actionItem('4', 'Backup_PC_2004/f3.tmp', 'Documentos/f3.tmp')]

function routes(extra: Record<string, Route> = {}): Record<string, Route> {
  return {
    'GET /api/session': () => jsonResponse(200, signedIn),
    'GET /api/sources': () =>
      jsonResponse(200, {
        sources: [fotosSource({ writes: { enabled: true, unavailable: null } }), usbSource()],
      }),
    'GET /api/tags': () => jsonResponse(200, { tags: [] }),
    'GET /api/search': (request) =>
      new URL(request.url).searchParams.get('count') === 'only'
        ? jsonResponse(200, { count: 4 })
        : jsonResponse(200, { items: tmpFiles, next_cursor: null }),
    'POST /api/commands/create-selection': () =>
      jsonResponse(201, {
        selection_id: 'sel-1',
        count: 4,
        bytes: 4096,
        kept: { count: 1, bytes: 1024 },
        expires_at: '2026-10-07T11:00:00Z',
      }),
    'GET /api/entries/1/children': () => jsonResponse(200, { items: [documentos], next_cursor: null }),
    'GET /api/entries/40/children': () => jsonResponse(200, { items: [], next_cursor: null }),
    'POST /api/commands/plan-move': () => jsonResponse(201, { action: planned, items: firstPage, next_cursor: 'c2' }),
    'GET /api/history/90/items': () => jsonResponse(200, { items: secondPage, next_cursor: null }),
    'POST /api/commands/run-action': () =>
      jsonResponse(202, { action: { ...planned, state: 'queued' }, job_id: '9', state: 'queued' }),
    'GET /api/history/90': () =>
      jsonResponse(200, action({ ...planned, state: 'done' }, { done: 2, conflict: 1, refused: 1 })),
    ...extra,
  }
}

function commandNames(requests: Request[]) {
  return requests
    .filter((r) => new URL(r.url).pathname.startsWith('/api/commands/'))
    .map((r) => new URL(r.url).pathname.slice('/api/commands/'.length))
}

function bodies(requests: Request[], name: string) {
  return Promise.all(
    requests
      .filter((r) => r.method === 'POST' && new URL(r.url).pathname === `/api/commands/${name}`)
      .map((r) => r.clone().json() as Promise<unknown>),
  )
}

// lines lists the text of a list's items, with spaces normalized.
function lines(list: HTMLElement) {
  return within(list)
    .getAllByRole('listitem')
    .map((item) => item.textContent.replace(/\s+/g, ' '))
}

describe('Moving search results', () => {
  it('R3.4: previews every item, conflict, and refused item with its reason, and runs only after Confirm', async () => {
    const requests = stubApi(routes())
    renderApp('/search?name=tmp')
    await screen.findByText('4 results')

    await userEvent.click(screen.getByRole('button', { name: 'Select all results' }))
    await userEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Select all' }))
    const bulk = within(screen.getByRole('region', { name: 'Change the selected items' }))
    await userEvent.click(bulk.getByRole('button', { name: 'Move to…' }))

    // Only Fotos allows changes, so the chooser opens at its top folder.
    const chooser = within(await screen.findByRole('dialog', { name: 'Move 4 items to…' }))
    await userEvent.click(await chooser.findByRole('button', { name: 'Documentos' }))
    await chooser.findByText('This folder has no subfolders.')
    await userEvent.click(chooser.getByRole('button', { name: 'Move here' }))

    const preview = within(await screen.findByRole('alertdialog', { name: 'Move into “Documentos”' }))
    expect(lines(preview.getByRole('list', { name: 'Items' }))).toEqual([
      '2 items will be changed',
      '1 item cannot go there and stays as it is',
      '1 item is not included',
      'In total: 2 files, 2 KiB',
    ])
    expect(lines(preview.getByRole('list', { name: 'Will be changed' }))).toEqual([
      'Backup_PC_2004/f0.tmp → Documentos/f0.tmp',
    ])
    expect(lines(preview.getByRole('list', { name: 'Cannot go there' }))).toEqual([
      'Backup_PC_2004/f1.tmp → Documentos/f1.tmp' + 'Name taken',
    ])
    expect(lines(preview.getByRole('list', { name: 'Not included' }))).toEqual([
      'Backup_PC_2004/f2.tmp → Documentos/f2.tmp' + 'It is kept, and would no longer be kept there',
    ])
    await userEvent.click(preview.getByRole('button', { name: 'Load more' }))
    expect(
      await preview.findByText('Backup_PC_2004/f3.tmp → Documentos/f3.tmp'),
    ).toBeInTheDocument()
    expect(lines(preview.getByRole('list', { name: 'Will be changed' }))).toHaveLength(2)
    expect(preview.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument()
    const pageRequest = requests.find((r) => new URL(r.url).pathname === '/api/history/90/items')
    expect(new URL(pageRequest?.url ?? '').searchParams.get('cursor')).toBe('c2')

    // Nothing runs until the owner confirms.
    expect(commandNames(requests)).toEqual(['create-selection', 'plan-move'])
    expect(await bodies(requests, 'plan-move')).toEqual([{ selection_id: 'sel-1', destination_id: '40' }])

    await userEvent.click(preview.getByRole('button', { name: 'Confirm' }))
    expect(await screen.findByText('Moved.')).toBeInTheDocument()
    expect(screen.getByText('Not everything was changed: see History.')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'See History' })).toHaveAttribute('href', '/history')
    expect(commandNames(requests)).toEqual(['create-selection', 'plan-move', 'run-action'])
    expect(await bodies(requests, 'run-action')).toEqual([{ action_id: '90' }])
    // The selection was used: the bulk section is gone.
    expect(screen.queryByRole('region', { name: 'Change the selected items' })).not.toBeInTheDocument()
  })

  it('cancelling the preview runs nothing', async () => {
    const requests = stubApi(routes())
    renderApp('/search?name=tmp')
    await screen.findByText('4 results')

    await userEvent.click(screen.getByRole('checkbox', { name: 'Select f0.tmp' }))
    await userEvent.click(screen.getByRole('checkbox', { name: 'Select f1.tmp' }))
    await userEvent.click(
      within(screen.getByRole('region', { name: 'Change the selected items' })).getByRole('button', { name: 'Move to…' }),
    )
    const chooser = within(await screen.findByRole('dialog', { name: 'Move 2 items to…' }))
    await userEvent.click(await chooser.findByRole('button', { name: 'Documentos' }))
    await userEvent.click(chooser.getByRole('button', { name: 'Move here' }))
    const preview = within(await screen.findByRole('alertdialog', { name: 'Move into “Documentos”' }))
    await userEvent.click(preview.getByRole('button', { name: 'Cancel' }))

    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    expect(commandNames(requests)).toEqual(['plan-move'])
    expect(await bodies(requests, 'plan-move')).toEqual([{ entry_ids: ['300', '301'], destination_id: '40' }])
    // The rows stay selected.
    expect(screen.getByRole('region', { name: 'Change the selected items' })).toBeInTheDocument()
  })
})
