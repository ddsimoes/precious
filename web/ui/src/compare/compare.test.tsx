import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import type { Bucket, CompareItem } from '@/api/compare'
import type { EntryRow } from '@/api/entries'
import { entryDetail, entryRow, folderRow, fotosSource } from '@/test/fixtures'
import { errorResponse, jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'

const MiB = 1024 ** 2

const fotos = folderRow('2', 'Fotos')
const copia = folderRow('5', 'Fotos - Copia')
const editada = entryRow({
  id: '51',
  name: 'DSC_editada.JPG',
  path: 'Fotos - Copia/2006/Praia/DSC_editada.JPG',
  file_kind: 'image',
  size: 2 * MiB,
})
const praia = entryRow({ id: '21', name: 'DSC01.JPG', path: 'Fotos/2006/Praia/DSC01.JPG', size: 3 * MiB })
const praiaCopy = entryRow({ id: '52', name: 'DSC01.JPG', path: 'Fotos - Copia/2006/Praia/DSC01.JPG', size: 3 * MiB })

const summary = {
  only_left: { files: 0, bytes: 0 },
  only_right: { files: 1, bytes: 2 * MiB },
  identical: { files: 300, bytes: 900 * MiB },
  different: { files: 0, bytes: 0 },
  unchecked: { files: 4, bytes: 12 * MiB },
}

function item(path: string, left: EntryRow | null, right: EntryRow | null, overrides: Partial<CompareItem> = {}): CompareItem {
  return {
    path,
    path_b64: btoa(path),
    left_path: left === null ? null : path,
    right_path: right === null ? null : path,
    left,
    right,
    twin: null,
    ...overrides,
  }
}

// opening is the server's order for a comparison opened without a group.
const opening: Bucket[] = ['only_left', 'only_right', 'different', 'unchecked', 'identical']

const groups: Partial<Record<Bucket, CompareItem[]>> = {
  only_right: [item('2006/Praia/DSC_editada.JPG', null, editada)],
  identical: [item('2006/Praia/DSC01.JPG', praia, praiaCopy)],
}

const base = {
  'GET /api/session': () => jsonResponse(200, signedIn),
  'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource()] }),
  'GET /api/tags': () => jsonResponse(200, { tags: [] }),
}

// compareRoute serves Compare as the server does: the group asked for, or
// without one the first holding files, named in bucket.
function compareRoute(decided: Map<string, string> = new Map(), sums: typeof summary = summary) {
  return (request: Request) => {
    const params = new URL(request.url).searchParams
    const bucket = (params.get('bucket') as Bucket | null) ?? opening.find((b) => sums[b].files > 0) ?? 'only_left'
    const items = (groups[bucket] ?? []).map((i) => ({
      ...i,
      left: i.left,
      right: i.right === null ? null : { ...i.right, eff_decision: decided.get(i.right.id) ?? 'undecided' },
    }))
    return jsonResponse(200, { left: fotos, right: copia, summary: sums, bucket, items, next_cursor: null })
  }
}

function compareRequests(requests: Request[]) {
  return requests
    .filter((r) => new URL(r.url).pathname === '/api/compare')
    .map((r) => Object.fromEntries(new URL(r.url).searchParams))
}

function commandBodies(requests: Request[], name: string) {
  return Promise.all(
    requests
      .filter((r) => r.method === 'POST' && new URL(r.url).pathname === `/api/commands/${name}`)
      .map((r) => r.clone().json() as Promise<unknown>),
  )
}

describe('Compare', () => {
  it('opens on the first group with files, and keeps its sides and group in the address', async () => {
    const requests = stubApi({ ...base, 'GET /api/compare': compareRoute() })
    const { router } = renderApp('/compare?left=2&right=5')
    const user = userEvent.setup()

    // Nothing is only on the left of this pair: the server opens it on the
    // right, in the one request that computes the comparison.
    const files = within(await screen.findByRole('list', { name: 'Files: Only on the right' }))
    expect(files.getByRole('link', { name: '2006/Praia/DSC_editada.JPG' })).toBeInTheDocument()
    expect(router.state.location.search).toBe('?left=2&right=5&bucket=only_right')
    expect(router.state.historyAction).toBe('REPLACE')
    expect(compareRequests(requests)).toEqual([{ left: '2', right: '5' }])
    const sides = within(screen.getByRole('region', { name: 'Folders compared' }))
    expect(sides.getByText('Fotos')).toBeInTheDocument()
    expect(sides.getByText('Fotos - Copia')).toBeInTheDocument()
    const nav = within(screen.getByRole('navigation', { name: 'Groups' }))
    expect(nav.getAllByRole('link').map((link) => link.textContent.replace(/\s+/g, ' '))).toEqual([
      'Only on the left0 files · 0 B',
      'Only on the right1 file · 2 MiB',
      'Identical300 files · 900 MiB',
      'Same name, different content0 files · 0 B',
      'Not checked yet4 files · 12 MiB',
    ])
    expect(nav.getByRole('link', { name: /Only on the right/ })).toHaveAttribute('aria-current', 'page')

    await user.click(nav.getByRole('link', { name: /Only on the left/ }))
    expect(await screen.findByText('No files in this group.')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /Only on the left/ })).toHaveAttribute('aria-current', 'page')
    expect(router.state.location.search).toBe('?left=2&right=5&bucket=only_left')
    expect(compareRequests(requests)).toEqual([
      { left: '2', right: '5' },
      { left: '2', right: '5', bucket: 'only_left' },
    ])
  })

  it('loads more of the group it opened on', async () => {
    const more = entryRow({ id: '53', name: 'DSC02.JPG', path: 'Fotos - Copia/2006/Praia/DSC02.JPG', size: MiB })
    const requests = stubApi({
      ...base,
      'GET /api/compare': (request) => {
        const params = new URL(request.url).searchParams
        const next = params.get('cursor') === null
        return jsonResponse(200, {
          left: fotos,
          right: copia,
          summary: { ...summary, only_right: { files: 2, bytes: 3 * MiB } },
          bucket: 'only_right',
          items: next ? groups.only_right : [item('2006/Praia/DSC02.JPG', null, more)],
          next_cursor: next ? '1' : null,
        })
      },
    })
    renderApp('/compare?left=2&right=5')
    const user = userEvent.setup()

    await screen.findByRole('list', { name: 'Files: Only on the right' })
    await user.click(screen.getByRole('button', { name: 'Load more' }))
    expect(await screen.findByRole('link', { name: '2006/Praia/DSC02.JPG' })).toBeInTheDocument()
    expect(compareRequests(requests)).toEqual([
      { left: '2', right: '5' },
      { left: '2', right: '5', bucket: 'only_right', cursor: '1' },
    ])
  })

  it('opens a pair with the same content on its identical files', async () => {
    const same = { ...summary, only_right: { files: 0, bytes: 0 }, unchecked: { files: 0, bytes: 0 } }
    stubApi({ ...base, 'GET /api/compare': compareRoute(new Map(), same) })
    const { router } = renderApp('/compare?left=2&right=5')

    expect(await screen.findByRole('list', { name: 'Files: Identical' })).toBeInTheDocument()
    expect(router.state.location.search).toBe('?left=2&right=5&bucket=identical')
  })

  it('shows the same comparison when the page is loaded again', async () => {
    const requests = stubApi({ ...base, 'GET /api/compare': compareRoute() })
    renderApp('/compare?left=2&right=5&bucket=identical')

    const files = within(await screen.findByRole('list', { name: 'Files: Identical' }))
    const row = files.getByRole('link', { name: '2006/Praia/DSC01.JPG' }).closest('li')!
    expect(within(row).getByRole('group', { name: `Decision for Left: ${praia.path}` })).toBeInTheDocument()
    expect(within(row).getByRole('group', { name: `Decision for Right: ${praiaCopy.path}` })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /Identical/ })).toHaveAttribute('aria-current', 'page')
    expect(compareRequests(requests)).toEqual([{ left: '2', right: '5', bucket: 'identical' }])
  })

  it('shows where each copy is, and the file an extra copy is the same as', async () => {
    const left = entryRow({ id: '31', name: 'img_0001.jpg', path: 'fotos-b/2002/12/img_0001.jpg', size: MiB })
    const right = entryRow({ id: '61', name: 'IMG_0001.jpg', path: 'fotos/2014/celular/IMG_0001.jpg', size: MiB })
    const extra = entryRow({ id: '62', name: 'IMG_0001 (1).jpg', path: 'fotos/2015/IMG_0001 (1).jpg', size: MiB })
    stubApi({
      ...base,
      'GET /api/compare': () =>
        jsonResponse(200, {
          left: folderRow('3', 'fotos-b'),
          right: folderRow('6', 'fotos'),
          summary: { ...summary, identical: { files: 2, bytes: 2 * MiB } },
          bucket: 'identical',
          items: [
            item('2002/12/img_0001.jpg', left, right, { right_path: '2014/celular/IMG_0001.jpg' }),
            item('2015/IMG_0001 (1).jpg', null, extra, {
              twin: { path: '2002/12/img_0001.jpg', entry: left },
            }),
          ],
          next_cursor: null,
        }),
    })
    renderApp('/compare?left=3&right=6&bucket=identical')

    const list = await screen.findByRole('list', { name: 'Files: Identical' })
    const [pair, copy] = Array.from(list.children) as HTMLElement[]
    // The pair: each side's path inside its side, as they differ.
    expect(within(pair!).getByText('2014/celular/IMG_0001.jpg')).toBeInTheDocument()
    expect(within(pair!).getAllByText('2002/12/img_0001.jpg')).toHaveLength(2) // the item, and its left path
    expect(within(pair!).queryByText(/Extra copy/)).not.toBeInTheDocument()
    // The extra copy names the left file holding its content.
    expect(copy).toHaveTextContent('Extra copy, same as 2002/12/img_0001.jpg on the left')
    expect(within(copy!).getByRole('link', { name: '2002/12/img_0001.jpg' })).toHaveAttribute(
      'href',
      '/compare?left=3&right=6&bucket=identical&entry=31',
    )
  })

  it('decides a file only on the right, and checks the two folders first', async () => {
    const decided = new Map<string, string>()
    const requests = stubApi({
      ...base,
      'GET /api/compare': compareRoute(decided),
      'POST /api/commands/set-decision': async (request) => {
        const body = (await request.clone().json()) as { entry_id: string; decision: string }
        decided.set(body.entry_id, body.decision)
        return jsonResponse(200, { applied: 1, skipped_count: 0, skipped: [] })
      },
      'POST /api/commands/check-now': () =>
        jsonResponse(202, {
          jobs: [{ job_id: '70', state: 'queued', coalesced: false }],
        }),
    })
    renderApp('/compare?left=2&right=5&bucket=only_right')
    const user = userEvent.setup()

    const files = within(await screen.findByRole('list', { name: 'Files: Only on the right' }))
    const row = files.getByRole('link', { name: '2006/Praia/DSC_editada.JPG' }).closest('li')!
    expect(row).toHaveTextContent('2 MiB · Decision: Undecided')
    await user.click(
      within(within(row).getByRole('group', { name: `Decision for Right: ${editada.path}` })).getByRole('button', {
        name: 'Discard',
      }),
    )
    expect(await commandBodies(requests, 'set-decision')).toEqual([{ entry_id: '51', decision: 'discard' }])
    await waitFor(() => expect(screen.getByText(/Decision: Discard/)).toBeInTheDocument())

    await user.click(screen.getByRole('button', { name: 'Check now' }))
    expect(await screen.findByText(/These folders are checked first/)).toBeInTheDocument()
    expect(await commandBodies(requests, 'check-now')).toEqual([{ entry_ids: ['2', '5'] }])
  })

  it('says when the two cannot be compared', async () => {
    stubApi({ ...base, 'GET /api/compare': () => errorResponse(400, 'invalid_request') })
    renderApp('/compare?left=2&right=21')
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'These two cannot be compared: one is inside the other, or one is a file.',
    )
  })

  it('explains how to start without two sides', async () => {
    stubApi(base)
    renderApp('/compare')
    expect(await screen.findByText(/Choose two folders to compare/)).toBeInTheDocument()
  })

  it('compares two folders chosen in their detail panels', async () => {
    stubApi({
      ...base,
      'GET /api/search': () => jsonResponse(200, { items: [fotos, copia], next_cursor: null, count: 2 }),
      'GET /api/entries/2': () => jsonResponse(200, entryDetail(fotos)),
      'GET /api/entries/5': () => jsonResponse(200, entryDetail(copia)),
      'GET /api/compare': compareRoute(),
    })
    const { router } = renderApp('/search?name=fotos&entry=2')
    const user = userEvent.setup()

    let panel = within(await screen.findByRole('complementary', { name: 'Fotos' }))
    await user.click(await panel.findByRole('button', { name: 'Compare with…' }))
    expect(panel.getByRole('status')).toHaveTextContent('Chosen for Compare.')
    expect(panel.queryByRole('link', { name: /^Compare with/ })).not.toBeInTheDocument()

    await user.click(screen.getByRole('link', { name: 'Details of Fotos - Copia' }))
    panel = within(await screen.findByRole('complementary', { name: 'Fotos - Copia' }))
    const compare = await panel.findByRole('link', { name: 'Compare with Fotos' })
    expect(compare).toHaveAttribute('href', '/compare?left=2&right=5')
    await user.click(compare)

    expect(await screen.findByRole('region', { name: 'Folders compared' })).toBeInTheDocument()
    expect(router.state.location.pathname).toBe('/compare')
    await waitFor(() => expect(router.state.location.search).toBe('?left=2&right=5&bucket=only_right'))
  })

  it('offers no Compare with… for a file', async () => {
    stubApi({
      ...base,
      'GET /api/search': () => jsonResponse(200, { items: [praia], next_cursor: null, count: 1 }),
      'GET /api/entries/21': () => jsonResponse(200, entryDetail(praia)),
    })
    renderApp('/search?entry=21')
    const panel = within(await screen.findByRole('complementary', { name: 'DSC01.JPG' }))
    await panel.findByRole('button', { name: 'Open' })
    expect(panel.queryByRole('button', { name: 'Compare with…' })).not.toBeInTheDocument()
  })
})
