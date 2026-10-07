import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import { rememberSource } from '@/app/sourceChoice'
import { card, coverage, fotosSource, homeResponse, usbSource } from '@/test/fixtures'
import { jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'

// The source chosen on one screen is used by the others opened without one
// in their address (r2b design D9).

function routes() {
  return {
    'GET /api/session': () => jsonResponse(200, signedIn),
    'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource(), usbSource()] }),
    'GET /api/tags': () => jsonResponse(200, { tags: [] }),
    'GET /api/home': () => jsonResponse(200, homeResponse()),
    'GET /api/opportunities': () =>
      jsonResponse(200, { cards: [card('caches', 1024, 2)], coverage: coverage(), computed_at: null }),
    'GET /api/opportunities/caches': () =>
      jsonResponse(200, { card: card('caches', 1024, 2), items: [], next_cursor: null }),
    'GET /api/gems': (request: Request) =>
      jsonResponse(200, {
        section: new URL(request.url).searchParams.get('section'),
        items: [],
        next_cursor: null,
        coverage: coverage(),
      }),
    'GET /api/search': (request: Request) =>
      new URL(request.url).searchParams.get('count') === 'only'
        ? jsonResponse(200, { count: 0 })
        : jsonResponse(200, { items: [], next_cursor: null }),
  }
}

// sourcesAsked lists the source of every request to path, '' for none.
function sourcesAsked(requests: Request[], path: string) {
  return requests
    .filter((r) => new URL(r.url).pathname === path)
    .map((r) => new URL(r.url).searchParams.get('source') ?? '')
}

async function openFromMenu(name: string) {
  await userEvent.click(within(screen.getByRole('navigation', { name: 'Main' })).getByRole('link', { name }))
}

async function expectChosen(value: string) {
  await waitFor(() => expect(screen.getByRole('combobox', { name: 'Source' })).toHaveValue(value))
}

describe('The chosen source', () => {
  it('carries from Home to Opportunities, a review list, Gems, and Search', async () => {
    const requests = stubApi(routes())
    const { router } = renderApp('/')
    await screen.findByRole('option', { name: 'Old disk' })
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Source' }), 'Old disk')
    await waitFor(() => expect(sourcesAsked(requests, '/api/home').at(-1)).toBe('old-disk'))
    expect(localStorage.getItem('precious.source')).toBe('old-disk')

    await openFromMenu('Opportunities')
    expect(router.state.location.search).toBe('')
    await expectChosen('old-disk')
    await waitFor(() => expect(sourcesAsked(requests, '/api/opportunities')).toEqual(['old-disk']))

    await userEvent.click(await screen.findByRole('link', { name: 'Caches, temporary files, and build output' }))
    await waitFor(() => expect(sourcesAsked(requests, '/api/opportunities/caches')).toEqual(['old-disk']))

    await openFromMenu('Gems')
    await expectChosen('old-disk')
    await waitFor(() => expect(sourcesAsked(requests, '/api/gems')).toEqual(['old-disk', 'old-disk', 'old-disk']))

    await openFromMenu('Search')
    await expectChosen('old-disk')
    await waitFor(() => expect(sourcesAsked(requests, '/api/search')).toEqual(['old-disk', 'old-disk']))
  })

  it('gives way to a source in the address', async () => {
    rememberSource('old-disk')
    const requests = stubApi(routes())
    renderApp('/gems?source=fotos')
    await expectChosen('fotos')
    await waitFor(() => expect(sourcesAsked(requests, '/api/gems')).toEqual(['fotos', 'fotos', 'fotos']))
    // The address does not change what is remembered.
    expect(localStorage.getItem('precious.source')).toBe('old-disk')
  })

  it('is forgotten when all sources are chosen', async () => {
    rememberSource('old-disk')
    const requests = stubApi(routes())
    renderApp('/opportunities')
    await expectChosen('old-disk')
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Source' }), 'All sources')
    await waitFor(() => expect(sourcesAsked(requests, '/api/opportunities')).toEqual(['old-disk', '']))
    expect(localStorage.getItem('precious.source')).toBeNull()
  })

  it('is not used once its source is gone', async () => {
    rememberSource('removed')
    const requests = stubApi(routes())
    renderApp('/')
    await expectChosen('')
    await waitFor(() => expect(sourcesAsked(requests, '/api/home').at(-1)).toBe(''))
  })
})
