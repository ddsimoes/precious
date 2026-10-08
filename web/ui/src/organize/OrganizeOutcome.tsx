import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'

import { actionQueryKey, fetchAction, isActive, planUndo, type Action } from '@/api/organize'
import { ErrorBanner } from '@/app/ErrorBanner'
import { Button } from '@/components/ui/button'
import { PreviewDialog } from '@/organize/PreviewDialog'
import type { Organize } from '@/organize/useOrganize'

// OrganizeOutcome shows what useOrganize is doing: a plan's error, the
// preview of a plan that needs one, and the result of the action last run,
// live, with a link to History and Undo.
export function OrganizeOutcome({ organize }: { organize: Organize }) {
  return (
    <>
      {organize.error !== null && (
        <ErrorBanner error={organize.error} overrides={organize.overrides} onDismiss={organize.dismissError} />
      )}
      {organize.ran !== null && <ActionStatus organize={organize} ran={organize.ran} />}
      {organize.preview !== null && (
        <PreviewDialog
          key={organize.preview.action.id}
          plan={organize.preview}
          onRan={organize.onRan}
          onClose={organize.closePreview}
        />
      )}
    </>
  )
}

// ActionStatus follows an action that was sent to run until it ends. A
// done action that can be undone offers Undo, except an undo itself, which
// History can undo again.
function ActionStatus({ organize, ran }: { organize: Organize; ran: Action }) {
  const { t } = useTranslation()
  const status = useQuery({
    queryKey: actionQueryKey(ran.id),
    queryFn: ({ signal }) => fetchAction(ran.id, signal),
    initialData: ran,
  })
  const action = status.data
  const c = action.counts
  const notAll =
    c.conflict + c.refused + c.failed + c.changed + c.not_permitted + c.offline + c.no_safe_rename +
      c.not_empty + c.not_attempted + c.manual_recovery > 0

  let text: string = t(`organize.status.pending.${action.kind}`)
  if (action.state === 'done') {
    text = t(`organize.status.done.${action.kind}`)
  } else if (action.state === 'stopped') {
    text = t('organize.status.stopped')
  }

  return (
    <div role="status" className="grid gap-2 rounded-md border bg-card p-3 text-sm">
      <p className="font-medium">{text}</p>
      {!isActive(action) && notAll && <p>{t('organize.status.notAll')}</p>}
      <div className="flex flex-wrap items-center gap-2">
        {action.kind !== 'undo' && action.undo.possible && (
          <Button
            size="sm"
            variant="outline"
            disabled={organize.pending}
            onClick={() => organize.start((csrfToken) => planUndo(action.id, null, csrfToken))}
          >
            {t('organize.status.undo')}
          </Button>
        )}
        <Button asChild size="sm" variant="link" className="px-1">
          <Link to="/history">{t('organize.status.history')}</Link>
        </Button>
        <Button size="sm" variant="ghost" onClick={organize.clear}>
          {t('organize.status.close')}
        </Button>
      </div>
    </div>
  )
}
