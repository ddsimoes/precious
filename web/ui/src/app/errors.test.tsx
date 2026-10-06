import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { describe, expect, it, vi } from 'vitest'

import { ApiError } from '@/app/api'
import { ErrorBanner } from '@/app/ErrorBanner'
import { errorMessage } from '@/app/errors'

describe('error messages', () => {
  const t = i18n.t

  it('maps known codes to plain sentences', () => {
    expect(errorMessage(t, new ApiError(409, 'job_active', 'job 12 is running'))).toBe(
      'A scan of this source is still running. Wait for it to stop, then try again.',
    )
    expect(errorMessage(t, new ApiError(403, 'outside_allowed_roots', 'outside'))).toBe(
      'This folder is outside the locations Precious may use. Choose a folder inside one of the listed locations.',
    )
  })

  it('lets a screen override a code, and falls back for unknown ones', () => {
    const error = new ApiError(400, 'invalid_request', 'bad handle')
    expect(errorMessage(t, error, { invalid_request: 'Start again.' })).toBe('Start again.')
    expect(errorMessage(t, new ApiError(502, 'http_502', 'Bad Gateway'))).toBe('Something went wrong. Try again.')
    expect(errorMessage(t, new TypeError('Failed to fetch'))).toBe(
      'Precious could not reach the server. Check the connection, then try again.',
    )
  })

  it('shows the message as an alert with the details collapsed', async () => {
    const onRetry = vi.fn()
    render(<ErrorBanner error={new ApiError(409, 'source_offline', 'volume gone')} onRetry={onRetry} />)

    const alert = screen.getByRole('alert')
    expect(alert).toHaveTextContent('The disk of this source is not connected.')
    expect(screen.getByText('409 source_offline: volume gone')).not.toBeVisible()
    await userEvent.setup().click(screen.getByRole('button', { name: 'Try again' }))
    expect(onRetry).toHaveBeenCalledOnce()
  })
})
