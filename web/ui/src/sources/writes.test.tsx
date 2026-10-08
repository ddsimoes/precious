import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import type { Source } from '@/api/sources'
import { fotosSource } from '@/test/fixtures'
import { jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'

function writesRow(card: string) {
  return within(within(screen.getByRole('article', { name: card })).getByRole('region', { name: 'Changes by Precious' }))
}

function commands(requests: Request[]) {
  return requests.filter((r) => new URL(r.url).pathname.startsWith('/api/commands/'))
}

describe('Changes by Precious on the Sources screen', () => {
  it('R3.7: cancelling the confirmation sends no command and leaves changes off', async () => {
    const requests = stubApi({
      'GET /api/session': () => jsonResponse(200, signedIn),
      'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource()] }),
    })
    renderApp('/sources')
    await screen.findByRole('article', { name: 'Fotos' })
    const row = writesRow('Fotos')
    expect(row.getByText('Off: Precious only reads this source.')).toBeInTheDocument()

    await userEvent.click(row.getByRole('button', { name: 'Allow changes…' }))
    const dialog = within(await screen.findByRole('alertdialog', { name: 'Allow changes on “Fotos”?' }))
    expect(
      dialog.getByText(
        'Precious will then move, rename, and create folders on this source, only when you ask. It never replaces a file, and keeps a history of every change, which you can undo.',
      ),
    ).toBeInTheDocument()
    await userEvent.click(dialog.getByRole('button', { name: 'Cancel' }))

    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    expect(commands(requests)).toEqual([])
    expect(writesRow('Fotos').getByText('Off: Precious only reads this source.')).toBeInTheDocument()
  })

  it('allows changes only after confirming, and turns them off without asking', async () => {
    let source = fotosSource()
    const bodies: unknown[] = []
    stubApi({
      'GET /api/session': () => jsonResponse(200, signedIn),
      'GET /api/sources': () => jsonResponse(200, { sources: [source] }),
      'POST /api/commands/set-source-writes': async (request) => {
        const body = (await request.json()) as { enabled: boolean }
        bodies.push(body)
        source = { ...source, writes: { enabled: body.enabled, unavailable: null } }
        return jsonResponse(200, { source })
      },
    })
    renderApp('/sources')
    await screen.findByRole('article', { name: 'Fotos' })

    await userEvent.click(writesRow('Fotos').getByRole('button', { name: 'Allow changes…' }))
    await userEvent.click(
      within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Allow changes' }),
    )
    await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument())
    expect(
      writesRow('Fotos').getByText('On: Precious moves, renames, and creates folders here when you ask.'),
    ).toBeInTheDocument()
    expect(bodies).toEqual([{ source_id: 'fotos', enabled: true }])

    await userEvent.click(writesRow('Fotos').getByRole('button', { name: 'Turn off' }))
    expect(await writesRow('Fotos').findByText('Off: Precious only reads this source.')).toBeInTheDocument()
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    expect(bodies).toEqual([
      { source_id: 'fotos', enabled: true },
      { source_id: 'fotos', enabled: false },
    ])
  })

  it('R3.7: a source where changes cannot be allowed shows why, with no control', async () => {
    const unavailable = (id: string, label: string, reason: Source['writes']['unavailable']) =>
      fotosSource({ id, label, root_entry_id: id, writes: { enabled: false, unavailable: reason } })
    const requests = stubApi({
      'GET /api/session': () => jsonResponse(200, signedIn),
      'GET /api/sources': () =>
        jsonResponse(200, {
          sources: [
            unavailable('a', 'Configured', 'forbidden_by_config'),
            unavailable('b', 'Mounted read-only', 'read_only'),
            unavailable('c', 'Old NTFS', 'no_replace_rename'),
          ],
        }),
    })
    renderApp('/sources')
    await screen.findByRole('article', { name: 'Configured' })

    const reasons: Record<string, string> = {
      Configured: 'Off: the server’s configuration does not allow changes on any source.',
      'Mounted read-only': 'Off: the disk is mounted read-only, so Precious cannot change anything on it.',
      'Old NTFS':
        'Off: this file system cannot rename without the risk of replacing a file, so Precious does not change it.',
    }
    for (const [card, reason] of Object.entries(reasons)) {
      const row = writesRow(card)
      expect(row.getByText(reason)).toBeInTheDocument()
      expect(row.queryByRole('button')).not.toBeInTheDocument()
    }
    expect(commands(requests)).toEqual([])
  })
})
