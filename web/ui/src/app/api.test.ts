import { describe, expect, it, vi } from 'vitest'

import { ApiError, apiGet, apiPost, newIdempotencyKey } from '@/app/api'

function stubFetch(response: () => Response) {
  const fetch = vi.fn<typeof globalThis.fetch>(async () => response())
  vi.stubGlobal('fetch', fetch)
  return fetch
}

function headersOf(fetch: ReturnType<typeof stubFetch>): Headers {
  return new Headers(fetch.mock.calls[0]?.[1]?.headers)
}

describe('api client', () => {
  it('sends same-origin GETs and decodes JSON', async () => {
    const fetch = stubFetch(() => Response.json({ ok: true }))

    await expect(apiGet('/api/home')).resolves.toEqual({ ok: true })
    expect(fetch.mock.calls[0]?.[0]).toBe('/api/home')
    expect(fetch.mock.calls[0]?.[1]).toMatchObject({ method: 'GET', credentials: 'same-origin' })
  })

  it('adds the CSRF token to POSTs but no Idempotency-Key outside commands', async () => {
    const fetch = stubFetch(() => new Response(null, { status: 204 }))

    await expect(apiPost('/api/session/logout', undefined, 'tok')).resolves.toBeUndefined()
    expect(fetch.mock.calls[0]?.[1]).toMatchObject({ method: 'POST', credentials: 'same-origin' })
    const headers = headersOf(fetch)
    expect(headers.get('X-CSRF-Token')).toBe('tok')
    expect(headers.has('Content-Type')).toBe(false)
    expect(headers.has('Idempotency-Key')).toBe(false)
  })

  it('adds an Idempotency-Key to commands, keeping a supplied one', async () => {
    const fetch = stubFetch(() => Response.json({ job_id: 1 }))

    await apiPost('/api/commands/start-scan', { source_id: 3 }, 'tok')
    await apiPost('/api/commands/start-scan', { source_id: 3 }, 'tok', { idempotencyKey: 'again' })

    const generated = headersOf(fetch).get('Idempotency-Key')
    expect(generated).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
    expect(new Headers(fetch.mock.calls[1]?.[1]?.headers).get('Idempotency-Key')).toBe('again')
    expect(headersOf(fetch).get('Content-Type')).toBe('application/json')
    expect(fetch.mock.calls[0]?.[1]?.body).toBe('{"source_id":3}')
  })

  it('turns the error envelope into an ApiError', async () => {
    stubFetch(() => Response.json({ error: { code: 'job_active', message: 'a scan is running' } }, { status: 409 }))

    const error = await apiPost('/api/commands/start-scan', {}, 'tok').catch((e: unknown) => e)
    expect(error).toBeInstanceOf(ApiError)
    expect(error).toMatchObject({ status: 409, code: 'job_active', message: 'a scan is running' })
  })

  it('reports a non-envelope error by its HTTP status', async () => {
    stubFetch(() => new Response('<html>bad gateway</html>', { status: 502, statusText: 'Bad Gateway' }))

    await expect(apiGet('/api/home')).rejects.toMatchObject({
      status: 502,
      code: 'http_502',
      message: 'Bad Gateway',
    })
  })

  it('generates distinct version 4 UUIDs', () => {
    const keys = new Set(Array.from({ length: 100 }, () => newIdempotencyKey()))
    expect(keys.size).toBe(100)
  })
})
