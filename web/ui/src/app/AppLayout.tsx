import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Navigate, NavLink, Outlet } from 'react-router'

import { ApiError } from '@/app/api'
import { useJobEvents } from '@/app/events'
import { SessionGate } from '@/app/SessionGate'
import { logout, sessionQueryKey, SignedInSession, type Session } from '@/app/session'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

// AppLayout is the frame of every signed-in screen. Without a session it
// redirects to /login before any screen can request inventory data.
export function AppLayout() {
  return (
    <SessionGate>
      {(session) =>
        session.authenticated ? <SignedInLayout session={session} /> : <Navigate to="/login" replace />
      }
    </SessionGate>
  )
}

function SignedInLayout({ session }: { session: Session }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  useJobEvents()

  const signOut = useMutation({
    mutationFn: async () => {
      try {
        await logout(session.csrf_token)
      } catch (error) {
        // A 401 means the session is already gone, which is what signing out wants.
        if (!(error instanceof ApiError && error.status === 401)) {
          throw error
        }
      }
    },
    onSuccess: async () => {
      // Drop every cached response, then fetch the new pre-login session; its
      // authenticated=false redirects to /login.
      queryClient.removeQueries({ predicate: (query) => query.queryKey[0] !== sessionQueryKey[0] })
      await queryClient.invalidateQueries({ queryKey: sessionQueryKey })
    },
  })

  const navItems = [
    { to: '/', label: t('nav.home'), end: true },
    { to: '/map', label: t('nav.map'), end: false },
    { to: '/search', label: t('nav.search'), end: false },
    { to: '/opportunities', label: t('nav.opportunities'), end: false },
    { to: '/history', label: t('nav.history'), end: false },
    { to: '/sources', label: t('nav.sources'), end: false },
  ]

  return (
    <SignedInSession value={session}>
      <div className="flex min-h-screen flex-col">
        <header className="flex flex-wrap items-center gap-x-6 gap-y-2 border-b bg-card px-4 py-2">
          <span className="text-lg font-semibold">{t('app.name')}</span>
          <nav aria-label={t('nav.label')} className="flex flex-wrap gap-1">
            {navItems.map((item) => (
              <NavLink
                key={item.to}
                to={item.to}
                end={item.end}
                className={({ isActive }) =>
                  cn(
                    'rounded-md px-3 py-1.5 text-sm font-medium hover:bg-accent',
                    isActive ? 'bg-accent text-accent-foreground' : 'text-muted-foreground',
                  )
                }
              >
                {item.label}
              </NavLink>
            ))}
          </nav>
          <div className="ml-auto flex items-center gap-3">
            {signOut.isError && (
              <p role="alert" className="text-sm text-destructive">
                {t('nav.signOutFailed')}
              </p>
            )}
            <Button
              variant="outline"
              size="sm"
              disabled={signOut.isPending}
              onClick={() => signOut.mutate()}
            >
              {t('nav.signOut')}
            </Button>
          </div>
        </header>
        <main className="flex-1 p-4">
          <Outlet />
        </main>
      </div>
    </SignedInSession>
  )
}
