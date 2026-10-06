import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import type { Gem, GemSection } from '@/api/gems'
import { coverage, entryRow, folderRow, fotosSource, relationTo } from '@/test/fixtures'
import { jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'

const MiB = 1024 ** 2

const photo = entryRow({
  id: '11',
  name: 'IMG_0001.JPG',
  path: 'Backup_Celular/IMG_0001.JPG',
  file_kind: 'image',
  category: 'personal_media',
  family: 'personal',
  size: 2 * MiB,
  mtime: '2003-05-01T10:00:00Z',
  content_state: 'hashed',
  copies: 1,
})
const office = folderRow('40', 'OFFICE11', { path: 'Backup_PC_2004/C/Arquivos de programas/Microsoft Office/OFFICE11' })
const budget = entryRow({
  id: '41',
  name: 'Meu orcamento casamento.xls',
  path: `${office.path}/Meu orcamento casamento.xls`,
  file_kind: 'document',
  size: 1 * MiB,
})
const fotos = folderRow('2', 'Fotos')
const editada = entryRow({
  id: '51',
  name: 'DSC_editada.JPG',
  path: 'Fotos - Copia/2006/Praia/DSC_editada.JPG',
  file_kind: 'image',
  size: 3 * MiB,
})

const sections: Record<GemSection, Gem[]> = {
  unique: [{ entry: photo, group: null, relation: null }],
  rescue: [{ entry: budget, group: office, relation: null }],
  only_in_copy: [{ entry: editada, group: null, relation: relationTo(fotos, { kind: 'overlap' }) }],
}

function commandBodies(requests: Request[], name: string) {
  return Promise.all(
    requests
      .filter((r) => r.method === 'POST' && new URL(r.url).pathname === `/api/commands/${name}`)
      .map((r) => r.clone().json() as Promise<unknown>),
  )
}

function gemsRoutes() {
  return {
    'GET /api/session': () => jsonResponse(200, signedIn),
    'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource()] }),
    'GET /api/gems': (request: Request) => {
      const section = new URL(request.url).searchParams.get('section') as GemSection
      return jsonResponse(200, { section, items: sections[section], next_cursor: null, coverage: coverage() })
    },
    'POST /api/commands/set-decision': () => jsonResponse(200, { applied: 1, skipped_count: 0, skipped: [] }),
    'POST /api/commands/select-list': () =>
      jsonResponse(201, {
        selection_id: 's9',
        count: 1,
        bytes: 2 * MiB,
        kept: { count: 0, bytes: 0 },
        expires_at: '2026-10-06T11:00:00Z',
      }),
  }
}

describe('Gems', () => {
  it('lists three sections, each with its checked share', async () => {
    const requests = stubApi(gemsRoutes())
    renderApp('/gems')

    const unique = within(await screen.findByRole('region', { name: 'Personal files with no other copy' }))
    expect(await unique.findByRole('link', { name: photo.path })).toHaveAttribute('href', '/gems?entry=11')
    expect(unique.getByText('On all disks, 89% of what could have a copy is checked.')).toBeInTheDocument()
    expect(unique.getByText('49,000 files not checked yet are not listed.')).toBeInTheDocument()
    expect(unique.getByText('Fotos · May 1, 2003')).toBeInTheDocument()

    const rescue = within(
      screen.getByRole('region', { name: 'Personal material inside programs and disposable folders' }),
    )
    expect(await rescue.findByRole('link', { name: budget.path })).toBeInTheDocument()
    expect(rescue.getByText(/^Inside/)).toHaveTextContent(`Inside ${office.path}`)
    expect(rescue.getByRole('link', { name: office.path })).toHaveAttribute('href', '/gems?entry=40')

    const onlyInCopy = within(screen.getByRole('region', { name: 'Files in only one of two similar folders' }))
    expect(await onlyInCopy.findByRole('link', { name: editada.path })).toBeInTheDocument()
    expect(onlyInCopy.getByText(/^Not in/)).toHaveTextContent('Not in Fotos')

    expect(
      requests.filter((r) => new URL(r.url).pathname === '/api/gems').map((r) => new URL(r.url).search),
    ).toEqual(['?section=unique', '?section=rescue', '?section=only_in_copy'])
  })

  it('decides a gem, and selects a whole section', async () => {
    const requests = stubApi(gemsRoutes())
    renderApp('/gems?source=fotos')
    const user = userEvent.setup()

    const unique = within(await screen.findByRole('region', { name: 'Personal files with no other copy' }))
    const group = await unique.findByRole('group', { name: 'Decision for IMG_0001.JPG' })
    await user.click(within(group).getByRole('button', { name: 'Keep' }))
    await waitFor(async () =>
      expect(await commandBodies(requests, 'set-decision')).toEqual([{ entry_id: '11', decision: 'keep' }]),
    )

    await user.click(unique.getByRole('button', { name: 'Select all rows' }))
    const dialog = within(await screen.findByRole('alertdialog', { name: 'Select every row of this list?' }))
    expect(dialog.getByText('1 item')).toBeInTheDocument()
    expect(await commandBodies(requests, 'select-list')).toEqual([{ list: 'gems_unique', source_id: 'fotos' }])
    await user.click(dialog.getByRole('button', { name: 'Select all' }))
    await user.click(
      within(unique.getByRole('group', { name: 'Set decision' })).getByRole('button', { name: 'Keep' }),
    )
    await waitFor(async () =>
      expect((await commandBodies(requests, 'set-decision')).at(-1)).toEqual({ selection_id: 's9', decision: 'keep' }),
    )
    expect(
      requests.filter((r) => new URL(r.url).pathname === '/api/gems').map((r) => new URL(r.url).searchParams.get('source')),
    ).toContain('fotos')
  })
})
