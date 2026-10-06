import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import { MockEventSource } from '@/test/eventSource'
import { fotosSource, homeResponse, scanEvent, usbSource } from '@/test/fixtures'
import { errorResponse, jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'

const GiB = 1024 ** 3

// rows reads a bar list as its items' text, with whitespace normalized.
function rows(name: string) {
  const list = screen.getByRole('list', { name })
  return within(list)
    .getAllByRole('listitem')
    .map((item) => item.textContent.replace(/\s+/g, ' '))
}

function homeRequests(requests: Request[]) {
  return requests
    .filter((r) => new URL(r.url).pathname === '/api/home')
    .map((r) => new URL(r.url).search)
}

describe('Home screen', () => {
  it('shows the totals, the breakdowns, and the decisions', async () => {
    stubApi({
      'GET /api/session': () => jsonResponse(200, signedIn),
      'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource(), usbSource()] }),
      'GET /api/home': () => jsonResponse(200, homeResponse()),
    })
    renderApp('/')

    const totals = within(await screen.findByRole('region', { name: 'Totals' }))
    expect(totals.getByText('120 GiB')).toBeInTheDocument()
    expect(totals.getByText('410,000')).toBeInTheDocument()
    expect(totals.getByText('31,000')).toBeInTheDocument()
    expect(screen.queryByText(/figures are incomplete/)).not.toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Scans in progress' })).not.toBeInTheDocument()

    expect(rows('Size by category')).toEqual([
      'Personal and valuable80 GiB · 300,000 files',
      'Programs and system25 GiB · 90,000 files',
      'Disposable5 GiB · 15,000 files',
      'Containers10 GiB · 5,000 files',
    ])
    // File kinds from the largest down; years in order.
    expect(rows('Size by file type')).toEqual([
      'Videos60 GiB · 2,000 files',
      'Images40 GiB · 200,000 files',
      'Other20 GiB · 208,000 files',
    ])
    expect(rows('Size by year of last change')).toEqual([
      '200450 GiB · 110,000 files',
      '202470 GiB · 300,000 files',
    ])
    expect(rows('Decisions')).toEqual([
      'Keep25 GiB · 60,000 files',
      'Discard40 GiB · 45,000 files',
      'Later5 GiB · 5,000 files',
      'Undecided50 GiB · 300,000 files',
    ])
  })

  it('recomputes every figure for the chosen source', async () => {
    const requests = stubApi({
      'GET /api/session': () => jsonResponse(200, signedIn),
      'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource(), usbSource()] }),
      'GET /api/home': (request) =>
        new URL(request.url).searchParams.get('source') === 'old-disk'
          ? jsonResponse(
              200,
              homeResponse({
                totals: { bytes: 5 * GiB, files: 1_200, dirs: 80 },
                by_family: [{ family: 'programs', bytes: 5 * GiB, files: 1_200 }],
                by_kind: [{ kind: 'installer', bytes: 5 * GiB, files: 1_200 }],
                by_year: [{ year: 2011, bytes: 5 * GiB, files: 1_200 }],
                decisions: {
                  undecided: { bytes: 5 * GiB, files: 1_200 },
                  keep: { bytes: 0, files: 0 },
                  discard: { bytes: 0, files: 0 },
                  later: { bytes: 0, files: 0 },
                },
              }),
            )
          : jsonResponse(200, homeResponse()),
    })
    const { router } = renderApp('/')
    const user = userEvent.setup()

    await screen.findByRole('region', { name: 'Totals' })
    const filter = screen.getByRole('combobox', { name: 'Source' })
    expect(within(filter).getAllByRole('option').map((o) => o.textContent)).toEqual([
      'All sources',
      'Fotos',
      'Old disk',
    ])
    await user.selectOptions(filter, 'Old disk')

    const totals = within(screen.getByRole('region', { name: 'Totals' }))
    expect(await totals.findByText('5 GiB')).toBeInTheDocument()
    expect(totals.getByText('1,200')).toBeInTheDocument()
    expect(totals.getByText('80')).toBeInTheDocument()
    expect(rows('Size by file type')).toEqual(['Installers5 GiB · 1,200 files'])
    expect(rows('Size by year of last change')).toEqual(['20115 GiB · 1,200 files'])
    expect(rows('Decisions')[3]).toBe('Undecided5 GiB · 1,200 files')
    expect(router.state.location.search).toBe('?source=old-disk')
    expect(homeRequests(requests)).toEqual(['', '?source=old-disk'])

    await user.selectOptions(filter, 'All sources')
    expect(await totals.findByText('120 GiB')).toBeInTheDocument()
    expect(router.state.location.search).toBe('')
  })

  it('opens on the source named in the address', async () => {
    const requests = stubApi({
      'GET /api/session': () => jsonResponse(200, signedIn),
      'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource(), usbSource()] }),
      'GET /api/home': () => jsonResponse(200, homeResponse()),
    })
    renderApp('/?source=fotos')

    await screen.findByRole('region', { name: 'Totals' })
    await waitFor(() => expect(screen.getByRole('combobox', { name: 'Source' })).toHaveValue('fotos'))
    expect(homeRequests(requests)).toEqual(['?source=fotos'])
  })

  it('says when its figures are partial', async () => {
    stubApi({
      'GET /api/session': () => jsonResponse(200, signedIn),
      'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource()] }),
      'GET /api/home': () => jsonResponse(200, homeResponse({ partial: true })),
    })
    renderApp('/')

    expect(
      await screen.findByText('Some folders could not be read, so these figures are incomplete.'),
    ).toBeInTheDocument()
  })

  it('shows active scans and follows their progress', async () => {
    stubApi({
      'GET /api/session': () => jsonResponse(200, signedIn),
      'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource(), usbSource()] }),
      'GET /api/home': () =>
        jsonResponse(
          200,
          homeResponse({
            scans: [
              {
                source_id: 'fotos',
                job_id: '42',
                state: 'running',
                progress: { phase: 1, dirs: 300, files: 9_000, bytes: 2 * GiB },
              },
            ],
          }),
        ),
    })
    renderApp('/')

    const scans = within(await screen.findByRole('region', { name: 'Scans in progress' }))
    const progress = await scans.findByRole('status', { name: 'Fotos' })
    expect(progress).toHaveTextContent('ScanningFolders300Files9,000Size2 GiB')

    act(() =>
      MockEventSource.latest().emit(
        'job',
        scanEvent({ progress: { phase: 1, dirs: 900, files: 27_000, bytes: 6 * GiB } }),
        '3',
      ),
    )
    await waitFor(() => expect(progress).toHaveTextContent('ScanningFolders900Files27,000Size6 GiB'))
  })

  it('points to the Sources screen when there is no source', async () => {
    stubApi({
      'GET /api/session': () => jsonResponse(200, signedIn),
      'GET /api/sources': () => jsonResponse(200, { sources: [] }),
      'GET /api/home': () => jsonResponse(200, homeResponse({ totals: { bytes: 0, files: 0, dirs: 0 } })),
    })
    renderApp('/')

    expect(await screen.findByText(/No sources yet\./)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Add a source' })).toHaveAttribute('href', '/sources')
  })

  it('shows a failed load with a retry', async () => {
    let fail = true
    stubApi({
      'GET /api/session': () => jsonResponse(200, signedIn),
      'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource()] }),
      'GET /api/home': () => (fail ? errorResponse(500, 'internal', 'boom') : jsonResponse(200, homeResponse())),
    })
    renderApp('/')
    const user = userEvent.setup()

    expect(await screen.findByRole('alert')).toHaveTextContent('Something went wrong on the server. Try again.')
    fail = false
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('region', { name: 'Totals' })).toBeInTheDocument()
  })
})
