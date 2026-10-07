import { screen, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { entryDetail, entryRow, folderRow, fotosSource } from '@/test/fixtures'
import { jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'

// vi.mock is hoisted above the static imports, so its factory loads the
// fake itself.
vi.mock('@/map/echarts', async () => {
  const { FakeChart } = await import('@/test/fakeChart')
  return { init: () => new FakeChart() }
})

// The Map's cells (r2b design D9): a quiet Decision column, and no figures
// for what could not be read.

const root = folderRow('1', 'fotos', { path: '', path_b64: '' })
const kept = folderRow('2', 'Documentos', { decision: 'keep', eff_decision: 'keep' })
const inherited = entryRow({ id: '3', name: 'setup.exe', path: 'setup.exe', eff_decision: 'discard' })
const undecided = entryRow({ id: '4', name: 'notas.txt', path: 'notas.txt' })
const locked = folderRow('5', 'Sem acesso', {
  state: 'unreadable',
  total_bytes: 0,
  total_files: 0,
  candidate_bytes: 0,
  checked_bytes: 0,
  duplicated_bytes: 0,
})
const rows = [kept, inherited, undecided, locked]

function routes() {
  return {
    'GET /api/session': () => jsonResponse(200, signedIn),
    'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource()] }),
    'GET /api/tags': () => jsonResponse(200, { tags: [] }),
    'GET /api/entries/1': () => jsonResponse(200, entryDetail(root, { ancestors: [] })),
    'GET /api/entries/1/treemap': () => jsonResponse(200, { entry: root, items: rows, other: { count: 0, bytes: 0 } }),
    'GET /api/entries/1/children': () => jsonResponse(200, { items: rows, next_cursor: null }),
  }
}

function cellOf(name: string, column: string) {
  const row = screen.getByRole('link', { name }).closest('[role="row"]')
  if (!(row instanceof HTMLElement)) {
    throw new Error(`no row for ${name}`)
  }
  const headers = screen.getAllByRole('columnheader').map((header) => header.textContent)
  const cell = within(row).getAllByRole('cell')[headers.indexOf(column)]
  if (cell === undefined) {
    throw new Error(`no ${column} cell for ${name}`)
  }
  return cell
}

describe('Map cells', () => {
  it('leaves Decision blank when nothing was decided, and marks an inherited decision briefly', async () => {
    stubApi(routes())
    renderApp('/map/1')
    await screen.findByRole('table', { name: 'Contents of Fotos' })

    expect(cellOf('Documentos', 'Decision')).toHaveTextContent(/^Keep$/)
    expect(cellOf('setup.exe', 'Decision')).toHaveTextContent(/^Discard ↑$/)
    expect(within(cellOf('setup.exe', 'Decision')).getByTitle('Discard (inherited)')).toBeInTheDocument()
    expect(cellOf('notas.txt', 'Decision').textContent).toBe('')
  })

  it('says a folder could not be read instead of showing 0 B, 0 files, and 0%', async () => {
    stubApi(routes())
    renderApp('/map/1')
    await screen.findByRole('table', { name: 'Contents of Fotos' })

    expect(cellOf('Sem acesso', 'Type or category')).toHaveTextContent('Could not be read')
    expect(cellOf('Sem acesso', 'Size▼')).toHaveTextContent(/^—$/)
    expect(cellOf('Sem acesso', 'Files')).toHaveTextContent(/^—$/)
    expect(cellOf('Sem acesso', 'Duplicated')).toHaveTextContent(/^—$/)
  })
})
