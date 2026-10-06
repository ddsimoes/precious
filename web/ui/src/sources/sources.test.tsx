import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import type { Source } from '@/api/sources'
import { MockEventSource } from '@/test/eventSource'
import { fotosSource, pickerItem, scanEvent, usbSource } from '@/test/fixtures'
import { errorResponse, jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'

function sourceCard(name: string) {
  return within(screen.getByRole('article', { name }))
}

function commands(requests: Request[]) {
  return requests.filter((r) => new URL(r.url).pathname.startsWith('/api/commands/'))
}

const mnt = pickerItem('/mnt')
const home = pickerItem('/home/owner', { name: 'owner' })
const tank = pickerItem('/mnt/tank', { volume_label: 'tank' })
const fotos = pickerItem('/mnt/tank/fotos')
const existing = pickerItem('/mnt/tank/music', { is_source: true })

// pickerRoute answers GET /api/picker with the roots, and with a listing for
// each known handle.
function pickerRoute(listings: Record<string, () => Response>) {
  return (request: Request) => {
    const handle = new URL(request.url).searchParams.get('handle')
    if (handle === null) {
      return jsonResponse(200, { roots: [mnt, home] })
    }
    const listing = listings[handle]
    return listing === undefined ? errorResponse(400, 'invalid_request', 'bad handle') : listing()
  }
}

const listings = {
  [mnt.handle]: () => jsonResponse(200, { entry: mnt, children: [tank], truncated: false }),
  [tank.handle]: () => jsonResponse(200, { entry: tank, children: [fotos, existing], truncated: true }),
  [fotos.handle]: () => jsonResponse(200, { entry: fotos, children: [], truncated: false }),
}

describe('Sources screen', () => {
  it('lists each source with its state, volume, file system, totals, and last scan', async () => {
    stubApi({
      'GET /api/session': () => jsonResponse(200, signedIn),
      'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource(), usbSource()] }),
    })
    renderApp('/sources')

    await screen.findByRole('article', { name: 'Fotos' })
    const online = sourceCard('Fotos')
    expect(online.getByText('Online')).toBeInTheDocument()
    expect(online.getByText('/mnt/tank/fotos')).toBeInTheDocument()
    expect(online.getByText('zfs file system')).toBeInTheDocument()
    expect(online.getByText('Recognized even if the disk is mounted at another path.')).toBeInTheDocument()
    expect(online.getByText('120 GiB')).toBeInTheDocument()
    expect(online.getByText('410,000')).toBeInTheDocument()
    expect(online.getByText('31,000')).toBeInTheDocument()
    expect(online.getByText(/^Oct 1, 2026, 2:30\sPM$/u)).toBeInTheDocument()
    const fileSystem = online.getByRole('list', { name: 'File system' })
    expect(within(fileSystem).getByText('Writable')).toBeInTheDocument()
    expect(within(fileSystem).getByText('Modification times are precise to 1 nanosecond.')).toBeInTheDocument()
    expect(online.getByRole('button', { name: 'Scan now' })).toBeEnabled()
    expect(online.getByRole('link', { name: 'Open in Map' })).toHaveAttribute('href', '/map/1')

    const offline = sourceCard('Old disk')
    expect(offline.getByText('Offline')).toBeInTheDocument()
    expect(offline.getByText(/The disk is not connected\. Its last figures are shown/)).toBeInTheDocument()
    expect(offline.getByText('Backups on USB (not connected)')).toBeInTheDocument()
    expect(offline.getByText('USB (vfat file system)')).toBeInTheDocument()
    expect(
      offline.getByText(
        'Recognized only at its current location. If the disk is mounted at another path, Precious will not recognize it as this source.',
      ),
    ).toBeInTheDocument()
    expect(offline.getByText('5 GiB')).toBeInTheDocument()
    expect(offline.getByText('Not scanned yet')).toBeInTheDocument()
    const cautious = within(offline.getByRole('list', { name: 'File system' }))
    expect(
      cautious.getByText('Precious does not recognize this file system, so it uses cautious settings.'),
    ).toBeInTheDocument()
    expect(cautious.getByText('Modification times are precise to 2 seconds.')).toBeInTheDocument()
    expect(offline.getByRole('button', { name: 'Scan now' })).toBeDisabled()
    expect(offline.getByText('Connect the disk to scan it.')).toBeInTheDocument()
  })

  it("shows a source's own folder as its location, not its disk's mount point", async () => {
    const home = fotosSource({
      label: 'Home photos',
      mount_point: '/',
      path: '/home/owner/Fotos',
      rel_root: 'home/owner/Fotos',
      volume: { kind: 'uuid', id: '0b5e-77aa', label: null, fs_type: 'ext4', strong: true },
    })
    const unlabeled = usbSource({
      id: 'stick',
      label: 'Stick',
      rel_root: '',
      volume: { kind: 'uuid', id: '1234-ABCD', label: null, fs_type: 'vfat', strong: true },
    })
    stubApi({
      'GET /api/session': () => jsonResponse(200, signedIn),
      'GET /api/sources': () => jsonResponse(200, { sources: [home, unlabeled] }),
    })
    renderApp('/sources')

    const online = within(await screen.findByRole('article', { name: 'Home photos' }))
    expect(online.getByText('/home/owner/Fotos')).toBeInTheDocument()
    expect(online.queryByText('/')).not.toBeInTheDocument()
    // A disk without a label is named by its identity; its top folder is "/".
    expect(sourceCard('Stick').getByText('/ on 1234-ABCD (not connected)')).toBeInTheDocument()
  })

  it('adds a folder chosen in the picker, never sending a path', async () => {
    const added = fotosSource({ id: 'fotos', label: 'Holiday photos', last_scan_at: null })
    const requests = stubApi({
      'GET /api/session': () => jsonResponse(200, signedIn),
      'GET /api/sources': () => jsonResponse(200, { sources: [] }),
      'GET /api/picker': pickerRoute(listings),
      'POST /api/commands/add-source': () => jsonResponse(201, { source: added }),
    })
    renderApp('/sources')
    const user = userEvent.setup()

    expect(await screen.findByText('No sources yet. Add a folder to start.')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Add source' }))
    const dialog = within(await screen.findByRole('dialog', { name: 'Add a source' }))
    // No text box for a path: the picker starts at the allowed locations.
    expect(dialog.queryByRole('textbox')).not.toBeInTheDocument()

    await user.click(await dialog.findByRole('button', { name: /^mnt\s*\/mnt$/ }))
    await user.click(await dialog.findByRole('button', { name: 'tank' }))
    expect(await dialog.findByText('Only the first 2 folders are listed.')).toBeInTheDocument()
    expect(dialog.getByRole('button', { name: 'music Already a source' })).toBeInTheDocument()
    await user.click(dialog.getByRole('button', { name: 'fotos' }))

    expect(await dialog.findByText('This folder has no subfolders.')).toBeInTheDocument()
    expect(dialog.getByText('Selected folder: /mnt/tank/fotos')).toBeInTheDocument()
    const breadcrumb = within(dialog.getByRole('navigation', { name: 'Current folder' }))
    expect(breadcrumb.getByRole('button', { name: 'fotos' })).toHaveAttribute('aria-current', 'location')
    const name = dialog.getByLabelText('Name')
    expect(name).toHaveAttribute('placeholder', 'Default: fotos')
    await user.type(name, 'Holiday photos')
    await user.click(dialog.getByRole('button', { name: 'Add this folder' }))

    expect(await screen.findByRole('article', { name: 'Holiday photos' })).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent('“Holiday photos” was added. Choose Scan now')

    const [add] = commands(requests)
    expect(add?.headers.get('X-CSRF-Token')).toBe('session-token')
    expect(add?.headers.get('Idempotency-Key')).not.toBeNull()
    expect(await add?.json()).toEqual({ handle: fotos.handle, label: 'Holiday photos' })
    // Every picker request named a handle the server gave, or none.
    const handles = requests
      .filter((r) => new URL(r.url).pathname === '/api/picker')
      .map((r) => new URL(r.url).searchParams.get('handle'))
    expect(handles).toEqual([null, mnt.handle, tank.handle, fotos.handle])
    // Adding does not scan.
    expect(commands(requests)).toHaveLength(1)
  })

  it('names the folder after itself when no name is given', async () => {
    const requests = stubApi({
      'GET /api/session': () => jsonResponse(200, signedIn),
      'GET /api/sources': () => jsonResponse(200, { sources: [] }),
      'GET /api/picker': pickerRoute(listings),
      'POST /api/commands/add-source': () => jsonResponse(201, { source: fotosSource() }),
    })
    renderApp('/sources')
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: 'Add source' }))
    const dialog = within(await screen.findByRole('dialog'))
    await user.click(await dialog.findByRole('button', { name: /^mnt/ }))
    await user.click(await dialog.findByRole('button', { name: 'Add this folder' }))

    await screen.findByRole('article', { name: 'Fotos' })
    expect(await commands(requests)[0]?.json()).toEqual({ handle: mnt.handle })
  })

  it('explains a folder outside the allowed locations', async () => {
    stubApi({
      'GET /api/session': () => jsonResponse(200, signedIn),
      'GET /api/sources': () => jsonResponse(200, { sources: [] }),
      'GET /api/picker': pickerRoute({
        ...listings,
        [mnt.handle]: () => errorResponse(403, 'outside_allowed_roots', 'resolves to /etc'),
      }),
    })
    renderApp('/sources')
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: 'Add source' }))
    const dialog = within(await screen.findByRole('dialog'))
    await user.click(await dialog.findByRole('button', { name: /^mnt/ }))

    expect(await dialog.findByRole('alert')).toHaveTextContent(
      'This folder is outside the locations Precious may use. Choose a folder inside one of the listed locations.',
    )
  })

  it('explains an add refused as outside the allowed locations', async () => {
    stubApi({
      'GET /api/session': () => jsonResponse(200, signedIn),
      'GET /api/sources': () => jsonResponse(200, { sources: [] }),
      'GET /api/picker': pickerRoute(listings),
      'POST /api/commands/add-source': () => errorResponse(403, 'outside_allowed_roots', 'state directory'),
    })
    renderApp('/sources')
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: 'Add source' }))
    const dialog = within(await screen.findByRole('dialog'))
    await user.click(await dialog.findByRole('button', { name: /^mnt/ }))
    await user.click(await dialog.findByRole('button', { name: 'Add this folder' }))

    expect(await dialog.findByRole('alert')).toHaveTextContent('outside the locations Precious may use')
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('starts the picker over when its folder list is out of date', async () => {
    stubApi({
      'GET /api/session': () => jsonResponse(200, signedIn),
      'GET /api/sources': () => jsonResponse(200, { sources: [] }),
      'GET /api/picker': pickerRoute({}),
    })
    renderApp('/sources')
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: 'Add source' }))
    const dialog = within(await screen.findByRole('dialog'))
    await user.click(await dialog.findByRole('button', { name: /^owner/ }))

    expect(await dialog.findByRole('alert')).toHaveTextContent('This folder list is out of date')
    await user.click(dialog.getByRole('button', { name: 'Back to the locations' }))
    expect(await dialog.findByRole('button', { name: /^mnt/ })).toBeInTheDocument()
  })

  it('renames a source', async () => {
    const requests = stubApi({
      'GET /api/session': () => jsonResponse(200, signedIn),
      'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource()] }),
      'POST /api/commands/rename-source': async (request) => {
        const { label } = (await request.clone().json()) as { label: string }
        return jsonResponse(200, { source: fotosSource({ label }) })
      },
    })
    renderApp('/sources')
    const user = userEvent.setup()

    await screen.findByRole('article', { name: 'Fotos' })
    await user.click(sourceCard('Fotos').getByRole('button', { name: 'Rename' }))
    const input = sourceCard('Fotos').getByLabelText('Source name')
    await user.clear(input)
    await user.type(input, '  Family photos ')
    await user.click(sourceCard('Fotos').getByRole('button', { name: 'Save' }))

    expect(await screen.findByRole('article', { name: 'Family photos' })).toBeInTheDocument()
    expect(screen.queryByLabelText('Source name')).not.toBeInTheDocument()
    expect(await commands(requests)[0]?.json()).toEqual({ source_id: 'fotos', label: 'Family photos' })
  })

  it('removes a source only after confirmation, explaining a running scan', async () => {
    let attempts = 0
    const requests = stubApi({
      'GET /api/session': () => jsonResponse(200, signedIn),
      'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource(), usbSource()] }),
      'POST /api/commands/remove-source': () => {
        attempts++
        return attempts === 1
          ? errorResponse(409, 'job_active', 'scan 42 is still running')
          : jsonResponse(200, {})
      },
    })
    renderApp('/sources')
    const user = userEvent.setup()

    await screen.findByRole('article', { name: 'Fotos' })
    await user.click(sourceCard('Fotos').getByRole('button', { name: 'Remove' }))
    let confirm = within(await screen.findByRole('alertdialog', { name: 'Remove “Fotos”?' }))
    expect(confirm.getByText(/No file on the disk is changed or deleted\./)).toBeInTheDocument()
    await user.click(confirm.getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    expect(commands(requests)).toHaveLength(0)

    await user.click(sourceCard('Fotos').getByRole('button', { name: 'Remove' }))
    confirm = within(await screen.findByRole('alertdialog'))
    await user.click(confirm.getByRole('button', { name: 'Remove' }))
    expect(await confirm.findByRole('alert')).toHaveTextContent(
      'A scan of this source is still running. Wait for it to stop, then try again.',
    )
    expect(screen.getByRole('article', { name: 'Fotos' })).toBeInTheDocument()

    await user.click(confirm.getByRole('button', { name: 'Remove' }))
    await waitFor(() => expect(screen.queryByRole('article', { name: 'Fotos' })).not.toBeInTheDocument())
    expect(screen.getByRole('article', { name: 'Old disk' })).toBeInTheDocument()
    expect(await commands(requests)[1]?.json()).toEqual({ source_id: 'fotos' })
  })

  it('starts a scan and follows its progress live', async () => {
    let sources: Source[] = [fotosSource()]
    const requests = stubApi({
      'GET /api/session': () => jsonResponse(200, signedIn),
      'GET /api/sources': () => jsonResponse(200, { sources }),
      'POST /api/commands/start-scan': () =>
        jsonResponse(202, { job_id: '42', state: 'queued', coalesced: false }),
    })
    renderApp('/sources')
    const user = userEvent.setup()

    await screen.findByRole('article', { name: 'Fotos' })
    await user.click(sourceCard('Fotos').getByRole('button', { name: 'Scan now' }))

    const progress = await screen.findByRole('status', { name: 'Scan in progress' })
    expect(progress).toHaveTextContent('Waiting to start')
    expect(sourceCard('Fotos').getByRole('button', { name: 'Scan now' })).toBeDisabled()
    expect(await commands(requests)[0]?.json()).toEqual({ source_id: 'fotos' })

    const stream = MockEventSource.latest()
    act(() => stream.emit('job', scanEvent({ progress: { phase: 1, dirs: 1_200, files: 34_000, bytes: 3 * 1024 ** 3 } }), '5'))
    // The cache notifies the screen on its next tick.
    await waitFor(() => expect(progress).toHaveTextContent('Scanning'))
    expect(progress).toHaveTextContent('Folders1,200')
    expect(progress).toHaveTextContent('Files34,000')
    expect(progress).toHaveTextContent('Size3 GiB')

    act(() => stream.emit('job', scanEvent({ progress: { phase: 2, dirs: 2_000, files: 50_000, bytes: 4 * 1024 ** 3 } }), '6'))
    await waitFor(() => expect(progress).toHaveTextContent('Finishing'))
    expect(progress).toHaveTextContent('Files50,000')

    // When the scan ends, the list is read again with its new totals.
    sources = [fotosSource({ totals: { bytes: 4 * 1024 ** 3, files: 50_000, dirs: 2_000 } })]
    act(() => stream.emit('job', scanEvent({ state: 'succeeded', progress: {} }), '7'))
    await waitFor(() =>
      expect(screen.queryByRole('status', { name: 'Scan in progress' })).not.toBeInTheDocument(),
    )
    expect(await sourceCard('Fotos').findByText('50,000')).toBeInTheDocument()
    expect(requests.filter((r) => new URL(r.url).pathname === '/api/sources')).toHaveLength(2)
  })

  it('explains a scan refused because the disk is gone', async () => {
    stubApi({
      'GET /api/session': () => jsonResponse(200, signedIn),
      'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource()] }),
      'POST /api/commands/start-scan': () => errorResponse(409, 'source_offline', 'volume not mounted'),
    })
    renderApp('/sources')
    const user = userEvent.setup()

    await screen.findByRole('article', { name: 'Fotos' })
    await user.click(sourceCard('Fotos').getByRole('button', { name: 'Scan now' }))

    expect(await sourceCard('Fotos').findByRole('alert')).toHaveTextContent(
      'The disk of this source is not connected. Connect it, then try again.',
    )
  })
})
