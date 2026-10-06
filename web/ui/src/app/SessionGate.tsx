import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { useSession, type Session } from '@/app/session'
import { Button } from '@/components/ui/button'

// SessionGate renders children once GET /api/session has answered, and a
// loading or retry message until then. A later check of the session that
// fails (the server briefly unreachable) keeps the last answer.
export function SessionGate({ children }: { children: (session: Session) => ReactNode }) {
  const { t } = useTranslation()
  const query = useSession()

  if (query.data !== undefined) {
    return children(query.data)
  }
  if (query.isPending) {
    return (
      <p role="status" className="p-6 text-sm text-muted-foreground">
        {t('app.loading')}
      </p>
    )
  }
  return (
    <div role="alert" className="grid justify-items-start gap-3 p-6 text-sm">
      <p>{t('app.sessionUnavailable')}</p>
      <Button variant="outline" onClick={() => void query.refetch()}>
        {t('app.retry')}
      </Button>
    </div>
  )
}
