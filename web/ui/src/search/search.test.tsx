import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import type { EntryRow } from '@/api/entries'
import type { SearchCount } from '@/api/search'
import { rememberSource } from '@/app/sourceChoice'
import { entryDetail, entryRow, folderRow, fotosSource, usbSource } from '@/test/fixtures'
import { errorResponse, jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'

const GiB = 1024 ** 3

const tmpFiles = Array.from({ length: 5 }, (_, i) =>
  entryRow({ id: String(300 + i), name: `f${i}.tmp`, path: `Backup_PC_2004/f${i}.tmp`, total_bytes: 1000 + i }),
)

// searchRoute answers a page of items, and the count for count=only.
function searchRoute(items: EntryRow[], count: SearchCount | Promise<SearchCount>) {
  return async (request: Request) =>
    new URL(request.url).searchParams.get('count') === 'only'
      ? jsonResponse(200, { count: await count })
      : jsonResponse(200, { items, next_cursor: null })
}

function routes(
  count: SearchCount = 1_200,
  extra: Record<string, (request: Request) => Response | Promise<Response>> = {},
) {
  return {
    'GET /api/session': () => jsonResponse(200, signedIn),
    'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource(), usbSource()] }),
    'GET /api/tags': () =>
      jsonResponse(200, {
        tags: [
          { id: 9, name: 'familia', own_count: 12 },
          { id: 10, name: 'scan', own_count: 0 },
        ],
      }),
    'GET /api/search': searchRoute(tmpFiles, count),
    'GET /api/entries/5': () =>
      jsonResponse(200, entryDetail(folderRow('5', 'Backup_PC_2004', { path: 'Backup_PC_2004' }))),
    ...extra,
  }
}

// searches lists the parameters of the page requests; counts, those of the
// count requests.
function searches(requests: Request[]) {
  return requests
    .filter((r) => new URL(r.url).pathname === '/api/search')
    .map((r) => new URL(r.url).searchParams)
    .filter((p) => !p.has('count'))
}

function counts(requests: Request[]) {
  return requests
    .filter((r) => new URL(r.url).pathname === '/api/search')
    .map((r) => new URL(r.url).searchParams)
    .filter((p) => p.get('count') === 'only')
}

function rowOf(name: string) {
  const row = screen.getByRole('link', { name }).closest('[role="row"]')
  if (!(row instanceof HTMLElement)) {
    throw new Error(`no row for ${name}`)
  }
  return row
}

function cellIn(row: HTMLElement, column: string) {
  const headers = screen.getAllByRole('columnheader').map((header) => header.textContent)
  const cell = within(row).getAllByRole('cell')[headers.indexOf(column)]
  if (cell === undefined) {
    throw new Error(`no ${column} cell`)
  }
  return cell
}

function cellOf(name: string, column: string) {
  return cellIn(rowOf(name), column)
}

function commandBodies(requests: Request[], name: string) {
  return Promise.all(
    requests
      .filter((r) => r.method === 'POST' && new URL(r.url).pathname === `/api/commands/${name}`)
      .map((r) => r.clone().json() as Promise<unknown>),
  )
}

describe('Search screen', () => {
  it('keeps the filters in the address and sends them to the server', async () => {
    const requests = stubApi(routes())
    const { router } = renderApp('/search')
    const form = within(await screen.findByRole('form', { name: 'Filters' }))

    await userEvent.type(form.getByRole('searchbox', { name: 'Name contains' }), 'natal')
    await userEvent.type(form.getByRole('textbox', { name: 'Extensions' }), '.JPG, png')
    await userEvent.click(form.getByText('File type', { selector: 'summary' }))
    await userEvent.click(form.getByRole('checkbox', { name: 'Image' }))
    await userEvent.type(form.getByRole('spinbutton', { name: 'At least' }), '1.5')
    await userEvent.type(form.getByRole('spinbutton', { name: 'At most' }), '2')
    await userEvent.selectOptions(form.getByRole('combobox', { name: 'At most: Unit' }), 'GiB')
    await userEvent.type(form.getByRole('spinbutton', { name: 'From' }), '2004')
    await userEvent.type(form.getByRole('spinbutton', { name: 'To' }), '2006')
    await userEvent.click(form.getByText('Category', { selector: 'summary' }))
    await userEvent.click(form.getByRole('checkbox', { name: 'Personal media' }))
    await userEvent.click(form.getByText('Decision', { selector: 'summary' }))
    await userEvent.click(within(form.getByRole('group', { name: 'Decision' })).getByRole('checkbox', { name: 'Keep' }))
    await userEvent.click(form.getByText('Suggestion', { selector: 'summary' }))
    await userEvent.click(within(form.getByRole('group', { name: 'Suggestion' })).getByRole('checkbox', { name: 'Review' }))
    await userEvent.click(form.getByText('Tags', { selector: 'summary' }))
    await userEvent.click(await form.findByRole('checkbox', { name: 'familia' }))
    await userEvent.click(form.getByRole('button', { name: 'Search' }))

    const expected =
      'name=natal&min_size=1572864&max_size=2147483648&year_from=2004&year_to=2006&ext=jpg&ext=png&file_kind=image&category=personal_media&tag=9&decision=keep&triage=review'
    await waitFor(() => expect(router.state.location.search).toBe(`?${expected}`))
    await waitFor(() => expect(searches(requests).at(-1)?.toString()).toBe(expected))

    // The address alone restores the form, which a new search mounts afresh.
    const restored = within(screen.getByRole('form', { name: 'Filters' }))
    expect(restored.getByRole('searchbox', { name: 'Name contains' })).toHaveValue('natal')
    expect(restored.getByRole('textbox', { name: 'Extensions' })).toHaveValue('jpg png')
    expect(restored.getByRole('spinbutton', { name: 'At least' })).toHaveValue(1.5)
    expect(restored.getByRole('combobox', { name: 'At least: Unit' })).toHaveValue('MiB')
    expect(restored.getByRole('spinbutton', { name: 'At most' })).toHaveValue(2)
    expect(restored.getByRole('combobox', { name: 'At most: Unit' })).toHaveValue('GiB')
    expect(restored.getByText('Tags (1)')).toBeInTheDocument()
    expect(restored.getByRole('checkbox', { name: 'Review', hidden: true })).toBeChecked()
  })

  it('shows the count exactly up to 10,000 and as more than 10,000 beyond', async () => {
    stubApi(routes('10000+'))
    renderApp('/search?name=tmp')
    expect(await screen.findByText('More than 10,000 results')).toBeInTheDocument()
  })

  it('shows an exact count', async () => {
    stubApi(routes(9_500))
    renderApp('/search?name=tmp')
    expect(await screen.findByText('9,500 results')).toBeInTheDocument()
  })

  it('says it is counting until the count arrives', async () => {
    let answer: (count: SearchCount) => void = () => {}
    const count = new Promise<SearchCount>((resolve) => {
      answer = resolve
    })
    const requests = stubApi(routes(0, { 'GET /api/search': searchRoute(tmpFiles, count) }))
    renderApp('/search?name=tmp')
    expect(await screen.findByRole('link', { name: 'f0.tmp' })).toBeInTheDocument()
    expect(screen.getByText('Counting…')).toBeInTheDocument()

    answer(1_200)
    expect(await screen.findByText('1,200 results')).toBeInTheDocument()
    expect(screen.queryByText('Counting…')).not.toBeInTheDocument()
    expect(searches(requests).map(String)).toEqual(['name=tmp'])
    expect(counts(requests).map(String)).toEqual(['name=tmp&count=only'])
  })

  it('finds what could not be read, and selects it all', async () => {
    const requests = stubApi(
      routes(5, {
        'POST /api/commands/create-selection': () =>
          jsonResponse(201, {
            selection_id: 'sel-4',
            count: 5,
            bytes: 0,
            kept: { count: 0, bytes: 0 },
            expires_at: '2026-10-05T18:00:00Z',
          }),
      }),
    )
    const { router } = renderApp('/search')
    const form = within(await screen.findByRole('form', { name: 'Filters' }))
    await userEvent.click(form.getByRole('checkbox', { name: 'Could not be read' }))
    await userEvent.click(form.getByRole('button', { name: 'Search' }))

    await waitFor(() => expect(router.state.location.search).toBe('?state=unreadable'))
    await waitFor(() => expect(searches(requests).at(-1)?.toString()).toBe('state=unreadable'))
    await waitFor(() => expect(counts(requests).at(-1)?.toString()).toBe('state=unreadable&count=only'))
    const filters = within(screen.getByRole('form', { name: 'Filters' }))
    expect(filters.getByRole('checkbox', { name: 'Could not be read' })).toBeChecked()

    await userEvent.click(await screen.findByRole('button', { name: 'Select all results' }))
    expect(await commandBodies(requests, 'create-selection')).toEqual([{ query: { state: 'unreadable' } }])
  })

  it('marks the entries that could not be read, with no figures', async () => {
    const locked = folderRow('40', 'Sem acesso', {
      path: 'Backup_PC_2004/Sem acesso',
      state: 'unreadable',
      total_bytes: 0,
      total_files: 0,
      duplicated_bytes: 0,
    })
    const lockedFile = entryRow({
      id: '41',
      name: 'bloqueado.doc',
      path: 'Backup_PC_2004/bloqueado.doc',
      state: 'unreadable',
      total_bytes: 0,
    })
    stubApi(routes(2, { 'GET /api/search': searchRoute([locked, lockedFile], 2) }))
    renderApp('/search?state=unreadable')
    await screen.findByText('2 results')

    for (const name of ['Sem acesso', 'bloqueado.doc']) {
      expect(cellOf(name, 'Type or category')).toHaveTextContent('Could not be read')
      expect(cellOf(name, 'Size▼')).toHaveTextContent('—')
      expect(cellOf(name, 'Has copies')).toHaveTextContent('—')
      expect(rowOf(name)).not.toHaveTextContent(/0\sB|0%/)
    }
    expect(cellOf('Sem acesso', 'Files')).toHaveTextContent('—')
  })

  it('shows the folder and copies of each result, after its source across sources', async () => {
    const movs = ['Fotos/2004', 'Backup_PC_2004/Meus documentos/Videos', 'Celular'].map((folder, i) =>
      entryRow({
        id: String(60 + i),
        name: 'MOV_0195.mp4',
        path: `${folder}/MOV_0195.mp4`,
        file_kind: 'video',
        content_state: 'hashed',
        copies: 3,
      }),
    )
    const notas = entryRow({
      id: '64',
      name: 'notas.txt',
      path: 'notas.txt',
      source_id: 'old-disk',
      content_state: 'hashed',
      copies: 1,
    })
    const pending = entryRow({ id: '65', name: 'novo.bin', path: 'Novos/novo.bin', content_state: 'pending' })
    const copia = folderRow('66', 'Fotos - Copia', {
      candidate_bytes: 10 * GiB,
      checked_bytes: 10 * GiB,
      duplicated_bytes: 9 * GiB,
    })
    const root = folderRow('1', '', { path: '', path_b64: '' })
    stubApi(routes(7, { 'GET /api/search': searchRoute([...movs, notas, pending, copia, root], 7) }))
    renderApp('/search')
    await screen.findByText('7 results')

    for (const where of ['Fotos: Fotos/2004', 'Fotos: Backup_PC_2004/Meus documentos/Videos', 'Fotos: Celular']) {
      const line = screen.getByText(where)
      // Cut from the left when too long, with the full text as its title.
      expect(line.closest('[title]')).toHaveAttribute('title', where)
      expect(line.closest('[title]')).toHaveAttribute('dir', 'rtl')
      const row = line.closest('[role="row"]')
      if (!(row instanceof HTMLElement)) {
        throw new Error(`no row for ${where}`)
      }
      expect(within(row).getByRole('link', { name: 'MOV_0195.mp4' })).toBeInTheDocument()
      expect(cellIn(row, 'Has copies')).toHaveTextContent('3 copies')
    }
    // A top-level entry shows its source alone.
    expect(cellOf('notas.txt', 'Name')).toHaveTextContent(/^notas\.txtOld disk$/)
    expect(cellOf('notas.txt', 'Has copies')).toHaveTextContent('No other copy')
    expect(cellOf('novo.bin', 'Has copies')).toHaveTextContent('Not checked')
    expect(cellOf('Fotos - Copia', 'Has copies')).toHaveTextContent('90%')
    // The Has copies header says it is not the space that can be freed.
    expect(screen.getByRole('columnheader', { name: 'Has copies' })).toHaveAccessibleDescription(
      'Files here that also exist elsewhere, counting every copy. Not the space you could free: see Opportunities.',
    )
    // A source's top folder is named after the source.
    expect(screen.getByRole('link', { name: 'Fotos' })).toHaveAttribute('href', '/map/1')
    expect(screen.getByRole('checkbox', { name: 'Select Fotos' })).toBeInTheDocument()
    expect(cellOf('Fotos', 'Name')).toHaveTextContent(/^FotosDetails$/)
  })

  it('shows the folder of each result without the source when one source is searched', async () => {
    const mov = entryRow({
      id: '60',
      name: 'MOV_0195.mp4',
      path: 'Fotos/2004/MOV_0195.mp4',
      content_state: 'hashed',
      copies: 3,
    })
    const notas = entryRow({ id: '64', name: 'notas.txt', path: 'notas.txt' })
    stubApi(routes(2, { 'GET /api/search': searchRoute([mov, notas], 2) }))
    renderApp('/search?source=fotos')
    await screen.findByText('2 results')

    expect(cellOf('MOV_0195.mp4', 'Name')).toHaveTextContent(/^MOV_0195\.mp4Fotos\/2004$/)
    expect(cellOf('notas.txt', 'Name')).toHaveTextContent(/^notas\.txt$/)
  })

  it('searches the source chosen in its filter, and remembers it', async () => {
    const requests = stubApi(routes())
    const { router } = renderApp('/search?name=tmp')
    await screen.findByRole('option', { name: 'Old disk' })
    await screen.findByText('1,200 results')
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Source' }), 'Old disk')

    await waitFor(() => expect(router.state.location.search).toBe('?name=tmp&source=old-disk'))
    await waitFor(() => expect(searches(requests).at(-1)?.toString()).toBe('source=old-disk&name=tmp'))
    await waitFor(() => expect(counts(requests).at(-1)?.toString()).toBe('source=old-disk&name=tmp&count=only'))
    expect(localStorage.getItem('precious.source')).toBe('old-disk')
  })

  it('searches the remembered source unless the address names one', async () => {
    rememberSource('old-disk')
    const requests = stubApi(routes())
    const { router } = renderApp('/search?name=tmp')
    await screen.findByText('1,200 results')
    expect(screen.getByRole('combobox', { name: 'Source' })).toHaveValue('old-disk')
    expect(searches(requests).map(String)).toEqual(['source=old-disk&name=tmp'])
    expect(router.state.location.search).toBe('?name=tmp')

    await router.navigate('/search?name=tmp&source=fotos')
    await waitFor(() => expect(searches(requests).at(-1)?.toString()).toBe('source=fotos&name=tmp'))
    expect(screen.getByRole('combobox', { name: 'Source' })).toHaveValue('fotos')
  })

  it('limits the search to a folder until asked to search everywhere', async () => {
    const requests = stubApi(routes())
    const { router } = renderApp('/search?within=5&ext=tmp')
    expect(await screen.findByText('Backup_PC_2004')).toBeInTheDocument()
    expect(screen.getByText(/Only inside/)).toHaveTextContent('Only inside Backup_PC_2004')
    await waitFor(() => expect(searches(requests)[0]?.get('within')).toBe('5'))
    await userEvent.click(screen.getByRole('button', { name: 'Search everywhere' }))
    expect(router.state.location.search).toBe('?ext=tmp')
  })

  it('selects all results after a confirmation and reports what a bulk discard skipped', async () => {
    const requests = stubApi(
      routes(1_200, {
        'POST /api/commands/create-selection': () =>
          jsonResponse(201, {
            selection_id: 'sel-1',
            count: 1_200,
            bytes: 3 * GiB,
            kept: { count: 3, bytes: 5 * 1024 ** 2 },
            expires_at: '2026-10-05T18:00:00Z',
          }),
        'POST /api/commands/set-decision': () =>
          jsonResponse(200, {
            applied: 1_197,
            skipped_count: 3,
            skipped: [
              { entry_id: '42', path: 'Fotos/IMG_0042.JPG', path_b64: btoa('Fotos/IMG_0042.JPG') },
              {
                entry_id: '43',
                path: 'Backup_PC_2004/Meus documentos/carta.doc',
                path_b64: btoa('Backup_PC_2004/Meus documentos/carta.doc'),
              },
            ],
          }),
      }),
    )
    renderApp('/search?name=.tmp&within=5')
    await screen.findByText('1,200 results')

    await userEvent.click(screen.getByRole('button', { name: 'Select all results' }))
    const dialog = within(await screen.findByRole('alertdialog', { name: 'Select all results?' }))
    expect(dialog.getByText('1,200 items')).toBeInTheDocument()
    expect(dialog.getByText('3 GiB in total')).toBeInTheDocument()
    expect(
      dialog.getByText('3 of them (5 MiB) are kept. Discard, Later, Undecided, and Follow folder will skip them.'),
    ).toBeInTheDocument()
    expect(await commandBodies(requests, 'create-selection')).toEqual([{ query: { name: '.tmp', within: '5' } }])

    await userEvent.click(dialog.getByRole('button', { name: 'Select all' }))
    const bulk = within(screen.getByRole('region', { name: 'Change the selected items' }))
    expect(bulk.getByText('All 1,200 results are selected (3 GiB).')).toBeInTheDocument()
    for (const checkbox of screen.getAllByRole('checkbox', { name: /^Select f\d\.tmp$/ })) {
      expect(checkbox).toBeChecked()
    }

    await userEvent.click(within(bulk.getByRole('group', { name: 'Set decision' })).getByRole('button', { name: 'Discard' }))
    const report = within(await screen.findByRole('region', { name: 'Result of the last change' }))
    expect(report.getByText('Decision set on 1,197 items.')).toBeInTheDocument()
    expect(report.getByText('3 kept items were skipped:')).toBeInTheDocument()
    expect(
      report
        .getByRole('list')
        .querySelectorAll('li')
        .length,
    ).toBe(2)
    expect(report.getByText('Fotos/IMG_0042.JPG')).toBeInTheDocument()
    expect(report.getByText('Backup_PC_2004/Meus documentos/carta.doc')).toBeInTheDocument()
    expect(report.getByText('and 1 more')).toBeInTheDocument()
    expect(await commandBodies(requests, 'set-decision')).toEqual([{ selection_id: 'sel-1', decision: 'discard' }])
    expect(screen.queryByRole('region', { name: 'Change the selected items' })).not.toBeInTheDocument()
  })

  it('cancels a selection without changing anything', async () => {
    const requests = stubApi(
      routes(4, {
        'POST /api/commands/create-selection': () =>
          jsonResponse(201, {
            selection_id: 'sel-2',
            count: 4,
            bytes: 4000,
            kept: { count: 0, bytes: 0 },
            expires_at: '2026-10-05T18:00:00Z',
          }),
      }),
    )
    renderApp('/search?name=tmp')
    await screen.findByText('4 results')
    await userEvent.click(screen.getByRole('button', { name: 'Select all results' }))
    const dialog = within(await screen.findByRole('alertdialog'))
    expect(dialog.getByText('None of them is kept.')).toBeInTheDocument()
    await userEvent.click(dialog.getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('region', { name: 'Change the selected items' })).not.toBeInTheDocument()
    expect(requests.filter((r) => r.method === 'POST')).toHaveLength(1)
  })

  it('applies decisions and tags to rows picked one by one', async () => {
    const requests = stubApi(
      routes(5, {
        'POST /api/commands/set-decision': () => jsonResponse(200, { applied: 2, skipped_count: 0, skipped: [] }),
        'POST /api/commands/set-tags': () => jsonResponse(200, { applied: 2 }),
      }),
    )
    renderApp('/search?name=tmp')
    await screen.findByText('5 results')

    await userEvent.click(screen.getByRole('checkbox', { name: 'Select f1.tmp' }))
    await userEvent.click(screen.getByRole('checkbox', { name: 'Select f3.tmp' }))
    const bulk = within(screen.getByRole('region', { name: 'Change the selected items' }))
    expect(bulk.getByText('2 items selected')).toBeInTheDocument()
    await userEvent.selectOptions(bulk.getByRole('combobox', { name: 'Tag' }), 'scan')
    await userEvent.click(bulk.getByRole('button', { name: 'Add tag' }))
    const report = within(await screen.findByRole('region', { name: 'Result of the last change' }))
    expect(report.getByText('Tags changed on 2 items.')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('checkbox', { name: 'Select f2.tmp' }))
    await userEvent.click(screen.getByRole('checkbox', { name: 'Select f4.tmp' }))
    await userEvent.click(
      within(screen.getByRole('group', { name: 'Set decision' })).getByRole('button', { name: 'Later' }),
    )
    expect(await screen.findByText('Decision set on 2 items.')).toBeInTheDocument()
    expect(screen.queryByText(/were skipped/)).not.toBeInTheDocument()

    expect(await commandBodies(requests, 'set-tags')).toEqual([{ entry_ids: ['301', '303'], add: [10], remove: [] }])
    expect(await commandBodies(requests, 'set-decision')).toEqual([{ entry_ids: ['302', '304'], decision: 'later' }])
  })

  it('says when a selection has expired', async () => {
    stubApi(
      routes(1_200, {
        'POST /api/commands/create-selection': () =>
          jsonResponse(201, {
            selection_id: 'sel-3',
            count: 1_200,
            bytes: 3 * GiB,
            kept: { count: 0, bytes: 0 },
            expires_at: '2026-10-05T18:00:00Z',
          }),
        'POST /api/commands/set-decision': () => errorResponse(409, 'selection_expired'),
      }),
    )
    renderApp('/search?name=tmp')
    await screen.findByText('1,200 results')
    await userEvent.click(screen.getByRole('button', { name: 'Select all results' }))
    await userEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Select all' }))
    await userEvent.click(
      within(screen.getByRole('group', { name: 'Set decision' })).getByRole('button', { name: 'Keep' }),
    )
    expect(await screen.findByText('This selection has expired. Select the items again.')).toBeInTheDocument()
  })

  it('renames and deletes tags after a confirmation', async () => {
    const requests = stubApi(
      routes(5, {
        'POST /api/commands/rename-tag': async (request) => {
          const { name } = (await request.clone().json()) as { name: string }
          return name === 'familia'
            ? errorResponse(409, 'tag_exists')
            : jsonResponse(200, { tag: { id: 10, name } })
        },
        'POST /api/commands/delete-tag': () => jsonResponse(200, { tag: { id: 9, name: 'familia' } }),
      }),
    )
    renderApp('/search')
    await userEvent.click(await screen.findByRole('button', { name: 'Manage tags' }))
    const dialog = within(screen.getByRole('dialog', { name: 'Manage tags' }))
    expect(dialog.getByText('on 12 items')).toBeInTheDocument()

    await userEvent.click(dialog.getByRole('button', { name: 'Rename scan' }))
    const input = dialog.getByRole('textbox', { name: 'New name for scan' })
    await userEvent.clear(input)
    await userEvent.type(input, 'familia')
    await userEvent.click(dialog.getByRole('button', { name: 'Save' }))
    expect(await dialog.findByText('A tag with this name already exists. Choose it from the list.')).toBeInTheDocument()
    await userEvent.clear(input)
    await userEvent.type(input, 'scans{Enter}')
    await waitFor(() => expect(dialog.queryByRole('textbox')).not.toBeInTheDocument())

    await userEvent.click(dialog.getByRole('button', { name: 'Delete familia' }))
    const confirm = within(dialog.getByRole('group', { name: /Delete the tag “familia”/ }))
    expect(requests.some((r) => new URL(r.url).pathname === '/api/commands/delete-tag')).toBe(false)
    await userEvent.click(confirm.getByRole('button', { name: 'Delete tag' }))

    expect(await commandBodies(requests, 'rename-tag')).toEqual([
      { tag_id: 10, name: 'familia' },
      { tag_id: 10, name: 'scans' },
    ])
    await waitFor(async () => expect(await commandBodies(requests, 'delete-tag')).toEqual([{ tag_id: 9 }]))
  })
})
