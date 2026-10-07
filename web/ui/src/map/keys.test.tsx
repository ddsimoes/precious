import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

import { entryDetail, entryRow, folderRow, fotosSource } from '@/test/fixtures'
import { jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'

// vi.mock is hoisted above the static imports, so its factory loads the
// fake itself.
vi.mock('@/map/echarts', async () => {
  const { FakeChart } = await import('@/test/fakeChart')
  return { init: () => new FakeChart() }
})

// The Map keys (r2b design D9) and the focus a dialog gives back.

const root = folderRow('1', 'fotos', { path: '', path_b64: '' })
const natal = entryRow({ id: '4', name: 'NATAL.JPG', path: 'NATAL.JPG', file_kind: 'image' })
const downloads = folderRow('3', 'Downloads')
const fotos = folderRow('2', 'Ferias')

function routes() {
  return {
    'GET /api/session': () => jsonResponse(200, signedIn),
    'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource()] }),
    'GET /api/tags': () => jsonResponse(200, { tags: [] }),
    'GET /api/entries/1': () => jsonResponse(200, entryDetail(root, { ancestors: [] })),
    'GET /api/entries/1/treemap': () =>
      jsonResponse(200, { entry: root, items: [natal, downloads, fotos], other: { count: 0, bytes: 0 } }),
    'GET /api/entries/1/children': () => jsonResponse(200, { items: [natal, downloads, fotos], next_cursor: null }),
    'GET /api/entries/2': () => jsonResponse(200, entryDetail(fotos)),
    'GET /api/entries/2/treemap': () => jsonResponse(200, { entry: fotos, items: [], other: { count: 0, bytes: 0 } }),
    'GET /api/entries/2/children': () => jsonResponse(200, { items: [], next_cursor: null }),
    'GET /api/entries/3': () => jsonResponse(200, entryDetail(downloads)),
    'GET /api/entries/4': () => jsonResponse(200, entryDetail(natal)),
  }
}

function rowOf(name: string) {
  const row = screen.getByRole('link', { name }).closest('[role="row"]')
  if (!(row instanceof HTMLElement)) {
    throw new Error(`no row for ${name}`)
  }
  return row
}

// escapeDialog presses Escape in the open dialog, which jsdom does not turn
// into the dialog's cancel event as browsers do.
async function escapeDialog(dialog: HTMLElement) {
  await userEvent.keyboard('{Escape}')
  fireEvent(dialog, new Event('cancel', { cancelable: true }))
}

describe('Map keys', () => {
  // The inventory-explorer scenario "Walking a folder with the arrows".
  it('walks the rows with the arrows and opens a folder with Enter', async () => {
    stubApi(routes())
    const { router } = renderApp('/map/1')
    await screen.findByRole('table', { name: 'Contents of Fotos' })

    await userEvent.click(within(rowOf('NATAL.JPG')).getAllByRole('cell')[1]!)
    expect(router.state.location.search).toBe('?entry=4')
    await userEvent.keyboard('{ArrowDown}{ArrowDown}')
    await waitFor(() => expect(router.state.location.search).toBe('?entry=2'))
    expect(rowOf('Ferias')).toHaveAttribute('aria-selected', 'true')
    await waitFor(() => expect(rowOf('Ferias')).toHaveFocus())
    expect(await screen.findByRole('complementary', { name: 'Ferias' })).toBeInTheDocument()
    // Walking replaces the address: Back leaves the walk.
    expect(router.state.historyAction).toBe('REPLACE')

    // Past the last row the selection stays.
    await userEvent.keyboard('{ArrowDown}')
    expect(router.state.location.search).toBe('?entry=2')

    await userEvent.keyboard('{Enter}')
    await waitFor(() => expect(router.state.location.pathname).toBe('/map/2'))
    expect(await screen.findByRole('table', { name: 'Contents of Ferias' })).toBeInTheDocument()
  })

  it('selects the first or the last row when none is selected, and closes the panel with Escape', async () => {
    stubApi(routes())
    const { router } = renderApp('/map/1')
    await screen.findByRole('table', { name: 'Contents of Fotos' })

    await userEvent.keyboard('{ArrowUp}')
    await waitFor(() => expect(router.state.location.search).toBe('?entry=2'))
    await userEvent.keyboard('{ArrowUp}')
    await waitFor(() => expect(router.state.location.search).toBe('?entry=3'))
    expect(await screen.findByRole('complementary', { name: 'Downloads' })).toBeInTheDocument()

    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(router.state.location.search).toBe(''))
    expect(screen.queryByRole('complementary')).not.toBeInTheDocument()

    await userEvent.keyboard('{ArrowDown}')
    await waitFor(() => expect(router.state.location.search).toBe('?entry=4'))
  })

  it('walks on from the row the last arrow chose before the address follows', async () => {
    stubApi(routes())
    const { router } = renderApp('/map/1')
    await screen.findByRole('table', { name: 'Contents of Fotos' })

    // Three arrows in one task: the route renders only after the last one.
    act(() => {
      for (let i = 0; i < 3; i++) {
        document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }))
      }
    })
    await waitFor(() => expect(router.state.location.search).toBe('?entry=2'))
    // The first arrow added the walk to history, the others replaced it.
    expect(router.state.historyAction).toBe('REPLACE')
    await waitFor(() => expect(rowOf('Ferias')).toHaveFocus())
  })

  it('keeps walking in a window that opens the panel over the Map, which a link still focuses', async () => {
    // Below 1,600 px the panel is a drawer, which takes the focus (D23).
    vi.stubGlobal('matchMedia', (media: string) => ({ media, matches: false }))
    stubApi(routes())
    const { router } = renderApp('/map/1')
    await screen.findByRole('table', { name: 'Contents of Fotos' })

    await userEvent.keyboard('{ArrowDown}')
    expect(await screen.findByRole('complementary', { name: 'NATAL.JPG' })).toBeInTheDocument()
    await waitFor(() => expect(rowOf('NATAL.JPG')).toHaveFocus())
    await userEvent.keyboard('{ArrowDown}{ArrowDown}')
    await waitFor(() => expect(router.state.location.search).toBe('?entry=2'))
    await waitFor(() => expect(rowOf('Ferias')).toHaveFocus())
    expect(await screen.findByRole('complementary', { name: 'Ferias' })).toBeInTheDocument()
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(router.state.location.search).toBe(''))

    screen.getByRole('link', { name: 'Details of Downloads' }).focus()
    await userEvent.keyboard('{Enter}')
    const panel = await screen.findByRole('complementary', { name: 'Downloads' })
    await waitFor(() => expect(panel).toHaveFocus())
  })

  it('leaves the focus on the row the keys left when the address arrives before the next row takes it', async () => {
    vi.stubGlobal('matchMedia', (media: string) => ({ media, matches: false }))
    stubApi(routes())
    const { router } = renderApp('/map/1')
    await screen.findByRole('table', { name: 'Contents of Fotos' })

    // Arrows pressed right after Back: the address of the row they chose
    // renders while the row they left still holds the focus.
    rowOf('Ferias').focus()
    await act(() => router.navigate('/map/1?entry=4'))
    const panel = await screen.findByRole('complementary', { name: 'NATAL.JPG' })
    expect(panel).not.toHaveFocus()
    await userEvent.keyboard('{ArrowDown}')
    await waitFor(() => expect(router.state.location.search).toBe('?entry=3'))
  })

  it('opens a file in the viewer with Enter, and gives the focus back to its row', async () => {
    stubApi(routes())
    renderApp('/map/1?entry=4')
    await screen.findByRole('table', { name: 'Contents of Fotos' })
    rowOf('NATAL.JPG').focus()

    await userEvent.keyboard('{Enter}')
    const dialog = screen.getByRole('dialog', { name: 'NATAL.JPG' })
    within(dialog).getByRole('button', { name: 'Close' }).focus()
    await escapeDialog(dialog)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(rowOf('NATAL.JPG')).toHaveFocus()
  })

  it('leaves keys typed in a field, in a dialog, or in the panel alone', async () => {
    stubApi(routes())
    const { router } = renderApp('/map/1?entry=4')
    await screen.findByRole('table', { name: 'Contents of Fotos' })
    const panel = await screen.findByRole('complementary', { name: 'NATAL.JPG' })

    // A field.
    screen.getByRole('combobox', { name: 'Color by' }).focus()
    await userEvent.keyboard('{Escape}')
    expect(router.state.location.search).toBe('?entry=4')

    // The panel: its buttons keep their keys.
    within(panel).getByRole('button', { name: 'Open' }).focus()
    await userEvent.keyboard('{ArrowDown}')
    expect(router.state.location.search).toBe('?entry=4')

    // A dialog, open from the panel.
    await userEvent.click(within(panel).getByRole('button', { name: 'Open' }))
    const dialog = screen.getByRole('dialog', { name: 'NATAL.JPG' })
    within(dialog).getByRole('button', { name: 'Close' }).focus()
    await userEvent.keyboard('{ArrowDown}{Enter}')
    expect(router.state.location.search).toBe('?entry=4')
    expect(router.state.location.pathname).toBe('/map/1')
  })
})

