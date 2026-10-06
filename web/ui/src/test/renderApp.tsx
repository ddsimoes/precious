import { QueryClientProvider } from '@tanstack/react-query'
import { render } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { vi } from 'vitest'

import { createQueryClient } from '@/app/queryClient'
import { routes } from '@/app/routes'

// Route answers one API request; the key of a route table is "METHOD /path".
type Route = (request: Request) => Response | Promise<Response>

export function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

// errorResponse answers with the API error envelope.
export function errorResponse(status: number, code: string, message = code): Response {
  return jsonResponse(status, { error: { code, message } })
}

// signedIn is the session of an authenticated owner.
export const signedIn = { authenticated: true, csrf_token: 'session-token', admin_exists: true }

// stubApi replaces fetch with the route table. Requests are recorded in
// order; a request without a route fails the test.
export function stubApi(table: Record<string, Route>): Request[] {
  const requests: Request[] = []
  vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = new Request(new URL(String(input), 'http://precious.test'), init)
    requests.push(request)
    const route = table[`${request.method} ${new URL(request.url).pathname}`]
    if (route === undefined) {
      throw new Error(`unexpected request ${request.method} ${request.url}`)
    }
    return route(request)
  })
  return requests
}

// renderApp renders the whole app at path with a fresh query cache, made as
// the app makes it but without retries.
export function renderApp(path: string) {
  const queryClient = createQueryClient()
  queryClient.setDefaultOptions({ queries: { retry: false } })
  const router = createMemoryRouter(routes, { initialEntries: [path] })
  render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
  return { router, queryClient }
}
