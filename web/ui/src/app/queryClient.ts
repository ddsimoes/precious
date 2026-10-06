import { MutationCache, QueryCache, QueryClient } from '@tanstack/react-query'

import { ApiError } from '@/app/api'
import { sessionQueryKey, type Session } from '@/app/session'

// createQueryClient makes the app's query cache. A 401 from any query or
// command while signed in means the session ended on the server: the cached
// session is marked signed out, which sends AppLayout to /login, every other
// cached response is dropped, and a fresh pre-login session is fetched.
export function createQueryClient(): QueryClient {
  const onError = (error: Error) => {
    if (error instanceof ApiError && error.status === 401) {
      signedOut(queryClient)
    }
  }
  const queryClient: QueryClient = new QueryClient({
    queryCache: new QueryCache({ onError }),
    mutationCache: new MutationCache({ onError }),
    defaultOptions: {
      queries: {
        // A refused request (4xx) fails the same way when repeated.
        retry: (failures, error) =>
          !(error instanceof ApiError && error.status >= 400 && error.status < 500) && failures < 3,
      },
    },
  })
  return queryClient
}

// signedOut handles a session the server no longer accepts. Before sign-in
// (a wrong password is a 401 too) there is nothing to do.
function signedOut(queryClient: QueryClient) {
  const session = queryClient.getQueryData<Session>(sessionQueryKey)
  if (session === undefined || !session.authenticated) {
    return
  }
  queryClient.setQueryData<Session>(sessionQueryKey, { ...session, authenticated: false })
  queryClient.removeQueries({ predicate: (query) => query.queryKey[0] !== sessionQueryKey[0] })
  void queryClient.invalidateQueries({ queryKey: sessionQueryKey })
}
