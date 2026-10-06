import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { Navigate } from 'react-router'

import { ApiError } from '@/app/api'
import { SessionGate } from '@/app/SessionGate'
import { login, sessionQueryKey, type Session } from '@/app/session'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { FormControl, FormField, FormLabel, FormMessage } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'

export function LoginPage() {
  return (
    <main className="flex min-h-screen items-center justify-center p-4">
      <SessionGate>
        {(session) =>
          session.authenticated ? <Navigate to="/" replace /> : <LoginForm session={session} />
        }
      </SessionGate>
    </main>
  )
}

function LoginForm({ session }: { session: Session }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [password, setPassword] = useState('')

  const mutation = useMutation({
    mutationFn: () => login(password, session.csrf_token),
    // The authenticated session re-renders LoginPage, which redirects to Home.
    onSuccess: (result) => {
      queryClient.setQueryData<Session>(sessionQueryKey, {
        authenticated: true,
        csrf_token: result.csrf_token,
        admin_exists: true,
      })
    },
    onError: (error) => {
      // Anything but a wrong password may mean the pre-login session expired;
      // fetch a fresh one (and CSRF token) for the next attempt.
      if (!(error instanceof ApiError && error.code === 'login_failed')) {
        void queryClient.invalidateQueries({ queryKey: sessionQueryKey })
      }
    },
  })

  let error: string | undefined
  if (mutation.error !== null) {
    error =
      mutation.error instanceof ApiError && mutation.error.code === 'login_failed'
        ? t('login.failed')
        : t('login.error')
  }

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    mutation.mutate()
  }

  return (
    <Card className="w-full max-w-sm">
      <CardHeader>
        <CardTitle>
          <h1>{t('login.title')}</h1>
        </CardTitle>
        <CardDescription>{t('login.description')}</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4">
        {!session.admin_exists && (
          <p role="status" className="rounded-md bg-muted p-3 text-sm">
            <Trans
              i18nKey="login.noAdmin"
              components={{ command: <code className="font-mono font-semibold" /> }}
            />
          </p>
        )}
        <form className="grid gap-4" onSubmit={submit} noValidate>
          <FormField error={error}>
            <FormLabel>{t('login.password')}</FormLabel>
            <FormControl>
              <Input
                type="password"
                name="password"
                autoComplete="current-password"
                required
                value={password}
                onChange={(event) => setPassword(event.target.value)}
              />
            </FormControl>
            <FormMessage />
          </FormField>
          <Button type="submit" disabled={mutation.isPending || password === ''}>
            {mutation.isPending ? t('login.submitting') : t('login.submit')}
          </Button>
        </form>
      </CardContent>
    </Card>
  )
}
