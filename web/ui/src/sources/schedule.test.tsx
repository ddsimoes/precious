import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { Schedule } from '@/api/sources'
import { fotosSource, usbSource } from '@/test/fixtures'
import { jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'

function sourceCard(name: string) {
  return within(screen.getByRole('article', { name }))
}

// row is the description of the term on the source's card.
function row(source: string, term: string) {
  return sourceCard(source).getByText(term, { selector: 'dt' }).nextElementSibling
}

function scheduleCommands(requests: Request[]) {
  return requests.filter((r) => new URL(r.url).pathname === '/api/commands/set-source-schedule')
}

// browserZone makes the browser's time zone zone; the test environment's is
// UTC.
function browserZone(zone: string) {
  const resolvedOptions = Intl.DateTimeFormat.prototype.resolvedOptions
  vi.spyOn(Intl.DateTimeFormat.prototype, 'resolvedOptions').mockImplementation(function (this: Intl.DateTimeFormat) {
    return { ...resolvedOptions.call(this), timeZone: zone }
  })
}

afterEach(() => {
  vi.restoreAllMocks()
})

const weeklySunday: Schedule = { every: 'week', at: '03:00', weekday: 0, zone: 'UTC' }

describe('Source schedule', () => {
  it('shows each schedule with its next scan and the last skipped scan, in the browser locale', async () => {
    stubApi({
      'GET /api/session': () => jsonResponse(200, signedIn),
      'GET /api/sources': () =>
        jsonResponse(200, {
          sources: [
            fotosSource({ schedule: weeklySunday, next_scan_at: '2026-10-04T03:00:00Z' }),
            usbSource({
              schedule: { every: 'day', at: '03:00', zone: 'America/Sao_Paulo' },
              next_scan_at: '2026-10-02T06:00:00Z',
              schedule_skipped: { at: '2026-10-01T06:00:00Z', reason: 'offline' },
            }),
            fotosSource({ id: 'music', label: 'Music' }),
          ],
        }),
    })
    renderApp('/sources')

    await screen.findByRole('article', { name: 'Fotos' })
    const fotos = sourceCard('Fotos')
    expect(row('Fotos', 'Rescan')).toHaveTextContent('Weekly on Sunday at 03:00')
    expect(row('Fotos', 'Next scan')).toHaveTextContent(/^Oct 4, 2026, 3:00\sAM$/u)
    expect(fotos.queryByText('Last scheduled scan')).not.toBeInTheDocument()

    // A schedule set in another zone names it.
    expect(row('Old disk', 'Rescan')).toHaveTextContent('Daily at 03:00 (America/Sao_Paulo time)')
    expect(row('Old disk', 'Next scan')).toHaveTextContent(/^Oct 2, 2026, 6:00\sAM$/u)
    expect(row('Old disk', 'Last scheduled scan')).toHaveTextContent(
      /^Skipped on Oct 1, 2026, 6:00\sAM: the disk was not connected\.$/u,
    )

    expect(row('Music', 'Rescan')).toHaveTextContent('Off')
    expect(sourceCard('Music').queryByText('Next scan')).not.toBeInTheDocument()
  })

  it("sets a weekly schedule at a time in the browser's time zone", async () => {
    browserZone('America/Sao_Paulo')
    const requests = stubApi({
      'GET /api/session': () => jsonResponse(200, signedIn),
      'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource()] }),
      'POST /api/commands/set-source-schedule': async (request) => {
        const { schedule } = (await request.clone().json()) as { schedule: Schedule }
        return jsonResponse(200, { source: fotosSource({ schedule, next_scan_at: '2026-10-03T07:30:00Z' }) })
      },
    })
    renderApp('/sources')
    const user = userEvent.setup()

    await screen.findByRole('article', { name: 'Fotos' })
    await user.click(sourceCard('Fotos').getByRole('button', { name: 'Change schedule' }))
    const form = within(sourceCard('Fotos').getByRole('form', { name: 'Rescan schedule' }))
    expect(form.queryByLabelText('Time')).not.toBeInTheDocument()
    await user.selectOptions(form.getByLabelText('Rescan'), 'Weekly')
    await user.selectOptions(form.getByLabelText('Day'), 'Saturday')
    const time = form.getByLabelText('Time')
    expect(time).toHaveValue('03:00')
    await user.clear(time)
    expect(form.getByRole('button', { name: 'Save' })).toBeDisabled()
    await user.type(time, '04:30')
    expect(form.getByText(/in your time zone, America\/Sao_Paulo\./u)).toBeInTheDocument()
    await user.click(form.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(screen.queryByRole('form', { name: 'Rescan schedule' })).not.toBeInTheDocument())
    expect(await scheduleCommands(requests)[0]?.json()).toEqual({
      source_id: 'fotos',
      schedule: { every: 'week', at: '04:30', weekday: 6, zone: 'America/Sao_Paulo' },
    })
    expect(row('Fotos', 'Rescan')).toHaveTextContent('Weekly on Saturday at 04:30')
    expect(row('Fotos', 'Next scan')).toHaveTextContent(/^Oct 3, 2026, 7:30\sAM$/u)
  })

  it('sets a daily schedule without a weekday, and turns a schedule off', async () => {
    const requests = stubApi({
      'GET /api/session': () => jsonResponse(200, signedIn),
      'GET /api/sources': () =>
        jsonResponse(200, { sources: [fotosSource({ schedule: weeklySunday, next_scan_at: '2026-10-04T03:00:00Z' })] }),
      'POST /api/commands/set-source-schedule': async (request) => {
        const { schedule } = (await request.clone().json()) as { schedule: Schedule | null }
        return jsonResponse(200, {
          source: fotosSource({ schedule, next_scan_at: schedule === null ? null : '2026-10-02T03:00:00Z' }),
        })
      },
    })
    renderApp('/sources')
    const user = userEvent.setup()

    await screen.findByRole('article', { name: 'Fotos' })
    await user.click(sourceCard('Fotos').getByRole('button', { name: 'Change schedule' }))
    let form = within(sourceCard('Fotos').getByRole('form', { name: 'Rescan schedule' }))
    // The form starts at the current schedule.
    expect(form.getByLabelText('Rescan')).toHaveValue('week')
    expect(form.getByLabelText('Day')).toHaveValue('0')
    await user.selectOptions(form.getByLabelText('Rescan'), 'Daily')
    expect(form.queryByLabelText('Day')).not.toBeInTheDocument()
    await user.click(form.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(row('Fotos', 'Rescan')).toHaveTextContent('Daily at 03:00'))

    await user.click(sourceCard('Fotos').getByRole('button', { name: 'Change schedule' }))
    form = within(sourceCard('Fotos').getByRole('form', { name: 'Rescan schedule' }))
    await user.selectOptions(form.getByLabelText('Rescan'), 'Off')
    expect(form.queryByLabelText('Time')).not.toBeInTheDocument()
    await user.click(form.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(row('Fotos', 'Rescan')).toHaveTextContent('Off'))
    expect(sourceCard('Fotos').queryByText('Next scan')).not.toBeInTheDocument()

    const [daily, off] = scheduleCommands(requests)
    expect(await daily?.json()).toEqual({ source_id: 'fotos', schedule: { every: 'day', at: '03:00', zone: 'UTC' } })
    expect(await off?.json()).toEqual({ source_id: 'fotos', schedule: null })
  })
})
