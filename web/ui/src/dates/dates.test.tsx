import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import type { Camera, MediaDate } from '@/api/dates'
import type { Item } from '@/api/organize'
import { MockEventSource } from '@/test/eventSource'
import {
  action,
  actionItem,
  camera,
  dateJSON,
  datesSummary,
  entryDates,
  entryDetail,
  entryRow,
  folderRow,
  fotosSource,
  mediaDate,
} from '@/test/fixtures'
import { errorResponse, jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'

type Route = (request: Request) => Response | Promise<Response>

const fotos = fotosSource({ writes: { enabled: true, unavailable: null } })

function routes(
  list: (params: URLSearchParams) => MediaDate[],
  cameras: () => Camera[],
  extra: Record<string, Route> = {},
): Record<string, Route> {
  return {
    'GET /api/session': () => jsonResponse(200, signedIn),
    'GET /api/sources': () => jsonResponse(200, { sources: [fotos] }),
    'GET /api/dates/summary': () => jsonResponse(200, datesSummary()),
    'GET /api/dates/cameras': () => jsonResponse(200, { items: cameras() }),
    'GET /api/dates': (request) =>
      jsonResponse(200, { items: list(new URL(request.url).searchParams), next_cursor: null }),
    ...extra,
  }
}

function commandRequests(requests: Request[], name: string) {
  return requests.filter((r) => r.method === 'POST' && new URL(r.url).pathname === `/api/commands/${name}`)
}

function bodies(requests: Request[], name: string) {
  return Promise.all(commandRequests(requests, name).map((r) => r.clone().json() as Promise<unknown>))
}

// text reads an element's text with every kind of space as a plain one:
// Intl puts a narrow no-break space before AM and PM.
function text(element: Element) {
  return (element.textContent ?? '').replace(/\s/g, ' ')
}

// lines are the texts of a list's own items, without those of lists inside
// them.
function lines(list: HTMLElement) {
  return within(list)
    .getAllByRole('listitem')
    .filter((li) => li.parentElement === list)
    .map((li) => text(li))
}

// The R5.2 corpus: the Sony's 12 photos, its clock 1 year 3 hours behind,
// in the event folders Bahia and Natal, and one more of its photos in a
// subfolder of Bahia, which the shift does not take.
const sonyKey = 'SONY|DSC-W55|'
const bahia = { id: '812', path: 'Viagens/2010-07 Bahia', path_b64: btoa('Viagens/2010-07 Bahia') }
const natal = { id: '813', path: 'Viagens/2010-12 Natal', path_b64: btoa('Viagens/2010-12 Natal') }
const shiftS = 31_546_800
const sonyRef = { key: sonyKey, make: 'SONY', model: 'DSC-W55', serial: '' }

function sonyPhoto(id: string, path: string, local: string, shifted: boolean): MediaDate {
  const date = new Date(`${local}Z`)
  if (shifted) {
    date.setTime(date.getTime() + shiftS * 1000)
  }
  const iso = date.toISOString().replace('.000Z', 'Z')
  return mediaDate(id, path, {
    date: dateJSON(
      shifted
        ? { instant: iso, local: iso.slice(0, 19), source: 'owner', confidence: 'medium', corrected: 'shift' }
        : { instant: iso, local },
    ),
    flags: shifted ? [] : ['camera_offset'],
    camera: sonyRef,
    correction: shifted ? { kind: 'shift', shift_s: shiftS, created_at: '2026-10-08T10:00:00Z' } : null,
  })
}

// The Sony's clock times in each event: the true times minus 365 days and
// 3 hours (design D19).
const sonyShots = {
  [bahia.id]: { prefix: 'DSC003', day: '2009-07-17', first: 301, times: ['07:00', '07:30', '08:00', '09:00', '09:30', '10:00'] },
  [natal.id]: { prefix: 'DSC004', day: '2009-12-24', first: 401, times: ['16:00', '16:30', '17:00', '18:00', '18:30', '19:00'] },
}

function sonyPhotos(folder: typeof bahia, shifted: boolean): MediaDate[] {
  const { prefix, day, first, times } = sonyShots[folder.id]!
  return times.map((time, index) =>
    sonyPhoto(
      String(first + index),
      `${folder.path}/${prefix}${String(index + 1).padStart(2, '0')}.JPG`,
      `${day}T${time}:00`,
      shifted,
    ),
  )
}

const sonyOffset = camera(sonyKey, {
  photos: 12,
  state: 'offset',
  suggested_shift_s: shiftS,
  events: [
    { folder: bahia, delta_s: -shiftS, photos: 6, reference: 'gps' },
    { folder: natal, delta_s: -shiftS, photos: 6, reference: 'gps' },
  ],
})
const canon = camera('Canon|Canon PowerShot SX230 HS|', { photos: 8 })

describe('The Dates screen', () => {
  it('shows the totals, the time zone, and the media job live, and is in the main menu', async () => {
    stubApi(
      routes(
        () => [],
        () => [],
        { 'GET /api/dates/summary': () => jsonResponse(200, datesSummary({ time_zone: 'UTC', time_zone_set: false })) },
      ),
    )
    renderApp('/dates?source=fotos')

    expect(await screen.findByRole('link', { name: 'Dates', current: 'page' })).toHaveAttribute('href', '/dates')
    expect(await screen.findByText('562 photos and videos')).toBeInTheDocument()
    expect(screen.getByText(/No time zone is set on the server/)).toHaveTextContent('server’s zone, UTC')
    expect(lines(screen.getByRole('list', { name: 'Camera information' }))).toEqual([
      'Not read yet: 0',
      'Read: 120',
      'Not read: no camera information in this file type: 440',
      'Could not be read: 2',
    ])
    expect(lines(screen.getByRole('list', { name: 'To check' }))).toEqual([
      'Modification time disagrees: 3',
      'Camera date not plausible: 1',
      'Camera clock off: 12',
      'No date in the file: 440',
    ])

    MockEventSource.latest().open()
    MockEventSource.latest().emit(
      'job',
      {
        job_id: '70',
        kind: 'media',
        source_id: 'fotos',
        state: 'running',
        cancel_requested: false,
        progress: { phase: 2, files: 40, of_files: 120, bytes: 4 * 1024 ** 2, changed: 0, unreadable: 1 },
        attempts: 1,
      },
      '1',
    )
    const progress = await screen.findByRole('status', { name: 'Reading photos and videos' })
    expect(text(progress)).toContain('Reading photos and videos · Reading camera information')
    expect(text(progress)).toContain('Read 40 of 120 files (4 MiB)')
    expect(text(progress)).toContain('Could not be read: 1')
  })

  it('R5.2 A camera’s suggested shift in one confirmation', async () => {
    let shifted = false
    const requests = stubApi(
      routes(
        (params) => {
          if (params.get('camera') === sonyKey && params.get('within') === bahia.id) {
            const extra = sonyPhoto('399', `${bahia.path}/extra/DSC09999.JPG`, '2009-07-18T07:00:00', shifted)
            return [...sonyPhotos(bahia, shifted), extra]
          }
          if (params.get('camera') === sonyKey && params.get('within') === natal.id) {
            return sonyPhotos(natal, shifted)
          }
          return [...sonyPhotos(bahia, shifted), ...sonyPhotos(natal, shifted)]
        },
        () => (shifted ? [{ ...sonyOffset, state: 'ok', suggested_shift_s: null, events: [] }, canon] : [sonyOffset, canon]),
        {
          'POST /api/commands/set-date-correction': () => {
            shifted = true
            return jsonResponse(200, { applied: 12, skipped_count: 0, skipped: [], batch_id: 'b1' })
          },
        },
      ),
    )
    renderApp('/dates?source=fotos')

    const sony = within(await screen.findByRole('article', { name: 'SONY DSC-W55' }))
    expect(sony.getByText('Its clock looks off by +1 year 3 hours.')).toBeInTheDocument()
    expect(lines(sony.getByRole('list', { name: 'Compared in' }))).toEqual([
      'Viagens/2010-07 Bahia: 6 photos, −1 year 3 hours from the other cameras; the other cameras’ GPS agrees with them',
      'Viagens/2010-12 Natal: 6 photos, −1 year 3 hours from the other cameras; the other cameras’ GPS agrees with them',
    ])
    const canonCard = within(screen.getByRole('article', { name: 'Canon PowerShot SX230 HS' }))
    expect(canonCard.getByText('Its clock agrees with the other cameras.')).toBeInTheDocument()
    expect(canonCard.queryByRole('button')).not.toBeInTheDocument()

    await userEvent.click(sony.getByRole('button', { name: 'Shift +1 year 3 hours — 12 photos' }))
    const dialog = within(
      await screen.findByRole('alertdialog', { name: 'Shift the photos of SONY DSC-W55 by +1 year 3 hours' }),
    )
    const photos = lines(await dialog.findByRole('list', { name: 'Photos to shift' }))
    expect(photos).toHaveLength(12)
    expect(photos[0]).toBe('Viagens/2010-07 Bahia/DSC00301.JPG: Jul 17, 2009, 7:00:00 AM → Jul 17, 2010, 10:00:00 AM')
    expect(photos[11]).toBe('Viagens/2010-12 Natal/DSC00406.JPG: Dec 24, 2009, 7:00:00 PM → Dec 24, 2010, 10:00:00 PM')
    expect(photos.some((line) => line.includes('DSC09999'))).toBe(false)
    expect(commandRequests(requests, 'set-date-correction')).toHaveLength(0)

    await userEvent.click(dialog.getByRole('button', { name: 'Shift 12 photos' }))
    expect(await screen.findByText('12 dates corrected.')).toBeInTheDocument()
    expect(await bodies(requests, 'set-date-correction')).toEqual([
      { folder_ids: [bahia.id, natal.id], camera_key: sonyKey, correction: { kind: 'shift', shift_s: shiftS } },
    ])

    // The photos show their new dates without a reload, and the Sony no
    // longer has a suggestion.
    const list = await screen.findByRole('list', { name: 'Photos and videos' })
    await waitFor(() =>
      expect(lines(list)[0]).toContain(
        'Jul 17, 2010, 10:00:00 AM · Your correction, shifted by you · Fairly sure',
      ),
    )
    expect(lines(list)[0]).not.toContain('Camera clock off')
    await waitFor(() =>
      expect(within(screen.getByRole('article', { name: 'SONY DSC-W55' })).queryByRole('button')).not.toBeInTheDocument(),
    )
  })

  it('R5.4 Setting file dates from the screen', async () => {
    const ouroPreto = folderRow('50', '2008-03 Ouro Preto', { path: 'Viagens/2008-03 Ouro Preto' })
    const path = (n: number) => `Viagens/2008-03 Ouro Preto/DSCN000${n}.JPG`
    const capture = (minute: string) =>
      dateJSON({ instant: `2008-03-22T17:${minute}:00Z`, local: `2008-03-22T14:${minute}:00`, offset_min: -180, confidence: 'high' })
    const rows = [
      mediaDate('301', path(1), { date: capture('00'), flags: ['mtime_disagrees'] }),
      mediaDate('302', path(2), { date: capture('10'), flags: ['mtime_disagrees'] }),
      mediaDate('303', path(3), { date: capture('20'), flags: ['mtime_disagrees'] }),
      mediaDate('304', path(4), {
        date: dateJSON({
          instant: '2008-03-01T00:00:00Z',
          local: '2008-03',
          precision: 'month',
          source: 'folder_name',
          confidence: 'low',
        }),
        flags: ['implausible'],
      }),
    ]
    const planned = action(
      { id: '90', kind: 'set_mtime', bulk: true, files: 3, bytes: 6 * 1024 ** 2 },
      { planned: 3, refused: 1 },
    )
    const item = (id: string, n: number, overrides: Partial<Item>): Item =>
      actionItem(id, path(n), path(n), { op: 'set_mtime', to: null, ...overrides })
    const items = [
      item('1', 1, { mtime: { from: '2011-01-15T10:00:00Z', to: '2008-03-22T17:00:00.12Z' } }),
      item('2', 2, { mtime: { from: '2011-01-15T10:00:00Z', to: '2008-03-22T17:10:00.34Z' } }),
      item('3', 3, { mtime: { from: '2011-01-15T10:00:00Z', to: '2008-03-22T17:20:00.56Z' } }),
      item('4', 4, { state: 'refused', reason: 'date_too_coarse', mtime: null }),
    ]
    const requests = stubApi(
      routes(
        () => rows,
        () => [],
        {
          'GET /api/entries/50': () => jsonResponse(200, entryDetail(ouroPreto)),
          'POST /api/commands/plan-set-mtime': () =>
            jsonResponse(201, { action: planned, items, next_cursor: null, summary: { unchanged: 0 } }),
          'POST /api/commands/run-action': () =>
            jsonResponse(202, { action: { ...planned, state: 'queued' }, job_id: '9', state: 'queued' }),
          'GET /api/history/90': () => jsonResponse(200, { ...planned, state: 'running' }),
        },
      ),
    )
    renderApp('/dates?source=fotos&within=50')

    expect(await screen.findByText('Fotos / Viagens / 2008-03 Ouro Preto')).toBeInTheDocument()
    const list = await screen.findByRole('list', { name: 'Photos and videos' })
    expect(lines(list)[0]).toContain('Mar 22, 2008, 2:00:00 PM UTC−03:00 · Camera · Sure')
    expect(lines(list)[0]).toContain('Modified Jan 15, 2011, 10:00:00 AM')
    expect(lines(list)[0]).toContain('Modification time disagrees')
    expect(lines(list)[3]).toContain('March 2008 · Folder name · Unsure')

    await userEvent.click(screen.getByRole('button', { name: 'Select all shown' }))
    expect(screen.getByText('4 selected')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Set file dates…' }))
    const dialog = within(await screen.findByRole('dialog', { name: 'Set file dates' }))
    expect(dialog.getByRole('radio', { name: 'The 4 selected photos and videos' })).toBeChecked()
    await userEvent.click(dialog.getByRole('button', { name: 'Show the preview' }))

    const preview = within(await screen.findByRole('alertdialog', { name: 'Set file dates' }))
    expect(await bodies(requests, 'plan-set-mtime')).toEqual([{ entry_ids: ['301', '302', '303', '304'] }])
    expect(lines(preview.getByRole('list', { name: 'Items' }))).toEqual([
      '3 items will be changed',
      '1 item is not included',
      'In total: 3 files, 6 MiB',
    ])
    expect(lines(preview.getByRole('list', { name: 'Will be changed' }))).toEqual([
      `${path(1)}: Jan 15, 2011, 10:00:00 AM → Mar 22, 2008, 5:00:00 PM`,
      `${path(2)}: Jan 15, 2011, 10:00:00 AM → Mar 22, 2008, 5:10:00 PM`,
      `${path(3)}: Jan 15, 2011, 10:00:00 AM → Mar 22, 2008, 5:20:00 PM`,
    ])
    expect(lines(preview.getByRole('list', { name: 'Not included' }))).toEqual([
      `${path(4)}Its date is not known precisely enough`,
    ])
    // Nothing runs before the owner confirms.
    expect(commandRequests(requests, 'run-action')).toHaveLength(0)

    await userEvent.click(preview.getByRole('button', { name: 'Confirm' }))
    expect(await screen.findByText('Setting file dates…')).toBeInTheDocument()
    expect(await bodies(requests, 'run-action')).toEqual([{ action_id: '90' }])
  })

  it('R5.5 Organizing by date shows the copies, the siblings, and discards copies only once confirmed', async () => {
    const viagens = folderRow('20', 'Viagens')
    const celular = folderRow('21', 'celular_2011')
    const fotosFolder = folderRow('30', 'Fotos')
    const wa = 'celular_2011/WhatsApp/Media/WhatsApp Images/IMG-20110416-WA0003.jpg'
    const sent = 'celular_2011/WhatsApp/Media/WhatsApp Images/Sent/IMG-20110416-WA0003.jpg'
    const planned = action(
      { id: '91', kind: 'date_organize', bulk: true, destination: fotosFolder, template: '{year}/{month}', files: 3 },
      { planned: 5, refused: 1 },
    )
    const items: Item[] = [
      actionItem('1', '', 'Fotos/2010', { op: 'mkdir', from: null }),
      actionItem('2', '', 'Fotos/2010/07', { op: 'mkdir', from: null }),
      actionItem('3', 'Viagens/2010-07 Bahia/IMG_0101.JPG', 'Fotos/2010/07/IMG_0101.JPG', {
        detail: 'IMG_0101.CR2',
      }),
      actionItem('4', 'Viagens/2010-07 Bahia/do celular da Ana/IMG_0102.JPG', 'Fotos/2010/07/IMG_0102 (1).JPG'),
      actionItem('5', wa, 'Fotos/2011/04/IMG-20110416-WA0003.jpg'),
      actionItem('6', sent, 'Fotos/2011/04/IMG-20110416-WA0003.jpg', {
        state: 'refused',
        reason: 'identical_copy',
        entry: folderRow('411', 'IMG-20110416-WA0003.jpg', { kind: 'file', path: sent }),
        copy_of: { entry: '410', path: wa, path_b64: btoa(wa) },
      }),
    ]
    const requests = stubApi(
      routes(
        () => [],
        () => [],
        {
          'GET /api/entries/20': () => jsonResponse(200, entryDetail(viagens)),
          'GET /api/entries/1/children': () =>
            jsonResponse(200, { items: [viagens, celular, fotosFolder], next_cursor: null }),
          'GET /api/entries/21/children': () => jsonResponse(200, { items: [], next_cursor: null }),
          'GET /api/entries/30/children': () => jsonResponse(200, { items: [], next_cursor: null }),
          'POST /api/commands/plan-date-organize': () =>
            jsonResponse(201, {
              action: planned,
              items,
              next_cursor: null,
              summary: { files_with_copies: 2, split_siblings: 1 },
            }),
          'GET /api/history/91/items': (request) => {
            const refusedOnly = new URL(request.url).searchParams.getAll('state').includes('refused')
            return jsonResponse(200, {
              items: refusedOnly ? items.filter((i) => i.state === 'refused') : items,
              next_cursor: null,
            })
          },
          'POST /api/commands/set-decision': () =>
            jsonResponse(200, { applied: 1, skipped_count: 0, skipped: [] }),
        },
      ),
    )
    renderApp('/dates?source=fotos&within=20')

    expect(await screen.findByText('Fotos / Viagens')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Organize by date…' }))
    const dialog = within(await screen.findByRole('dialog', { name: 'Organize by date' }))
    expect(dialog.getByRole('radio', { name: 'Every photo and video in these folders' })).toBeChecked()
    expect(dialog.getByRole('textbox', { name: 'Folders' })).toHaveValue('{year}/{month}')

    await userEvent.click(dialog.getByRole('button', { name: 'Add a folder…' }))
    const adding = within(await screen.findByRole('dialog', { name: 'Add a folder' }))
    await userEvent.click(await adding.findByRole('button', { name: 'celular_2011' }))
    await userEvent.click(adding.getByRole('button', { name: 'Add this folder' }))
    expect(lines(dialog.getByRole('list', { name: 'Folders' }))).toEqual(['Fotos / Viagens×', 'Fotos / celular_2011×'])

    await userEvent.click(dialog.getByRole('button', { name: 'Choose a folder…' }))
    const into = within(await screen.findByRole('dialog', { name: 'Organize into…' }))
    await userEvent.click(within(await into.findByRole('list', { name: 'Folders' })).getByRole('button', { name: 'Fotos' }))
    await userEvent.click(into.getByRole('button', { name: 'Organize here' }))
    expect(dialog.getByText('Fotos / Fotos')).toBeInTheDocument()
    await userEvent.click(dialog.getByRole('button', { name: 'Show the preview' }))

    const preview = within(await screen.findByRole('alertdialog', { name: 'Organize by date into “Fotos”' }))
    expect(await bodies(requests, 'plan-date-organize')).toEqual([
      { folder_ids: ['20', '21'], destination_id: '30', template: '{year}/{month}', rename: false },
    ])
    expect(preview.getByText(/Remove the copies first/)).toBeInTheDocument()
    expect(preview.getByRole('link', { name: 'duplicates list' })).toHaveAttribute(
      'href',
      '/opportunities/duplicates?source=fotos',
    )
    expect(
      preview.getByText(/1 file leaves a file of the same name behind in its folder, such as a RAW file/),
    ).toBeInTheDocument()
    expect(lines(preview.getByRole('list', { name: 'Will be changed' }))).toEqual([
      'New folder: Fotos/2010',
      'New folder: Fotos/2010/07',
      'Viagens/2010-07 Bahia/IMG_0101.JPG → Fotos/2010/07/IMG_0101.JPGLeaves behind: IMG_0101.CR2',
      'Viagens/2010-07 Bahia/do celular da Ana/IMG_0102.JPG → Fotos/2010/07/IMG_0102 (1).JPG',
      `${wa} → Fotos/2011/04/IMG-20110416-WA0003.jpg`,
    ])
    expect(lines(preview.getByRole('list', { name: 'Not included' }))).toEqual([
      `${sent} → Fotos/2011/04/IMG-20110416-WA0003.jpgAn identical copy already takes that nameSame content as ${wa}`,
    ])

    expect(await preview.findByText('1 identical copy stays where it is.')).toBeInTheDocument()
    await userEvent.click(preview.getByRole('button', { name: 'Discard these copies' }))
    const confirm = within(await screen.findByRole('alertdialog', { name: 'Discard 1 identical copy?' }))
    expect(commandRequests(requests, 'set-decision')).toHaveLength(0)
    await userEvent.click(confirm.getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('alertdialog', { name: 'Discard 1 identical copy?' })).not.toBeInTheDocument()
    expect(commandRequests(requests, 'set-decision')).toHaveLength(0)

    await userEvent.click(preview.getByRole('button', { name: 'Discard these copies' }))
    await userEvent.click(
      within(await screen.findByRole('alertdialog', { name: 'Discard 1 identical copy?' })).getByRole('button', {
        name: 'Discard them',
      }),
    )
    expect(await preview.findByText('1 copy discarded.')).toBeInTheDocument()
    expect(await bodies(requests, 'set-decision')).toEqual([{ entry_ids: ['411'], decision: 'discard' }])
    expect(commandRequests(requests, 'run-action')).toHaveLength(0)
  })

  it('corrects the selected dates in bulk and reports what it skipped', async () => {
    const rows = [
      mediaDate('501', 'celular_2011/WhatsApp/Media/WhatsApp Images/IMG-20110416-WA0003.jpg'),
      mediaDate('502', 'Fotos/2004/scan.jpg'),
    ]
    const requests = stubApi(
      routes(
        () => rows,
        () => [],
        {
          'POST /api/commands/set-date-correction': () =>
            jsonResponse(200, {
              applied: 1,
              skipped_count: 1,
              skipped: [{ entry_id: '502', path: 'Fotos/2004/scan.jpg', path_b64: '', reason: 'no_name_date' }],
              batch_id: 'b2',
            }),
        },
      ),
    )
    renderApp('/dates?source=fotos')

    await screen.findByRole('list', { name: 'Photos and videos' })
    await userEvent.click(screen.getByRole('checkbox', { name: `Select ${rows[0]!.entry.path}` }))
    await userEvent.click(screen.getByRole('checkbox', { name: `Select ${rows[1]!.entry.path}` }))
    await userEvent.click(screen.getByRole('button', { name: 'Correct dates…' }))
    const dialog = within(await screen.findByRole('dialog', { name: 'Correct dates' }))
    await userEvent.click(dialog.getByRole('radio', { name: 'Shift the date' }))
    await userEvent.type(dialog.getByRole('spinbutton', { name: 'Years' }), '1')
    await userEvent.type(dialog.getByRole('spinbutton', { name: 'Hours' }), '3')
    await userEvent.selectOptions(dialog.getByRole('combobox', { name: 'Move the dates' }), 'Earlier')
    await userEvent.click(dialog.getByRole('radio', { name: 'Take the date in the file name' }))
    await userEvent.click(dialog.getByRole('button', { name: 'Apply' }))

    expect(await dialog.findByText('1 date corrected.')).toBeInTheDocument()
    expect(dialog.getByText('Fotos/2004/scan.jpg (no date in its name)')).toBeInTheDocument()
    expect(await bodies(requests, 'set-date-correction')).toEqual([
      { entry_ids: ['501', '502'], correction: { kind: 'use_name' } },
    ])
  })

  it('sends a shift in seconds, a year counting 365 days, and a set date at its precision', async () => {
    const requests = stubApi(
      routes(
        () => [mediaDate('601', 'Fotos/a.jpg')],
        () => [],
        {
          'POST /api/commands/set-date-correction': () =>
            jsonResponse(200, { applied: 1, skipped_count: 0, skipped: [], batch_id: 'b3' }),
        },
      ),
    )
    renderApp('/dates?source=fotos')

    await userEvent.click(await screen.findByRole('checkbox', { name: 'Select Fotos/a.jpg' }))
    await userEvent.click(screen.getByRole('button', { name: 'Correct dates…' }))
    let dialog = within(await screen.findByRole('dialog', { name: 'Correct dates' }))
    await userEvent.click(dialog.getByRole('radio', { name: 'Shift the date' }))
    await userEvent.selectOptions(dialog.getByRole('combobox', { name: 'Move the dates' }), 'Earlier')
    await userEvent.type(dialog.getByRole('spinbutton', { name: 'Years' }), '1')
    await userEvent.type(dialog.getByRole('spinbutton', { name: 'Days' }), '2')
    await userEvent.type(dialog.getByRole('spinbutton', { name: 'Hours' }), '3')
    await userEvent.type(dialog.getByRole('spinbutton', { name: 'Minutes' }), '4')
    await userEvent.click(dialog.getByRole('button', { name: 'Apply' }))
    await dialog.findByText('1 date corrected.')
    await userEvent.click(dialog.getByRole('button', { name: 'Close' }))

    await userEvent.click(screen.getByRole('button', { name: 'Correct dates…' }))
    dialog = within(await screen.findByRole('dialog', { name: 'Correct dates' }))
    await userEvent.selectOptions(dialog.getByRole('combobox', { name: 'Known to' }), 'The year')
    await userEvent.type(dialog.getByRole('spinbutton', { name: 'Year' }), '1978')
    await userEvent.click(dialog.getByRole('button', { name: 'Apply' }))
    await dialog.findByText('1 date corrected.')

    expect(await bodies(requests, 'set-date-correction')).toEqual([
      { entry_ids: ['601'], correction: { kind: 'shift', shift_s: -(365 * 86_400 + 2 * 86_400 + 3 * 3_600 + 4 * 60) } },
      { entry_ids: ['601'], correction: { kind: 'set', local: '1978' } },
    ])
  })

  it('asks for a source when there are several', async () => {
    stubApi({
      ...routes(
        () => [],
        () => [],
      ),
      'GET /api/sources': () =>
        jsonResponse(200, { sources: [fotos, fotosSource({ id: 'other', label: 'Other', root_entry_id: '2' })] }),
    })
    renderApp('/dates')
    expect(await screen.findByText(/Choose a source at the top/)).toBeInTheDocument()
    expect(await screen.findByText('562 photos and videos')).toBeInTheDocument()
    expect(screen.queryByRole('list', { name: 'Photos and videos' })).not.toBeInTheDocument()
  })

  it('previews a camera’s shift as the server applies it, and counts only the photos it moves', async () => {
    const path = (n: number) => `${bahia.path}/DSC0070${n}.JPG`
    const created_at = '2026-10-08T10:00:00Z'
    // Flagged, no correction: its date is the camera's.
    const plain = sonyPhoto('701', path(1), '2009-07-17T07:00:00', false)
    // Flagged, shifted by the owner by an hour: the camera said 07:30.
    const shifted = mediaDate('702', path(2), {
      date: dateJSON({ instant: '2009-07-17T08:30:00Z', local: '2009-07-17T08:30:00', source: 'owner', corrected: 'shift' }),
      flags: ['camera_offset'],
      camera: sonyRef,
      correction: { kind: 'shift', shift_s: 3_600, created_at },
    })
    // Set by the owner: it keeps its date.
    const set = mediaDate('703', path(3), {
      date: dateJSON({ instant: '2010-07-20T12:00:00Z', local: '2010-07-20T12:00:00', source: 'owner', corrected: 'set' }),
      flags: ['camera_offset'],
      camera: sonyRef,
      correction: { kind: 'set', local: '2010-07-20T12:00:00', created_at },
    })
    // Not flagged, and not among the event's photos: the shift leaves it.
    const agreeing = mediaDate('704', path(4), {
      date: dateJSON({ instant: '2010-07-18T09:00:00Z', local: '2010-07-18T09:00:00' }),
      camera: sonyRef,
    })
    const sony = camera(sonyKey, {
      photos: 4,
      state: 'offset',
      suggested_shift_s: shiftS,
      events: [{ folder: bahia, delta_s: -shiftS, photos: 3, reference: 'gps' }],
    })
    const requests = stubApi(
      routes(
        (params) =>
          params.get('camera') === sonyKey && params.get('within') === bahia.id ? [plain, shifted, set, agreeing] : [],
        () => [sony],
        {
          'POST /api/commands/set-date-correction': () =>
            jsonResponse(200, { applied: 2, skipped_count: 1, skipped: [], batch_id: 'b4' }),
        },
      ),
    )
    renderApp('/dates?source=fotos')

    const card = within(await screen.findByRole('article', { name: 'SONY DSC-W55' }))
    await userEvent.click(card.getByRole('button', { name: /^Shift \+1 year 3 hours/ }))
    const dialog = within(await screen.findByRole('alertdialog', { name: /^Shift the photos of SONY DSC-W55/ }))
    const moved = lines(await dialog.findByRole('list', { name: 'Photos to shift' }))
    expect(moved).toHaveLength(2)
    expect(moved[0]).toContain(`${path(1)}: Jul 17, 2009, 7:00:00 AM → Jul 17, 2010, 10:00:00 AM`)
    // The camera's 07:30 moved by the shift, not the owner's 08:30.
    expect(moved[1]).toContain(`${path(2)}: Jul 17, 2009, 8:30:00 AM → Jul 17, 2010, 10:30:00 AM`)
    expect(moved[1]).toContain('+1 hour')
    expect(moved[0]).not.toContain('+1 hour')
    const kept = lines(dialog.getByRole('list', { name: 'Photos that keep your correction' }))
    expect(kept).toEqual([`${path(3)}: Jul 20, 2010, 12:00:00 PM`])
    expect(dialog.queryByText(new RegExp(path(4)))).not.toBeInTheDocument()

    await userEvent.click(dialog.getByRole('button', { name: 'Shift 2 photos' }))
    expect(await screen.findByText('2 dates corrected.')).toBeInTheDocument()
    expect(await bodies(requests, 'set-date-correction')).toEqual([
      { folder_ids: [bahia.id], camera_key: sonyKey, correction: { kind: 'shift', shift_s: shiftS } },
    ])
  })

  it('sends a camera’s shift over more than 100 folders in requests of at most 100', async () => {
    const folders = Array.from({ length: 101 }, (_, i) => {
      const path = `Eventos/${i}`
      return { id: String(1000 + i), path, path_b64: btoa(path) }
    })
    const sony = camera(sonyKey, {
      photos: 101,
      state: 'offset',
      suggested_shift_s: shiftS,
      events: folders.map((folder) => ({ folder, delta_s: -shiftS, photos: 1, reference: 'gps' as const })),
    })
    const requests = stubApi(
      routes(
        (params) => {
          const folder = folders.find((f) => f.id === params.get('within'))
          return folder === undefined || params.get('camera') !== sonyKey
            ? []
            : [sonyPhoto(String(2000 + Number(folder.id)), `${folder.path}/DSC.JPG`, '2009-07-17T07:00:00', false)]
        },
        () => [sony],
        {
          'POST /api/commands/set-date-correction': async (request) => {
            const body = (await request.clone().json()) as { folder_ids: string[] }
            return jsonResponse(200, { applied: body.folder_ids.length, skipped_count: 0, skipped: [], batch_id: 'b5' })
          },
        },
      ),
    )
    renderApp('/dates?source=fotos')

    const card = within(await screen.findByRole('article', { name: 'SONY DSC-W55' }))
    await userEvent.click(card.getByRole('button', { name: /^Shift \+1 year 3 hours/ }))
    const dialog = within(await screen.findByRole('alertdialog', { name: /^Shift the photos of SONY DSC-W55/ }))
    await userEvent.click(await dialog.findByRole('button', { name: 'Shift 101 photos' }, { timeout: 5_000 }))
    expect(await screen.findByText('101 dates corrected.')).toBeInTheDocument()

    const sent = (await bodies(requests, 'set-date-correction')) as Array<{ folder_ids: string[]; camera_key: string }>
    expect(sent).toHaveLength(2)
    for (const body of sent) {
      expect(body.folder_ids.length).toBeLessThanOrEqual(100)
      expect(body.camera_key).toBe(sonyKey)
    }
    expect(sent.flatMap((body) => body.folder_ids)).toEqual(folders.map((folder) => folder.id))
  })

  it('ignores a folder in the address that belongs to another source', async () => {
    const other = fotosSource({ id: 'other', label: 'Other', root_entry_id: '2', writes: { enabled: true, unavailable: null } })
    const requests = stubApi({
      ...routes(
        () => [mediaDate('801', 'Fotos/a.jpg')],
        () => [],
        {
          // Folder 77 is one of fotos.
          'GET /api/entries/77': () => jsonResponse(200, entryDetail(folderRow('77', 'Viagens'))),
          'POST /api/commands/set-date-correction': () =>
            jsonResponse(200, { applied: 1, skipped_count: 0, skipped: [], batch_id: 'b6' }),
        },
      ),
      'GET /api/sources': () => jsonResponse(200, { sources: [fotos, other] }),
    })
    renderApp('/dates?source=other&within=77')

    // The list of the whole source is asked for once the folder is known.
    await waitFor(() =>
      expect(
        requests.some((r) => {
          const url = new URL(r.url)
          return url.pathname === '/api/dates' && url.searchParams.get('source') === 'other' && !url.searchParams.has('within')
        }),
      ).toBe(true),
    )
    // No notice limits the list to the folder.
    expect(screen.queryByRole('button', { name: 'Everywhere' })).not.toBeInTheDocument()

    await userEvent.click(await screen.findByRole('button', { name: 'Correct dates…' }))
    const dialog = within(await screen.findByRole('dialog', { name: 'Correct dates' }))
    await userEvent.click(dialog.getByRole('radio', { name: 'Take the date in the file name' }))
    await userEvent.click(dialog.getByRole('button', { name: 'Apply' }))
    const sent = (await bodies(requests, 'set-date-correction')) as Array<{ folder_ids?: string[] }>
    expect(sent.filter((body) => body.folder_ids?.includes('77'))).toEqual([])
  })

  it('offers no bulk action while the list fails', async () => {
    stubApi(
      routes(
        () => [],
        () => [],
        { 'GET /api/dates': () => errorResponse(404, 'not_found') },
      ),
    )
    renderApp('/dates?source=fotos')

    await screen.findByRole('button', { name: 'Try again' })
    expect(screen.getByRole('button', { name: 'Correct dates…' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Set file dates…' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Organize by date…' })).toBeDisabled()
  })

  it('says why a bulk shift skipped files: no date to shift, or the owner’s own correction', async () => {
    stubApi(
      routes(
        () => [mediaDate('901', 'Fotos/a.jpg'), mediaDate('902', 'Fotos/b.jpg')],
        () => [],
        {
          'POST /api/commands/set-date-correction': () =>
            jsonResponse(200, {
              applied: 0,
              skipped_count: 2,
              skipped: [
                { entry_id: '901', path: 'Fotos/a.jpg', path_b64: '', reason: 'no_date' },
                { entry_id: '902', path: 'Fotos/b.jpg', path_b64: '', reason: 'has_correction' },
              ],
              batch_id: 'b7',
            }),
        },
      ),
    )
    renderApp('/dates?source=fotos')

    await userEvent.click(await screen.findByRole('button', { name: 'Select all shown' }))
    await userEvent.click(screen.getByRole('button', { name: 'Correct dates…' }))
    const dialog = await screen.findByRole('dialog', { name: 'Correct dates' })
    await userEvent.click(within(dialog).getByRole('radio', { name: 'Shift the date' }))
    await userEvent.type(within(dialog).getByRole('spinbutton', { name: 'Hours' }), '1')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Apply' }))

    expect(await within(dialog).findByText('2 not corrected:')).toBeInTheDocument()
    expect(text(dialog)).not.toContain('dates.correct.skip.')
    expect(within(dialog).getByText(/^Fotos\/a\.jpg \(.*no date to shift.*\)$/)).toBeInTheDocument()
    expect(within(dialog).getByText(/^Fotos\/b\.jpg \(.*correction.*\)$/)).toBeInTheDocument()
  })

  it('says the server’s reason when it refuses to shift one date', async () => {
    const row = entryRow({
      id: '410',
      name: 'IMG_0410.JPG',
      name_b64: btoa('IMG_0410.JPG'),
      path: 'Fotos/IMG_0410.JPG',
      path_b64: btoa('Fotos/IMG_0410.JPG'),
      file_kind: 'image',
      category: 'personal_media',
      family: 'personal',
      triage: 'keep',
    })
    stubApi({
      'GET /api/session': () => jsonResponse(200, signedIn),
      'GET /api/sources': () => jsonResponse(200, { sources: [fotos] }),
      'GET /api/tags': () => jsonResponse(200, { tags: [] }),
      'GET /api/entries/410': () => jsonResponse(200, entryDetail(row)),
      'GET /api/entries/410/dates': () => jsonResponse(200, { dates: entryDates() }),
      'POST /api/commands/set-date-correction': () =>
        errorResponse(
          409,
          'invalid_entry_state',
          'entry 410 cannot be corrected: it has no date to shift, or the shift would move it outside the years 1700 to 2200',
        ),
    })
    renderApp(`/search?entry=${row.id}`)

    const panel = within(await screen.findByRole('complementary', { name: 'IMG_0410.JPG' }))
    const section = within(await panel.findByRole('region', { name: 'Dates' }))
    await userEvent.click(await section.findByRole('button', { name: 'Correct the date…' }))
    const dialog = within(await screen.findByRole('dialog', { name: 'Correct the date of “IMG_0410.JPG”' }))
    await userEvent.click(dialog.getByRole('radio', { name: 'Shift the date' }))
    await userEvent.type(dialog.getByRole('spinbutton', { name: 'Hours' }), '1')
    await userEvent.click(dialog.getByRole('button', { name: 'Apply' }))

    const alert = await dialog.findByRole('alert')
    // The headline, not only the technical details with the status, gives
    // the reason.
    expect(within(alert).getByText(/^(?!409).*no date to shift/)).toBeInTheDocument()
    expect(text(alert)).not.toContain('after tomorrow')
  })
})
