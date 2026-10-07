import { screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import type { ArchiveNote } from '@/api/content'
import type { EntryDetail } from '@/api/entries'
import { entryDetail, entryRow, fotosSource } from '@/test/fixtures'
import { jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'

// The detail panel of archives that were not opened (r2b design D10).

async function panelOf(
  name: string,
  note: ArchiveNote | null,
  overrides: Parameters<typeof entryRow>[0] = {},
  detail: Partial<EntryDetail> = {},
) {
  const row = entryRow({ id: '30', name, path: `Backups/${name}`, file_kind: 'archive', ...overrides })
  stubApi({
    'GET /api/session': () => jsonResponse(200, signedIn),
    'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource()] }),
    'GET /api/tags': () => jsonResponse(200, { tags: [] }),
    [`GET /api/entries/${row.id}`]: () => jsonResponse(200, entryDetail(row, { archive_note: note, ...detail })),
  })
  renderApp(`/search?entry=${row.id}`)
  return within(await screen.findByRole('complementary', { name }))
}

describe('Archives that were not opened', () => {
  // The archive-contents scenario "A 7z file".
  it('says that 7z archives are not opened, so their content is not checked', async () => {
    const panel = await panelOf('backup.7z', 'unsupported')
    const archive = within(panel.getByRole('region', { name: 'Archive' }))
    expect(
      archive.getByText('7z archives are not opened, so what is inside is not checked for copies.'),
    ).toBeInTheDocument()
    // Nothing to open as a folder: the viewer's Open is the main action.
    expect(panel.queryByRole('link', { name: 'Open as a folder' })).not.toBeInTheDocument()
    expect(panel.getByRole('button', { name: 'Open' })).not.toHaveClass('border')
  })

  it('says that an archive inside an archive is not opened', async () => {
    const panel = await panelOf('plugins.zip', 'nested', { id: 'm80', archive_id: '61' })
    expect(within(panel.getByRole('region', { name: 'Archive' })).getByText(/^This archive is inside another archive\./))
      .toHaveTextContent('so what is inside is not checked for copies.')
  })

  it('says that an archive not listed yet was not read', async () => {
    const panel = await panelOf('fotos.tar.gz', 'not_listed')
    expect(
      within(panel.getByRole('region', { name: 'Archive' })).getByText(
        'This archive has not been read yet, so what is inside is not checked for copies.',
      ),
    ).toBeInTheDocument()
  })

  it('says that an archive opened without its listing is not checked', async () => {
    const panel = await panelOf('segredo.zip', null, {}, {
      archive: { format: 'zip', state: 'encrypted', detail: null, members: 0, unpacked_bytes: 0 },
    })
    const archive = within(panel.getByRole('region', { name: 'Archive' }))
    expect(archive.getByText('Encrypted, not opened')).toBeInTheDocument()
    expect(archive.getByText('What is inside is not checked for copies.')).toBeInTheDocument()
    expect(panel.queryByRole('link', { name: 'Open as a folder' })).not.toBeInTheDocument()
  })

  it('has no archive section for a file that is no archive', async () => {
    const panel = await panelOf('notas.txt', null, { file_kind: 'document' })
    expect(panel.queryByRole('region', { name: 'Archive' })).not.toBeInTheDocument()
  })
})
