import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useCallback, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  historyQueryRoot,
  needsPreview,
  runAction,
  type Action,
  type PlanResult,
  type RunResult,
} from '@/api/organize'
import type { MessageOverrides } from '@/app/errors'
import { useCsrfToken } from '@/app/session'
import { useSourceLabel } from '@/lib/sourceParams'

// Plan asks the server for a plan with the session's CSRF token.
export type Plan = (csrfToken: string) => Promise<PlanResult>

export interface StartOptions {
  // always shows the preview, even for a plan that could run at once.
  always?: boolean
  // overrides words the plan's errors for the control that started it.
  overrides?: MessageOverrides
  // onPlanned is called once the plan was made, run or previewed.
  onPlanned?: () => void
}

export interface Organize {
  start: (plan: Plan, options?: StartOptions) => void
  pending: boolean
  error: Error | null
  overrides: MessageOverrides | undefined
  dismissError: () => void
  // preview is the plan waiting for the owner's confirmation.
  preview: PlanResult | null
  closePreview: () => void
  // ran is the action last run, whose result is shown until cleared.
  ran: Action | null
  onRan: (result: RunResult) => void
  clear: () => void
}

// useOrganize plans an action, then runs it at once when it is a single
// change without a conflict, a refused item, or a lost keep, and otherwise
// keeps it for the preview (R3 design D9). onRan is called once an action
// was sent to run.
export function useOrganize({ onRan, onError }: { onRan?: () => void; onError?: (error: Error) => void } = {}): Organize {
  const queryClient = useQueryClient()
  const csrfToken = useCsrfToken()
  const [preview, setPreview] = useState<PlanResult | null>(null)
  const [ran, setRan] = useState<Action | null>(null)
  const [overrides, setOverrides] = useState<MessageOverrides | undefined>(undefined)

  const ranAction = useCallback(
    (result: RunResult) => {
      setPreview(null)
      setRan(result.action)
      void queryClient.invalidateQueries({ queryKey: historyQueryRoot })
      onRan?.()
    },
    [queryClient, onRan],
  )

  const mutation = useMutation({
    mutationFn: async ({ plan, always }: { plan: Plan; always: boolean }) => {
      const planned = await plan(csrfToken)
      if (always || needsPreview(planned.action)) {
        return { planned, result: null }
      }
      return { planned, result: await runAction(planned.action.id, csrfToken) }
    },
    onSuccess: ({ planned, result }) => {
      if (result === null) {
        setPreview(planned)
      } else {
        ranAction(result)
      }
    },
    onError,
  })

  return {
    start: (plan, { always = false, overrides, onPlanned } = {}) => {
      setRan(null)
      setOverrides(overrides)
      mutation.mutate({ plan, always }, { onSuccess: onPlanned })
    },
    pending: mutation.isPending,
    error: mutation.error,
    overrides,
    dismissError: mutation.reset,
    preview,
    closePreview: () => setPreview(null),
    ran,
    onRan: ranAction,
    clear: () => setRan(null),
  }
}

// useActionTitle names an action by its kind and destination, the
// destination's path or, for a source's top folder, the source's name. A
// cleanup plan is named by the review list it was drafted from, if any; a
// cleanup and a purge have no destination of their own.
export function useActionTitle(): (action: Action) => string {
  const { t } = useTranslation()
  const sourceLabel = useSourceLabel()
  return (action) => {
    const destination = action.destination
    if (action.kind === 'cleanup' && action.list !== null) {
      return t('organize.titleFromList', { list: t(`opportunities.list.${action.list}`) })
    }
    if (destination === null || action.kind === 'cleanup' || action.kind === 'purge') {
      return t(`organize.titleNoDestination.${action.kind}`)
    }
    return t(`organize.title.${action.kind}`, {
      destination: destination.path === '' ? sourceLabel(destination.source_id) : destination.path,
    })
  }
}
