import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import type { Bucket, CompareItem } from '@/api/compare'
import type { EntryRow } from '@/api/entries'
import { action, actionItem, entryRow, folderRow, fotosSource } from '@/test/fixtures'
import { jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'

type Route = (request: Request) => Response | Promise<Response>

const MiB = 1024 ** 2

const fotos = folderRow('2', 'Fotos')
const copia = folderRow('5', 'Fotos - Copia')
const editada = entryRow({ id: '51', name: 'DSC_editada.JPG', path: 'Fotos - Copia/2006/Praia/DSC_editada.JPG' })
const original = entryRow({ id: '21', name: 'DSC_0001.JPG', path: 'Fotos/2006/DSC_0001.JPG' })
const praia = entryRow({ id: '22', name: 'DSC01.JPG', path: 'Fotos/2006/Praia/DSC01.JPG' })
const praiaCopy = entryRow({ id: '52', name: 'DSC01.JPG', path: 'Fotos - Copia/2006/Praia/DSC01.JPG' })

function item(path: string, left: EntryRow | null, right: EntryRow | null): CompareItem {
  return { path, path_b64: btoa(path), left_path: path, right_path: path, left, right, twin: null }
}

const groups: Partial<Record<Bucket, CompareItem[]>> = {
  only_left: [item('2006/DSC_0001.JPG', original, null)],
  only_right: [item('2006/Praia/DSC_editada.JPG', null, editada)],
  identical: [item('2006/Praia/DSC01.JPG', praia, praiaCopy)],
}

const summary = {
  only_left: { files: 1, bytes: 3 * MiB },
  only_right: { files: 1, bytes: 2 * MiB },
  identical: { files: 300, bytes: 900 * MiB },
  different: { files: 0, bytes: 0 },
  unchecked: { files: 0, bytes: 0 },
}

function routes(writes: boolean, left: EntryRow = fotos, extra: Record<string, Route> = {}): Record<string, Route> {
  return {
    'GET /api/session': () => jsonResponse(200, signedIn),
    'GET /api/sources': () =>
      jsonResponse(200, { sources: [fotosSource({ writes: { enabled: writes, unavailable: null } })] }),
    'GET /api/tags': () => jsonResponse(200, { tags: [] }),
    'GET /api/compare': (request) => {
      const bucket = (new URL(request.url).searchParams.get('bucket') as Bucket | null) ?? 'only_left'
      return jsonResponse(200, { left, right: copia, summary, bucket, items: groups[bucket] ?? [], next_cursor: null })
    },
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

async function openGroup(name: string) {
  await userEvent.click(within(screen.getByRole('navigation', { name: 'Groups' })).getByRole('link', { name: new RegExp(`^${name}`) }))
}

describe('Moving the files only on one side in Compare', () => {
  it('is offered only on the two only-on-one-side groups', async () => {
    stubApi(routes(true))
    renderApp('/compare?left=2&right=5&bucket=only_left')

    expect(await screen.findByRole('button', { name: 'Move these files into “Fotos - Copia”' })).toBeInTheDocument()

    await openGroup('Only on the right')
    expect(await screen.findByRole('button', { name: 'Move these files into “Fotos”' })).toBeInTheDocument()

    await openGroup('Identical')
    await screen.findByRole('list', { name: 'Files: Identical' })
    expect(screen.queryByRole('button', { name: /^Move these files/ })).not.toBeInTheDocument()

    await openGroup('Same name, different content')
    await screen.findByText('No files in this group.')
    expect(screen.queryByRole('button', { name: /^Move these files/ })).not.toBeInTheDocument()
  })

  it('is not offered where Precious may not change the source', async () => {
    stubApi(routes(false))
    renderApp('/compare?left=2&right=5&bucket=only_left')
    await screen.findByRole('list', { name: 'Files: Only on the left' })
    expect(screen.queryByRole('button', { name: /^Move these files/ })).not.toBeInTheDocument()
  })

  it('is not offered when a side is an archive', async () => {
    const zip = entryRow({ id: '2', name: 'Fotos.zip', path: 'Fotos.zip', archive_state: 'complete' })
    stubApi(routes(true, zip))
    renderApp('/compare?left=2&right=5&bucket=only_left')
    await screen.findByRole('list', { name: 'Files: Only on the left' })
    expect(screen.queryByRole('button', { name: /^Move these files/ })).not.toBeInTheDocument()
  })

  it('plans the merge, previews it, and runs it after Confirm', async () => {
    const merge = action(
      { id: '60', kind: 'merge', bulk: true, destination: fotos, files: 1, bytes: 2 * MiB },
      { planned: 2 },
    )
    const requests = stubApi(
      routes(true, fotos, {
        'POST /api/commands/plan-merge': () =>
          jsonResponse(201, {
            action: merge,
            items: [
              actionItem('1', '', 'Fotos/2006/Praia', { op: 'mkdir', from: null }),
              actionItem('2', 'Fotos - Copia/2006/Praia/DSC_editada.JPG', 'Fotos/2006/Praia/DSC_editada.JPG'),
            ],
            next_cursor: null,
          }),
        'POST /api/commands/run-action': () =>
          jsonResponse(202, { action: { ...merge, state: 'queued' }, job_id: '9', state: 'queued' }),
        'GET /api/history/60': () => jsonResponse(200, { ...merge, state: 'running' }),
      }),
    )
    renderApp('/compare?left=2&right=5&bucket=only_right')

    await userEvent.click(await screen.findByRole('button', { name: 'Move these files into “Fotos”' }))
    const preview = within(
      await screen.findByRole('alertdialog', { name: 'Move the files only on one side into “Fotos”' }),
    )
    expect(preview.getByText('New folder: Fotos/2006/Praia')).toBeInTheDocument()
    expect(
      preview.getByText('Fotos - Copia/2006/Praia/DSC_editada.JPG → Fotos/2006/Praia/DSC_editada.JPG'),
    ).toBeInTheDocument()
    expect(await bodies(requests, 'plan-merge')).toEqual([{ left_id: '2', right_id: '5', from: 'right' }])
    expect(await bodies(requests, 'run-action')).toEqual([])

    await userEvent.click(preview.getByRole('button', { name: 'Confirm' }))
    expect(await screen.findByText('Moving the files…')).toBeInTheDocument()
    await waitFor(async () => expect(await bodies(requests, 'run-action')).toEqual([{ action_id: '60' }]))
  })
})
