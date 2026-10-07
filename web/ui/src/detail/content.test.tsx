import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import type { EntryDetail } from '@/api/entries'
import { copyOf, entryDetail, entryRow, folderRow, fotosSource, relationTo } from '@/test/fixtures'
import { jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'

const GiB = 1024 ** 3

const curriculo = entryRow({ id: '8', name: 'curriculo.doc', path: 'Documentos/curriculo.doc', content_state: 'hashed', copies: 3 })
const copy1 = entryRow({ id: '81', path: 'Backup/curriculo.doc', eff_decision: 'discard' })
const copy2 = entryRow({ id: '82', path: 'Pendrive/curriculo.doc' })

function routes(detail: EntryDetail, extra: Record<string, () => Response> = {}) {
  return {
    'GET /api/session': () => jsonResponse(200, signedIn),
    'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource()] }),
    'GET /api/tags': () => jsonResponse(200, { tags: [] }),
    [`GET /api/entries/${detail.entry.id}`]: () => jsonResponse(200, detail),
    'GET /api/search': () => jsonResponse(200, { items: [], next_cursor: null, count: 0 }),
    ...extra,
  }
}

describe('Detail panel duplicates', () => {
  it('lists a file’s other copies with the checked share, and its SHA-256 among the technical details', async () => {
    const sha = 'ab'.repeat(32)
    stubApi(
      routes(
        entryDetail(curriculo, {
          content: {
            state: 'hashed',
            sha256: sha,
            checked_at: null,
            copies: [copyOf(curriculo), copyOf(copy1), copyOf(copy2)],
            copies_count: 3,
          },
        }),
      ),
    )
    renderApp('/search?entry=8')
    const user = userEvent.setup()

    const copies = within(await screen.findByRole('region', { name: 'Copies' }))
    expect(copies.getByText('2 other copies:')).toBeInTheDocument()
    const list = within(copies.getByRole('list', { name: 'Other copies' }))
    expect(list.getAllByRole('link').map((link) => [link.textContent, link.getAttribute('href')])).toEqual([
      ['Backup/curriculo.doc', '/search?entry=81'],
      ['Pendrive/curriculo.doc', '/search?entry=82'],
    ])
    expect(list.getAllByRole('listitem')[0]).toHaveTextContent(/· Decision: Discard$/)
    expect(copies.getByText('On all disks, 89% of what could have a copy is checked.')).toBeInTheDocument()

    expect(screen.getByText(sha)).not.toBeVisible()
    await user.click(screen.getByText('Technical details'))
    expect(screen.getByText(sha)).toBeVisible()
    expect(screen.getByText('Read in full')).toBeVisible()
  })

  it.each([
    ['unique_size', 'No other copy: no other file has this size.'],
    ['sampled', 'No other copy: it differs from every other file of its size.'],
    ['pending', 'Not checked for copies yet.'],
  ] as const)('words a %s file’s claim with the checked share', async (state, text) => {
    const file = entryRow({ id: '9', content_state: state, copies: state === 'pending' ? null : 1 })
    stubApi(
      routes(entryDetail(file, { content: { state, sha256: null, checked_at: null, copies: [], copies_count: 0 } })),
    )
    renderApp('/search?entry=9')
    const copies = within(await screen.findByRole('region', { name: 'Copies' }))
    expect(copies.getByText(text)).toBeInTheDocument()
    expect(copies.getByText(/of what could have a copy is checked/)).toBeInTheDocument()
  })

  it('shows a folder’s relations, each opening Compare, and its percent duplicated', async () => {
    const emule = folderRow('60', 'emule-0.47c', {
      path: 'Downloads/emule-0.47c',
      total_bytes: 10 * GiB,
      candidate_bytes: 10 * GiB,
      checked_bytes: 10 * GiB,
      duplicated_bytes: 10 * GiB,
    })
    const zip = entryRow({ id: '61', path: 'Downloads/eMule0.47c-Installer.zip', archive_state: 'complete' })
    stubApi(routes(entryDetail(emule, { relations: [relationTo(zip, { self: 'b' })] })))
    renderApp('/search?entry=60')

    const relations = within(await screen.findByRole('region', { name: 'Related folders' }))
    const item = relations.getAllByRole('listitem')[0]!
    expect(item).toHaveTextContent('Same content as Downloads/eMule0.47c-Installer.zip')
    expect(within(item).getByRole('link', { name: zip.path })).toHaveAttribute('href', '/search?entry=61')
    expect(within(item).getByRole('link', { name: 'Compare' })).toHaveAttribute('href', '/compare?left=60&right=61')
    expect(screen.getByText('100% (10 GiB)')).toBeInTheDocument()
  })

  it.each([
    ['a', 'Most of it is also in Fotos'],
    ['b', 'Most of Fotos is also in it'],
  ] as const)('words an overlap from side %s', async (self, text) => {
    const copia = folderRow('70', 'Fotos - Copia')
    const fotos = folderRow('71', 'Fotos')
    stubApi(routes(entryDetail(copia, { relations: [relationTo(fotos, { kind: 'overlap', self })] })))
    renderApp('/search?entry=70')

    const relations = within(await screen.findByRole('region', { name: 'Related folders' }))
    expect(relations.getAllByRole('listitem')[0]).toHaveTextContent(text)
  })
})

describe('Search duplicate filter', () => {
  it('searches by duplicate state, with copies outside only inside a folder', async () => {
    const docs = folderRow('5', 'Fotos - Copia')
    const requests = stubApi(routes(entryDetail(docs)))
    const { router } = renderApp('/search?within=5')
    const user = userEvent.setup()

    await user.click(await screen.findByText('Copies', { selector: 'summary' }))
    await user.click(screen.getByRole('checkbox', { name: 'Has a copy outside this folder' }))
    await user.click(screen.getByRole('button', { name: 'Search' }))
    expect(new URLSearchParams(router.state.location.search).getAll('dup')).toEqual(['elsewhere'])
    expect(requests.some((r) => new URL(r.url).searchParams.get('dup') === 'elsewhere')).toBe(true)

    // Without a folder, "outside this folder" is not offered, and leaving the
    // folder drops it.
    await user.click(screen.getByRole('button', { name: 'Search everywhere' }))
    expect(router.state.location.search).toBe('')
    await user.click(screen.getByText('Copies', { selector: 'summary' }))
    expect(screen.queryByRole('checkbox', { name: 'Has a copy outside this folder' })).not.toBeInTheDocument()
    expect(screen.getByRole('checkbox', { name: 'No other copy' })).toBeInTheDocument()
  })
})