describe('Dialog focus', () => {
  // The inventory-explorer scenario "Escape from the viewer".
  it('gives the focus back to the panel’s Open button, and a second Escape closes the panel', async () => {
    stubApi(routes())
    const { router } = renderApp('/map/1?entry=4')
    const panel = within(await screen.findByRole('complementary', { name: 'NATAL.JPG' }))
    const open = panel.getByRole('button', { name: 'Open' })

    await userEvent.click(open)
    const dialog = screen.getByRole('dialog', { name: 'NATAL.JPG' })
    // A browser moves the focus into a modal dialog.
    within(dialog).getByRole('button', { name: 'Close' }).focus()

    await escapeDialog(dialog)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(open).toHaveFocus()
    expect(router.state.location.search).toBe('?entry=4')

    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(router.state.location.search).toBe(''))
    expect(screen.queryByRole('complementary')).not.toBeInTheDocument()
  })

  it('leaves the focus alone when the opener left the page', async () => {
    stubApi(routes())
    renderApp('/map/1?entry=4')
    const panel = within(await screen.findByRole('complementary', { name: 'NATAL.JPG' }))
    await userEvent.click(panel.getByRole('button', { name: 'Open' }))
    const dialog = screen.getByRole('dialog', { name: 'NATAL.JPG' })
    const close = within(dialog).getByRole('button', { name: 'Close' })
    close.focus()

    // Closing the panel removes the Open button and the viewer with it.
    await userEvent.click(panel.getByRole('button', { name: 'Close details' }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(document.activeElement === document.body || document.activeElement === null).toBe(true)
  })
})
