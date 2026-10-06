import { act, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import { MockEventSource } from '@/test/eventSource'
import { fotosSource } from '@/test/fixtures'
import { errorResponse, jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'

const preLogin = { authenticated: false, csrf_token: 'pre-token', admin_exists: true }

// sessionThatEnds answers GET /api/session as signed in until ended() is
// called, and as a fresh pre-login session afterwards.
function sessionThatEnds() {
  let ended = false
  return {
    route: () => jsonResponse(200, ended ? preLogin : signedIn),
    end: () => {
      ended = true
    },
  }
}

function sessionRequests(requests: Request[]) {
  return requests.filter((r) => new URL(r.url).pathname === '/api/session').length
}

describe('an ended session', () => {
  it('sends a 401 from a query to the login screen', async () => {
    const session = sessionThatEnds()
    const requests = stubApi({
      'GET /api/session': session.route,
      'GET /api/sources': () => {
        session.end()
        return errorResponse(401, 'unauthenticated')
      },
    })
    const { router, queryClient } = renderApp('/sources')

    expect(await screen.findByRole('heading', { name: 'Sign in to Precious' })).toBeInTheDocument()
    expect(router.state.location.pathname).toBe('/login')
    // The login form gets the fresh pre-login session's token.
    await waitFor(() => expect(sessionRequests(requests)).toBe(2))
    expect(queryClient.getQueryData(['sources'])).toBeUndefined()
    // Leaving the signed-in screens closes the event stream.
    expect(MockEventSource.instances.every((s) => s.readyState === MockEventSource.CLOSED)).toBe(true)
  })

  it('sends a 401 from a command to the login screen', async () => {
    const session = sessionThatEnds()
    stubApi({
      'GET /api/session': session.route,
      'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource()] }),
      'POST /api/commands/start-scan': () => {
        session.end()
        return errorResponse(401, 'unauthenticated')
      },
    })
    const { router } = renderApp('/sources')
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: 'Scan now' }))

    expect(await screen.findByRole('heading', { name: 'Sign in to Precious' })).toBeInTheDocument()
    expect(router.state.location.pathname).toBe('/login')
  })

  it('sends a refused event stream to the login screen', async () => {
    const session = sessionThatEnds()
    stubApi({
      'GET /api/session': session.route,
      'GET /api/sources': () => jsonResponse(200, { sources: [] }),
    })
    const { router } = renderApp('/sources')
    expect(await screen.findByRole('heading', { name: 'Sources' })).toBeInTheDocument()

    // The browser cannot show the stream's 401; it closes the stream, and the
    // session check finds the session gone.
    session.end()
    act(() => MockEventSource.latest().fail(MockEventSource.CLOSED))

    expect(await screen.findByRole('heading', { name: 'Sign in to Precious' })).toBeInTheDocument()
    expect(router.state.location.pathname).toBe('/login')
  })

  it('keeps the screen when the session cannot be checked', async () => {
    let reachable = true
    const requests = stubApi({
      'GET /api/session': () => (reachable ? jsonResponse(200, signedIn) : errorResponse(502, 'http_502')),
      'GET /api/sources': () => jsonResponse(200, { sources: [] }),
    })
    const { router, queryClient } = renderApp('/sources')
    expect(await screen.findByRole('heading', { name: 'Sources' })).toBeInTheDocument()

    reachable = false
    act(() => MockEventSource.latest().fail(MockEventSource.CLOSED))

    await waitFor(() => expect(queryClient.getQueryState(['session'])?.status).toBe('error'))
    expect(sessionRequests(requests)).toBe(2)
    expect(screen.getByRole('heading', { name: 'Sources' })).toBeInTheDocument()
    expect(router.state.location.pathname).toBe('/sources')
  })

  it('leaves a wrong password on the login screen without a new session', async () => {
    const requests = stubApi({
      'GET /api/session': () => jsonResponse(200, preLogin),
      'POST /api/session/login': () => errorResponse(401, 'login_failed'),
    })
    renderApp('/login')
    const user = userEvent.setup()

    await user.type(await screen.findByLabelText('Password'), 'wrong password!')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('Sign-in failed.')
    expect(sessionRequests(requests)).toBe(1)
  })
})
