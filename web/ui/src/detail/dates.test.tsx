import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import type { EntryDates } from '@/api/dates'
import type { EntryRow } from '@/api/entries'
import { dateJSON, entryDates, entryDetail, entryRow, fotosSource } from '@/test/fixtures'
import { errorResponse, jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'

type Route = (request: Request) => Response | Promise<Response>

// photo is an image file of fotos in Viagens/2008-03 Ouro Preto.
function photo(name: string, overrides: Partial<EntryRow> = {}): EntryRow {
  const path = `Viagens/2008-03 Ouro Preto/${name}`
  return entryRow({
    id: '301',
    name,
    name_b64: btoa(name),
    path,
    path_b64: btoa(path),
    file_kind: 'image',
    category: 'personal_media',
    family: 'personal',
    triage: 'keep',
    mtime: '2011-01-15T10:00:00Z',
    ...overrides,
  })
}

function routes(row: EntryRow, dates: () => EntryDates | null, extra: Record<string, Route> = {}) {
  return {
    'GET /api/session': () => jsonResponse(200, signedIn),
    'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource()] }),
    'GET /api/tags': () => jsonResponse(200, { tags: [] }),
    [`GET /api/entries/${row.id}`]: () => jsonResponse(200, entryDetail(row)),
    [`GET /api/entries/${row.id}/dates`]: () => jsonResponse(200, { dates: dates() }),
    ...extra,
  }
}

function bodies(requests: Request[], name: string) {
  return Promise.all(
    requests
      .filter((r) => r.method === 'POST' && new URL(r.url).pathname === `/api/commands/${name}`)
      .map((r) => r.clone().json() as Promise<unknown>),
  )
}

function text(element: Element) {
  return (element.textContent ?? '').replace(/\s/g, ' ')
}

// facts reads the terms of a description list as "term: value" lines.
function facts(terms: HTMLElement[]) {
  return terms.map((term) => `${text(term)}: ${text(term.nextElementSibling!)}`)
}

async function datesSection(name: string) {
  const panel = within(await screen.findByRole('complementary', { name }))
  return within(await panel.findByRole('region', { name: 'Dates' }))
}

// The Nikon's first photo of Ouro Preto (design D19): its capture with
// the offset -03:00, copied in 2011.
const disagreeing = entryDates({
  date: dateJSON({
    instant: '2008-03-22T17:00:00.12Z',
    local: '2008-03-22T14:00:00',
    offset_min: -180,
    confidence: 'high',
  }),
  flags: ['mtime_disagrees'],
  camera: { key: 'NIKON|COOLPIX P5000|3012345', make: 'NIKON', model: 'COOLPIX P5000', serial: '3012345' },
  candidates: [
    {
      source: 'exif',
      local: '2008-03-22T14:00:00',
      offset_min: -180,
      instant: '2008-03-22T17:00:00.12Z',
      precision: 'second',
      plausible: true,
    },
    {
      source: 'folder_name',
      local: '2008-03',
      offset_min: null,
      instant: '2008-03-01T00:00:00Z',
      precision: 'month',
      plausible: true,
    },
    {
      source: 'mtime',
      local: '2011-01-15T10:00:00',
      offset_min: null,
      instant: '2011-01-15T10:00:00Z',
      precision: 'second',
      plausible: true,
    },
  ],
})

describe('Dates in the detail panel', () => {
  it('A photo with a disagreeing modification time', async () => {
    const row = photo('DSCN0001.JPG')
    stubApi(routes(row, () => disagreeing))
    renderApp(`/search?entry=${row.id}`)

    const section = await datesSection('DSCN0001.JPG')
    expect(await section.findByRole('list', { name: 'Dates found' })).toBeInTheDocument()
    expect(facts(section.getAllByRole('term'))).toEqual([
      'Date: Mar 22, 2008, 2:00:00 PM UTC−03:00',
      'From: Camera',
      'Known: To the second',
      'How sure: Sure',
      'Camera: NIKON COOLPIX P5000 (serial 3012345)',
    ])
    const flags = section.getByRole('list', { name: 'To check' })
    expect(text(flags)).toContain('Modification time disagrees')
    expect(text(flags)).toContain('more than a day away from this date')
    expect(
      within(section.getByRole('list', { name: 'Dates found' }))
        .getAllByRole('listitem')
        .map(text),
    ).toEqual([
      'Camera: Mar 22, 2008, 2:00:00 PM UTC−03:00',
      'Folder name: March 2008',
      'Modification time: Jan 15, 2011, 10:00:00 AM',
    ])
  })

  it('An unreadable photo shows its state and no "No date in the file"', async () => {
    const row = photo('DSCN0009.JPG', { id: '309' })
    stubApi(
      routes(row, () =>
        entryDates({
          date: dateJSON({
            instant: '2011-01-15T10:00:00Z',
            local: '2011-01-15T10:00:00',
            source: 'mtime',
            confidence: 'lowest',
          }),
          metadata: 'unreadable',
          candidates: [
            {
              source: 'mtime',
              local: '2011-01-15T10:00:00',
              offset_min: null,
              instant: '2011-01-15T10:00:00Z',
              precision: 'second',
              plausible: true,
            },
          ],
        }),
      ),
    )
    renderApp(`/search?entry=${row.id}`)

    const section = await datesSection('DSCN0009.JPG')
    expect(await section.findByText('Could not be read')).toBeInTheDocument()
    expect(facts(section.getAllByRole('term'))).toEqual([
      'Date: Jan 15, 2011, 10:00:00 AM',
      'From: Modification time',
      'Known: To the second',
      'How sure: Least sure',
      'Camera information: Could not be read',
    ])
    expect(section.queryByRole('list', { name: 'To check' })).not.toBeInTheDocument()
    expect(section.queryByText('No date in the file')).not.toBeInTheDocument()
  })

  it('corrects one photo, and says why a single correction is refused', async () => {
    const row = photo('IMG-20110416-WA0003.jpg', { id: '410' })
    let corrected = false
    const requests = stubApi(
      routes(
        row,
        () =>
          corrected
            ? entryDates({
                date: dateJSON({ local: '1978', precision: 'year', instant: '1978-01-01T00:00:00Z', source: 'owner', confidence: 'high', corrected: 'set' }),
                correction: { kind: 'set', local: '1978', created_at: '2026-10-08T10:00:00Z' },
              })
            : entryDates(),
        {
          'POST /api/commands/set-date-correction': async (request) => {
            const body = (await request.clone().json()) as { correction: { kind: string } }
            if (body.correction.kind === 'use_folder') {
              return errorResponse(409, 'invalid_entry_state')
            }
            corrected = true
            return jsonResponse(200, { applied: 1, skipped_count: 0, skipped: [], batch_id: 'b1' })
          },
          'POST /api/commands/clear-date-correction': () => {
            corrected = false
            return jsonResponse(200, { cleared: 1, batch_id: 'b2' })
          },
        },
      ),
    )
    renderApp(`/search?entry=${row.id}`)
    const section = await datesSection('IMG-20110416-WA0003.jpg')

    await userEvent.click(await section.findByRole('button', { name: 'Correct the date…' }))
    const dialog = within(await screen.findByRole('dialog', { name: 'Correct the date of “IMG-20110416-WA0003.jpg”' }))
    expect(dialog.queryByRole('radio', { name: 'Remove the correction' })).not.toBeInTheDocument()
    await userEvent.click(dialog.getByRole('radio', { name: 'Take the date of the folder' }))
    await userEvent.click(dialog.getByRole('button', { name: 'Apply' }))
    expect(await dialog.findByText('No folder above this file has a date in its name.')).toBeInTheDocument()

    await userEvent.click(dialog.getByRole('radio', { name: 'Set the date' }))
    await userEvent.selectOptions(dialog.getByRole('combobox', { name: 'Known to' }), 'The year')
    await userEvent.type(dialog.getByRole('spinbutton', { name: 'Year' }), '1978')
    await userEvent.click(dialog.getByRole('button', { name: 'Apply' }))
    expect(await section.findByText('1978')).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(facts(section.getAllByRole('term'))).toContain('From: Your correction, set by you')
    expect(facts(section.getAllByRole('term'))).toContain('Your correction: Set to 1978, on Oct 8, 2026, 10:00 AM')

    await userEvent.click(section.getByRole('button', { name: 'Remove the correction' }))
    expect(await section.findByText('Jul 17, 2010, 10:00:00 AM')).toBeInTheDocument()
    expect(await bodies(requests, 'set-date-correction')).toEqual([
      { entry_id: '410', correction: { kind: 'use_folder' } },
      { entry_id: '410', correction: { kind: 'set', local: '1978' } },
    ])
    expect(await bodies(requests, 'clear-date-correction')).toEqual([{ entry_id: '410' }])
  })

  it('has no Dates section, and asks for no dates, for a photo inside an archive', async () => {
    const member = photo('inside.jpg', { id: 'm55', archive_id: '54' })
    const requests = stubApi(routes(member, () => null))
    renderApp(`/search?entry=${member.id}`)
    const panel = within(await screen.findByRole('complementary', { name: 'inside.jpg' }))
    await panel.findByRole('region', { name: 'Classification' })
    expect(panel.queryByRole('region', { name: 'Dates' })).not.toBeInTheDocument()
    expect(requests.some((r) => new URL(r.url).pathname.endsWith('/dates'))).toBe(false)
  })
})
