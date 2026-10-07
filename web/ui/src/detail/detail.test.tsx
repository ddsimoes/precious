import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import type { EntryDetail } from '@/api/entries'
import { entryDetail, entryRow, folderRow, fotosSource } from '@/test/fixtures'
import { errorResponse, jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'

const GiB = 1024 ** 3

const downloads = { id: '5', path: 'Downloads', path_b64: btoa('Downloads') }

// setupExe is a file inside Downloads, which is decided discard; the file
// inherits it and the tag `old` from Downloads, and carries `dudu` itself.
function setupExe(overrides: Partial<EntryDetail> = {}): EntryDetail {
  return entryDetail(entryRow({ eff_decision: 'discard', tag_ids: [7] }), {
    ancestors: [
      { id: '1', name: '', name_b64: '' },
      { id: '5', name: 'Downloads', name_b64: btoa('Downloads') },
    ],
    classification: {
      category: 'installer_download',
      family: 'programs',
      traits: [],
      triage: 'discard',
      group: false,
      veto: false,
      rules: [{ id: 'installer-name', explain: 'Its name says it installs a program.' }],
      indicators: [],
      owner: { category: null, group: null },
      rules_category: 'installer_download',
      rules_group: false,
    },
    intent: {
      decision: null,
      eff_decision: 'discard',
      from: downloads,
      tags: [
        { id: 3, name: 'old', own: false, from: downloads },
        { id: 7, name: 'dudu', own: true, from: null },
      ],
    },
    ...overrides,
  })
}

const tags = {
  tags: [
    { id: 3, name: 'old', own_count: 1 },
    { id: 7, name: 'dudu', own_count: 4 },
    { id: 9, name: 'familia', own_count: 12 },
  ],
}

function baseRoutes(detail: () => EntryDetail) {
  return {
    'GET /api/session': () => jsonResponse(200, signedIn),
    'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource()] }),
    'GET /api/tags': () => jsonResponse(200, tags),
    'GET /api/entries/12': () => jsonResponse(200, detail()),
  }
}

function commandBodies(requests: Request[], name: string) {
  return Promise.all(
    requests
      .filter((r) => r.method === 'POST' && new URL(r.url).pathname === `/api/commands/${name}`)
      .map((r) => r.clone().json() as Promise<unknown>),
  )
}

function detailGets(requests: Request[], id: string) {
  return requests.filter((r) => r.method === 'GET' && new URL(r.url).pathname === `/api/entries/${id}`).length
}

describe('Detail panel', () => {
  it('shows where the entry is, its figures, classification, decision, and tags', async () => {
    stubApi(baseRoutes(() => setupExe()))
    renderApp('/search?entry=12')

    const panel = within(await screen.findByRole('complementary', { name: 'setup.exe' }))
    const location = within(panel.getByRole('navigation', { name: 'Location' }))
    expect(await location.findByRole('link', { name: 'Fotos' })).toHaveAttribute('href', '/search?entry=1')
    expect(location.getByRole('link', { name: 'Downloads' })).toHaveAttribute('href', '/search?entry=5')
    expect(location.getByText('setup.exe')).toHaveAttribute('aria-current', 'page')

    expect(panel.getByText('3 MiB')).toBeInTheDocument()
    expect(panel.getByText('Dec 24, 2004, 10:00 AM')).toBeInTheDocument()

    const classification = within(panel.getByRole('region', { name: 'Classification' }))
    expect(classification.getByText('Installers and disk images', { selector: 'dd' })).toBeInTheDocument()
    expect(classification.getByText('Programs and system')).toBeInTheDocument()
    expect(classification.getByText('Its name says it installs a program.')).toBeInTheDocument()

    const decision = within(panel.getByRole('region', { name: 'Decision' }))
    expect(decision.getByText('None: follows its folder')).toBeInTheDocument()
    expect(decision.getByText('Discard', { selector: 'span' })).toBeInTheDocument()
    expect(decision.getByText(/Inherited from/)).toHaveTextContent('Inherited from Downloads')
    expect(decision.getByRole('link', { name: 'Downloads' })).toHaveAttribute('href', '/search?entry=5')
    expect(decision.getByRole('button', { name: 'Follow folder' })).toHaveAttribute('aria-pressed', 'true')

    const tagsRegion = within(panel.getByRole('region', { name: 'Tags' }))
    expect(within(tagsRegion.getByRole('list', { name: 'Own tags' })).getByText('dudu')).toBeInTheDocument()
    const inherited = within(tagsRegion.getByRole('list', { name: 'Inherited tags' }))
    expect(inherited.getByRole('listitem')).toHaveTextContent('old from Downloads')
    expect(tagsRegion.queryByRole('button', { name: 'Remove tag old' })).not.toBeInTheDocument()

    // Technical details stay collapsed until asked for.
    expect(panel.getByText('Raw name bytes')).not.toBeVisible()
    await userEvent.click(panel.getByText('Technical details'))
    expect(panel.getByText('Raw name bytes')).toBeVisible()
    expect(panel.getByText('73 65 74 75 70 2e 65 78 65')).toBeVisible()
  })

  it('shows a folder’s breakdowns, its veto, and its indicators', async () => {
    const office = folderRow('20', 'Microsoft Office', {
      category: 'application_installation',
      family: 'programs',
      triage: 'review',
      group: true,
      veto: true,
    })
    stubApi({
      ...baseRoutes(() => setupExe()),
      'GET /api/entries/20': () =>
        jsonResponse(
          200,
          entryDetail(office, {
            classification: {
              category: 'application_installation',
              family: 'programs',
              traits: ['contains_user_material'],
              triage: 'review',
              group: true,
              veto: true,
              rules: [{ id: 'program-folder', explain: 'It holds a program and its support files.' }],
              indicators: [
                {
                  entry_id: '812',
                  path: 'Microsoft Office/OFFICE11/orcamento.xls',
                  path_b64: btoa('Microsoft Office/OFFICE11/orcamento.xls'),
                  signal: 'editable_document_present',
                },
              ],
              owner: { category: null, group: null },
              rules_category: 'application_installation',
              rules_group: true,
            },
            stats: {
              dirs: 30,
              files: 1_000,
              unreadable: 0,
              mount_boundaries: 0,
              by_kind: [
                { kind: 'executable', bytes: 6 * GiB, files: 300 },
                { kind: 'document', bytes: 4 * GiB, files: 700 },
              ],
              by_year: [
                { year: null, bytes: 1 * GiB, files: 50 },
                { year: 2006, bytes: 1 * GiB, files: 100 },
                { year: 2003, bytes: 8 * GiB, files: 850 },
              ],
            },
          }),
        ),
    })
    renderApp('/map/1?entry=20')

    const panel = within(await screen.findByRole('complementary', { name: 'Microsoft Office' }))
    expect(panel.getByText('10 GiB')).toBeInTheDocument()
    expect(panel.getByText('1,000')).toBeInTheDocument()
    expect(panel.getByText('30')).toBeInTheDocument()
    expect(
      within(panel.getByRole('list', { name: 'Size by file type' }))
        .getAllByRole('listitem')
        .map((item) => item.textContent.replace(/\s+/g, ' ')),
    ).toEqual(['Applications6 GiB · 300 files', 'Documents4 GiB · 700 files'])
    expect(
      within(panel.getByRole('list', { name: 'Size by year of last change' }))
        .getAllByRole('listitem')
        .map((item) => item.textContent.replace(/\s+/g, ' ')),
    ).toEqual(['20038 GiB · 850 files', '20061 GiB · 100 files', 'Unknown date1 GiB · 50 files'])

    const classification = within(panel.getByRole('region', { name: 'Classification' }))
    expect(classification.getByText('Installed application', { selector: 'dd' })).toBeInTheDocument()
    expect(classification.getByText('Review')).toBeInTheDocument()
    expect(classification.getByText('Contains personal material')).toBeInTheDocument()
    expect(classification.getByText('It holds a program and its support files.')).toBeInTheDocument()
    expect(classification.getByText(/Discard is not suggested/)).toBeInTheDocument()
    const indicators = within(classification.getByRole('list', { name: 'Personal material found inside' }))
    expect(indicators.getByRole('link', { name: 'Microsoft Office/OFFICE11/orcamento.xls' })).toHaveAttribute(
      'href',
      '/map/1?entry=812',
    )
    expect(indicators.getByText('(Office document)')).toBeInTheDocument()
    expect(panel.getByRole('link', { name: 'Search in this folder' })).toHaveAttribute('href', '/search?within=20')
  })

  it('sets the own decision with an individual request, inherit included', async () => {
    let detail = setupExe()
    const requests = stubApi({
      ...baseRoutes(() => detail),
      'POST /api/commands/set-decision': async (request) => {
        const body = (await request.clone().json()) as { decision: string }
        detail =
          body.decision === 'inherit'
            ? setupExe()
            : setupExe({
                entry: entryRow({ decision: 'keep', eff_decision: 'keep' }),
                intent: { ...setupExe().intent, decision: 'keep', eff_decision: 'keep', from: { id: '12', path: 'Downloads/setup.exe', path_b64: '' } },
              })
        return jsonResponse(200, { applied: 1, skipped_count: 0, skipped: [] })
      },
    })
    renderApp('/search?entry=12')
    const decision = within(await screen.findByRole('region', { name: 'Decision' }))

    await userEvent.click(decision.getByRole('button', { name: 'Keep' }))
    await waitFor(() => expect(decision.getByRole('button', { name: 'Keep' })).toHaveAttribute('aria-pressed', 'true'))
    expect(decision.getByText('Set on this item.')).toBeInTheDocument()

    await userEvent.click(decision.getByRole('button', { name: 'Follow folder' }))
    await waitFor(() => expect(decision.getByText(/Inherited from/)).toBeInTheDocument())

    expect(await commandBodies(requests, 'set-decision')).toEqual([
      { entry_id: '12', decision: 'keep' },
      { entry_id: '12', decision: 'inherit' },
    ])
    const post = requests.find((r) => r.method === 'POST')
    expect(post?.headers.get('X-CSRF-Token')).toBe('session-token')
    expect(post?.headers.get('Idempotency-Key')).toMatch(/^[0-9a-f-]{36}$/)
    expect(detailGets(requests, '12')).toBe(3)
  })

  it('adds and removes own tags and creates a tag inline', async () => {
    const requests = stubApi({
      ...baseRoutes(() => setupExe()),
      'POST /api/commands/set-tags': () => jsonResponse(200, { applied: 1 }),
      'POST /api/commands/create-tag': async (request) => {
        const { name } = (await request.clone().json()) as { name: string }
        return name === 'Familia'
          ? errorResponse(409, 'tag_exists')
          : jsonResponse(201, { tag: { id: 11, name } })
      },
    })
    renderApp('/search?entry=12')
    const tagsRegion = within(await screen.findByRole('region', { name: 'Tags' }))

    await userEvent.click(tagsRegion.getByRole('button', { name: 'Remove tag dudu' }))
    // Own tags are not offered again; inherited ones may become own.
    const choose = tagsRegion.getByRole('combobox', { name: 'Add a tag' })
    expect(within(choose).getAllByRole('option').map((o) => o.textContent)).toEqual(['Choose a tag', 'old', 'familia'])
    await userEvent.selectOptions(choose, 'familia')
    await userEvent.click(tagsRegion.getByRole('button', { name: 'Add' }))

    await userEvent.type(tagsRegion.getByRole('textbox', { name: 'New tag' }), 'Familia')
    await userEvent.click(tagsRegion.getByRole('button', { name: 'Create and add' }))
    expect(await tagsRegion.findByRole('alert')).toHaveTextContent(
      'A tag with this name already exists. Choose it from the list.',
    )

    await userEvent.clear(tagsRegion.getByRole('textbox', { name: 'New tag' }))
    await userEvent.type(tagsRegion.getByRole('textbox', { name: 'New tag' }), 'livro')
    await userEvent.click(tagsRegion.getByRole('button', { name: 'Create and add' }))
    await waitFor(() => expect(tagsRegion.getByRole('textbox', { name: 'New tag' })).toHaveValue(''))

    expect(await commandBodies(requests, 'create-tag')).toEqual([{ name: 'Familia' }, { name: 'livro' }])
    expect(await commandBodies(requests, 'set-tags')).toEqual([
      { entry_ids: ['12'], add: [], remove: [7] },
      { entry_ids: ['12'], add: [9], remove: [] },
      { entry_ids: ['12'], add: [11], remove: [] },
    ])
  })

  it('renders names, paths, and link text as plain text (A15)', async () => {
    const hostile = '<img src=x onerror=alert(1)>'
    stubApi(
      baseRoutes(() =>
        setupExe({
          entry: entryRow({ name: hostile, path: `Downloads/${hostile}` }),
          intent: { decision: null, eff_decision: 'undecided', from: null, tags: [] },
        }),
      ),
    )
    renderApp('/search?entry=12')

    const panel = await screen.findByRole('complementary', { name: hostile })
    expect(within(panel).getByRole('heading', { name: hostile })).toBeInTheDocument()
    expect(panel.querySelector('img')).toBeNull()
    expect(within(panel).getByText('No folder above it has a decision.')).toBeInTheDocument()
  })

  it('closes when asked', async () => {
    stubApi(baseRoutes(() => setupExe()))
    const { router } = renderApp('/search?entry=12&name=setup')
    await screen.findByRole('complementary', { name: 'setup.exe' })
    await userEvent.click(screen.getByRole('button', { name: 'Close details' }))
    expect(screen.queryByRole('complementary')).not.toBeInTheDocument()
    expect(router.state.location.search).toBe('?name=setup')
  })
})
