import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import type { SearchCount } from '@/api/search'
import { entryDetail, entryRow, folderRow, fotosSource } from '@/test/fixtures'
import { errorResponse, jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'

const GiB = 1024 ** 3

const tmpFiles = Array.from({ length: 5 }, (_, i) =>
  entryRow({ id: String(300 + i), name: `f${i}.tmp`, path: `Backup_PC_2004/f${i}.tmp`, total_bytes: 1000 + i }),
)

function routes(
  count: SearchCount = 1_200,
  extra: Record<string, (request: Request) => Response | Promise<Response>> = {},
) {
  return {
    'GET /api/session': () => jsonResponse(200, signedIn),
    'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource()] }),
    'GET /api/tags': () =>
      jsonResponse(200, {
        tags: [
          { id: 9, name: 'familia', own_count: 12 },
          { id: 10, name: 'scan', own_count: 0 },
        ],
      }),
    'GET /api/search': () => jsonResponse(200, { items: tmpFiles, next_cursor: null, count }),
    'GET /api/entries/5': () =>
      jsonResponse(200, entryDetail(folderRow('5', 'Backup_PC_2004', { path: 'Backup_PC_2004' }))),
    ...extra,
  }
}

function searches(requests: Request[]) {
  return requests.filter((r) => new URL(r.url).pathname === '/api/search').map((r) => new URL(r.url).searchParams)
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
