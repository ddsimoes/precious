import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import { homeResponse } from '@/test/fixtures'
import { jsonResponse, renderApp, stubApi } from '@/test/renderApp'

const preLogin = { authenticated: false, csrf_token: 'pre-token', admin_exists: true }

describe('session and login', () => {
  it('redirects an unauthenticated deep link to the login screen', async () => {
    const requests = stubApi({ 'GET /api/session': () => jsonResponse(200, preLogin) })
    const { router } = renderApp('/map/12')

    expect(await screen.findByRole('heading', { name: 'Sign in to Precious' })).toBeInTheDocument()
    expect(router.state.location.pathname).toBe('/login')
    expect(screen.queryByRole('heading', { name: 'Map' })).not.toBeInTheDocument()
    expect(requests.map((r) => `${r.method} ${new URL(r.url).pathname}`)).toEqual([
      'GET /api/session',
    ])
  })

  it('signs in with the CSRF token and opens Home', async () => {
    const requests = stubApi({
      'GET /api/session': () => jsonResponse(200, preLogin),
      'POST /api/session/login': () =>
        jsonResponse(200, { authenticated: true, csrf_token: 'session-token' }),
      'GET /api/sources': () => jsonResponse(200, { sources: [] }),
      'GET /api/home': () => jsonResponse(200, homeResponse()),
    })
    const { router } = renderApp('/login')
    const user = userEvent.setup()

    await user.type(await screen.findByLabelText('Password'), 'correct horse battery')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))

    expect(await screen.findByRole('heading', { name: 'Home' })).toBeInTheDocument()
    expect(router.state.location.pathname).toBe('/')
    const login = requests.find((r) => r.method === 'POST')
    expect(login?.headers.get('X-CSRF-Token')).toBe('pre-token')
    expect(login?.headers.get('Content-Type')).toBe('application/json')
    expect(await login?.json()).toEqual({ password: 'correct horse battery' })
  })

  it('shows a generic message when the password is rejected', async () => {
    stubApi({
      'GET /api/session': () => jsonResponse(200, preLogin),
      'POST /api/session/login': () =>
        jsonResponse(401, { error: { code: 'login_failed', message: 'login failed' } }),
    })
    const { router } = renderApp('/login')
    const user = userEvent.setup()

    const password = await screen.findByLabelText('Password')
    await user.type(password, 'wrong password!')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Sign-in failed. Check the password and try again.',
    )
    expect(password).toHaveAttribute('aria-invalid', 'true')
    expect(router.state.location.pathname).toBe('/login')
  })

  it('tells the operator to set the administrator password when none exists', async () => {
    stubApi({
      'GET /api/session': () => jsonResponse(200, { ...preLogin, admin_exists: false }),
    })
    renderApp('/login')

    const message = await screen.findByText(/No administrator password is set yet\./)
    expect(message).toHaveAttribute('role', 'status')
    expect(message).toHaveTextContent('precious admin set-password')
  })

  it('signs out with the CSRF token and returns to the login screen', async () => {
    let authenticated = true
    const requests = stubApi({
      'GET /api/session': () =>
        jsonResponse(
          200,
          authenticated
            ? { authenticated: true, csrf_token: 'session-token', admin_exists: true }
            : preLogin,
        ),
      'GET /api/sources': () => jsonResponse(200, { sources: [] }),
      'POST /api/session/logout': () => {
        authenticated = false
        return new Response(null, { status: 204 })
      },
    })
    const { router } = renderApp('/sources')
    const user = userEvent.setup()

    expect(await screen.findByRole('heading', { name: 'Sources' })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Sign out' }))

    expect(await screen.findByRole('heading', { name: 'Sign in to Precious' })).toBeInTheDocument()
    await waitFor(() => expect(router.state.location.pathname).toBe('/login'))
    const logout = requests.find((r) => r.method === 'POST')
    expect(logout?.headers.get('X-CSRF-Token')).toBe('session-token')
  })
})
