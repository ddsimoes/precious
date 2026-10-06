import { useQuery } from '@tanstack/react-query'
import { createContext, useContext } from 'react'

import { apiGet, apiPost } from '@/app/api'

// Session is the bootstrap state from GET /api/session. Before login it
// describes a pre-login session whose csrf_token authorizes the login request.
export interface Session {
  authenticated: boolean
  csrf_token: string
  admin_exists: boolean
}

export interface LoginResult {
  authenticated: true
  csrf_token: string
}

// SignedInSession holds the authenticated session of the signed-in screens,
// which AppLayout provides.
export const SignedInSession = createContext<Session | null>(null)

// useCsrfToken returns the CSRF token that commands send. It is available
// only inside AppLayout.
export function useCsrfToken(): string {
  const session = useContext(SignedInSession)
  if (session === null) {
    throw new Error('useCsrfToken is used outside the signed-in layout')
  }
  return session.csrf_token
}

export const sessionQueryKey = ['session'] as const

export function useSession() {
  return useQuery({
    queryKey: sessionQueryKey,
    queryFn: ({ signal }) => apiGet<Session>('/api/session', signal),
    // The session changes only through login and logout, which update or
    // refetch it explicitly.
    staleTime: Infinity,
  })
}

export function login(password: string, csrfToken: string): Promise<LoginResult> {
  return apiPost<LoginResult>('/api/session/login', { password }, csrfToken)
}

export function logout(csrfToken: string): Promise<void> {
  return apiPost<void>('/api/session/logout', undefined, csrfToken)
}
