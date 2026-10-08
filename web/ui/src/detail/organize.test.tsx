import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import type { Action } from '@/api/organize'
import { action, actionItem, entryDetail, entryRow, folderRow, fotosSource } from '@/test/fixtures'
import { errorResponse, jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'

type Route = (request: Request) => Response | Promise<Response>

// setupExe is the file Downloads/setup.exe of fotos, kept through Downloads.
const setupExe = entryDetail(entryRow({ eff_decision: 'keep' }), {
  ancestors: [
    { id: '1', name: '', name_b64: '', only_child: false },
    { id: '5', name: 'Downloads', name_b64: btoa('Downloads'), only_child: false },
  ],
})

const downloads = folderRow('5', 'Downloads')
const old = folderRow('30', 'Old', { path: 'Downloads/Old' })

function routes(writes: boolean, extra: Record<string, Route> = {}): Record<string, Route> {
  return {
    'GET /api/session': () => jsonResponse(200, signedIn),
    'GET /api/sources': () =>
      jsonResponse(200, { sources: [fotosSource({ writes: { enabled: writes, unavailable: null } })] }),
    'GET /api/tags': () => jsonResponse(200, { tags: [] }),
    'GET /api/entries/12': () => jsonResponse(200, setupExe),
    ...extra,
  }
}

function commandRequests(requests: Request[], name: string) {
  return requests.filter((r) => r.method === 'POST' && new URL(r.url).pathname === `/api/commands/${name}`)
}

function commandNames(requests: Request[]) {
  return requests
    .filter((r) => new URL(r.url).pathname.startsWith('/api/commands/'))
    .map((r) => new URL(r.url).pathname.slice('/api/commands/'.length))
}

function bodies(requests: Request[], name: string) {
  return Promise.all(commandRequests(requests, name).map((r) => r.clone().json() as Promise<unknown>))
}

// runRoute answers run-action with the action it names, queued.
function runRoute(actions: Record<string, Action>): Route {
  return async (request) => {
    const { action_id } = (await request.clone().json()) as { action_id: string }
    return jsonResponse(202, { action: { ...actions[action_id], state: 'queued' }, job_id: '9', state: 'queued' })
  }
}

async function organizeSection() {
  const panel = within(await screen.findByRole('complementary', { name: 'setup.exe' }))
  return within(await panel.findByRole('region', { name: 'Organize' }))
}

describe('Organize in the detail panel', () => {
  it('runs a one-item rename at once and offers Undo', async () => {
    const renamed = action({ id: '77', kind: 'rename' }, { planned: 1 })
    const undo = action({ id: '78', kind: 'undo', undo_of: '77' }, { planned: 1 })
    const requests = stubApi(
      routes(true, {
        'POST /api/commands/plan-rename': () =>
          jsonResponse(201, {
            action: renamed,
            items: [actionItem('1', 'Downloads/setup.exe', 'Downloads/setup 2005.exe')],
            next_cursor: null,
          }),
        'POST /api/commands/run-action': runRoute({ '77': renamed, '78': undo }),
        'GET /api/history/77': () =>
          jsonResponse(200, { ...renamed, state: 'done', undo: { possible: true, reason: null } }),
        'POST /api/commands/plan-undo': () => jsonResponse(201, { action: undo, items: [], next_cursor: null }),
        'GET /api/history/78': () => jsonResponse(200, { ...undo, state: 'done' }),
      }),
    )
    renderApp('/search?entry=12')
    const section = await organizeSection()

    await userEvent.click(section.getByRole('button', { name: 'Rename' }))
    const name = section.getByRole('textbox', { name: 'New name' })
    expect(name).toHaveValue('setup.exe')
    expect(section.getByRole('button', { name: 'Save' })).toBeDisabled()
    await userEvent.clear(name)
    await userEvent.type(name, 'setup 2005.exe')
    await userEvent.click(section.getByRole('button', { name: 'Save' }))

    expect(await section.findByText('Renamed.')).toBeInTheDocument()
    // No preview: the plan and the run were sent back to back.
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    expect(commandNames(requests)).toEqual(['plan-rename', 'run-action'])
    expect(await bodies(requests, 'plan-rename')).toEqual([{ entry_id: '12', name: 'setup 2005.exe' }])
    expect(await bodies(requests, 'run-action')).toEqual([{ action_id: '77' }])
    const keys = new Set<string | null>()
    for (const request of [...commandRequests(requests, 'plan-rename'), ...commandRequests(requests, 'run-action')]) {
      expect(request.headers.get('X-CSRF-Token')).toBe('session-token')
      expect(request.headers.get('Idempotency-Key')).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
      keys.add(request.headers.get('Idempotency-Key'))
    }
    expect(keys.size).toBe(2)
    expect(section.getByRole('link', { name: 'See History' })).toHaveAttribute('href', '/history')
    expect(section.queryByRole('textbox')).not.toBeInTheDocument()

    await userEvent.click(section.getByRole('button', { name: 'Undo' }))
    expect(await section.findByText('Undone.')).toBeInTheDocument()
    expect(await bodies(requests, 'plan-undo')).toEqual([{ action_id: '77' }])
    expect(await bodies(requests, 'run-action')).toEqual([{ action_id: '77' }, { action_id: '78' }])
  })

  it('shows a taken name and runs nothing', async () => {
    const requests = stubApi(
      routes(true, {
        'POST /api/commands/plan-rename': () => errorResponse(409, 'name_taken'),
      }),
    )
    renderApp('/search?entry=12')
    const section = await organizeSection()

    await userEvent.click(section.getByRole('button', { name: 'Rename' }))
    await userEvent.clear(section.getByRole('textbox', { name: 'New name' }))
    await userEvent.type(section.getByRole('textbox', { name: 'New name' }), 'notes.txt')
    await userEvent.click(section.getByRole('button', { name: 'Save' }))

    expect(await section.findByRole('alert')).toHaveTextContent(
      'Something in this folder already has that name. Choose another name.',
    )
    expect(commandNames(requests)).toEqual(['plan-rename'])
    expect(section.getByRole('textbox', { name: 'New name' })).toHaveValue('notes.txt')
  })

  it('previews a single move that would lose a keep, and runs it only after Confirm', async () => {
    const moved = action(
      { id: '80', destination: old, kept_lost: 1 },
      { planned: 1 },
    )
    const requests = stubApi(
      routes(true, {
        'GET /api/entries/5/children': () => jsonResponse(200, { items: [old], next_cursor: null }),
        'GET /api/entries/30/children': () => jsonResponse(200, { items: [], next_cursor: null }),
        'POST /api/commands/plan-move': () =>
          jsonResponse(201, {
            action: moved,
            items: [actionItem('1', 'Downloads/setup.exe', 'Downloads/Old/setup.exe', { decision_after: 'discard' })],
            next_cursor: null,
          }),
        'POST /api/commands/run-action': runRoute({ '80': moved }),
        'GET /api/history/80': () => jsonResponse(200, { ...moved, state: 'done' }),
      }),
    )
    renderApp('/search?entry=12')
    const section = await organizeSection()

    await userEvent.click(section.getByRole('button', { name: 'Move to…' }))
    const chooser = within(await screen.findByRole('dialog', { name: 'Move “setup.exe” to…' }))
    // The chooser opens where the file is, and lists folders only, by name.
    const trail = within(chooser.getByRole('navigation', { name: 'Current folder' }))
    expect(trail.getAllByRole('button').map((b) => b.textContent)).toEqual(['Fotos', 'Downloads'])
    await userEvent.click(await chooser.findByRole('button', { name: 'Old' }))
    expect(await chooser.findByText('This folder has no subfolders.')).toBeInTheDocument()
    expect(chooser.getByText('Destination: Fotos / Downloads / Old')).toBeInTheDocument()
    const listings = requests
      .filter((r) => new URL(r.url).pathname.endsWith('/children'))
      .map((r) => [new URL(r.url).pathname, new URL(r.url).searchParams.get('kind'), new URL(r.url).searchParams.get('sort')])
    expect(listings).toEqual([
      ['/api/entries/5/children', 'directory', 'name'],
      ['/api/entries/30/children', 'directory', 'name'],
    ])
    await userEvent.click(chooser.getByRole('button', { name: 'Move here' }))

    const preview = within(await screen.findByRole('alertdialog', { name: 'Move into “Downloads/Old”' }))
    expect(preview.getByText('1 kept item would no longer be kept.')).toBeInTheDocument()
    expect(preview.getByText('Downloads/setup.exe → Downloads/Old/setup.exe')).toBeInTheDocument()
    expect(preview.getByText('Decision after the move: Discard')).toBeInTheDocument()
    expect(await bodies(requests, 'plan-move')).toEqual([{ entry_id: '12', destination_id: '30' }])
    expect(commandNames(requests)).toEqual(['plan-move'])

    await userEvent.click(preview.getByRole('button', { name: 'Confirm' }))
    expect(await section.findByText('Moved.')).toBeInTheDocument()
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    expect(await bodies(requests, 'run-action')).toEqual([{ action_id: '80' }])
  })

  it('never offers a folder being moved, or one below it, as the destination', async () => {
    const detail = entryDetail(downloads)
    stubApi(
      routes(true, {
        'GET /api/entries/5': () => jsonResponse(200, detail),
        'GET /api/entries/1/children': () => jsonResponse(200, { items: [downloads], next_cursor: null }),
        'GET /api/entries/5/children': () => jsonResponse(200, { items: [old], next_cursor: null }),
        'GET /api/entries/30/children': () => jsonResponse(200, { items: [], next_cursor: null }),
      }),
    )
    renderApp('/search?entry=5')
    const panel = within(await screen.findByRole('complementary', { name: 'Downloads' }))
    const section = within(await panel.findByRole('region', { name: 'Organize' }))
    expect(section.getByRole('button', { name: 'New folder' })).toBeInTheDocument()

    await userEvent.click(section.getByRole('button', { name: 'Move to…' }))
    const chooser = within(await screen.findByRole('dialog', { name: 'Move “Downloads” to…' }))
    await userEvent.click(await chooser.findByRole('button', { name: 'Downloads' }))
    expect(chooser.getByRole('button', { name: 'Move here' })).toBeDisabled()
    await userEvent.click(await chooser.findByRole('button', { name: 'Old' }))
    expect(chooser.getByRole('button', { name: 'Move here' })).toBeDisabled()
    expect(chooser.getByText('Choose a folder outside this one.')).toBeInTheDocument()
    await userEvent.click(within(chooser.getByRole('navigation', { name: 'Current folder' })).getByRole('button', { name: 'Fotos' }))
    await waitFor(() => expect(chooser.getByRole('button', { name: 'Move here' })).toBeEnabled())
  })

  it('points to the Sources screen while changes are off, with no control', async () => {
    const requests = stubApi(routes(false))
    renderApp('/search?entry=12')
    const section = await organizeSection()

    expect(section.getByRole('link', { name: 'Sources' })).toHaveAttribute('href', '/sources')
    expect(section.queryByRole('button')).not.toBeInTheDocument()
    expect(commandNames(requests)).toEqual([])
  })
})
