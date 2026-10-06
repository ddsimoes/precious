// A small typed client for the Precious JSON API. Requests stay on the app's
// origin with its cookies; state-changing requests carry the session's CSRF
// token, and commands carry an Idempotency-Key.

// ErrorEnvelope is the body of every API error response.
export interface ErrorEnvelope {
  error: { code: string; message: string }
}

// ApiError is a failed API response. code is the envelope's code, or
// `http_<status>` when the body is not an error envelope.
export class ApiError extends Error {
  readonly status: number
  readonly code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}

const commandPrefix = '/api/commands/'

export interface PostOptions {
  // idempotencyKey replaces the generated key, so a retried command keeps its key.
  idempotencyKey?: string
  signal?: AbortSignal
}

export function apiGet<T>(path: string, signal?: AbortSignal): Promise<T> {
  return send<T>(path, { method: 'GET', headers: { Accept: 'application/json' }, signal })
}

// apiProbe asks path for its first byte only, so a refusal is known by its
// error envelope without transferring the whole body, and returns the
// answer's status. A range past the end of an empty body (416) is not a
// refusal.
export async function apiProbe(path: string, signal?: AbortSignal): Promise<number> {
  const response = await fetch(path, {
    method: 'GET',
    headers: { Range: 'bytes=0-0' },
    credentials: 'same-origin',
    signal,
  })
  if (!response.ok && response.status !== 416) {
    throw await toApiError(response)
  }
  await response.body?.cancel()
  return response.status
}

// apiPost sends body as JSON with the X-CSRF-Token header; paths under
// /api/commands/ also get an Idempotency-Key.
export function apiPost<T>(
  path: string,
  body: unknown,
  csrfToken: string,
  { idempotencyKey, signal }: PostOptions = {},
): Promise<T> {
  const headers: Record<string, string> = {
    Accept: 'application/json',
    'X-CSRF-Token': csrfToken,
  }
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
  }
  if (path.startsWith(commandPrefix)) {
    headers['Idempotency-Key'] = idempotencyKey ?? newIdempotencyKey()
  }
  return send<T>(path, {
    method: 'POST',
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    signal,
  })
}

// postCommand runs the command name (POST /api/commands/<name>).
export function postCommand<T>(
  name: string,
  body: unknown,
  csrfToken: string,
  options?: PostOptions,
): Promise<T> {
  return apiPost<T>(`${commandPrefix}${name}`, body, csrfToken, options)
}

async function send<T>(path: string, init: RequestInit): Promise<T> {
  const response = await fetch(path, { ...init, credentials: 'same-origin' })
  if (!response.ok) {
    throw await toApiError(response)
  }
  if (response.status === 204) {
    return undefined as T
  }
  return (await response.json()) as T
}

async function toApiError(response: Response): Promise<ApiError> {
  const fallback = new ApiError(response.status, `http_${response.status}`, response.statusText)
  let body: unknown
  try {
    body = await response.json()
  } catch {
    return fallback
  }
  if (!isErrorEnvelope(body)) {
    return fallback
  }
  return new ApiError(response.status, body.error.code, body.error.message)
}

function isErrorEnvelope(body: unknown): body is ErrorEnvelope {
  if (typeof body !== 'object' || body === null || !('error' in body)) {
    return false
  }
  const { error } = body
  return (
    typeof error === 'object' &&
    error !== null &&
    'code' in error &&
    typeof error.code === 'string' &&
    'message' in error &&
    typeof error.message === 'string'
  )
}

// newIdempotencyKey returns a random UUID (version 4). crypto.randomUUID needs
// a secure context, and Precious may be served over plain HTTP on a LAN;
// crypto.getRandomValues works in both.
export function newIdempotencyKey(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(16))
  bytes[6] = (bytes[6]! & 0x0f) | 0x40
  bytes[8] = (bytes[8]! & 0x3f) | 0x80
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}
