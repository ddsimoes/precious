import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { Link } from 'react-router'

import { isDateOrganizePlan, isSetMtimePlan } from '@/api/dates'
import { maxBulkIds, refreshAfterDecision, setDecision } from '@/api/decisions'
import { actionItemsQueryKey, fetchActionItems, type PlanResult } from '@/api/organize'
import { ErrorBanner } from '@/app/ErrorBanner'
import { useCsrfToken } from '@/app/session'
import { Button } from '@/components/ui/button'
import { Dialog } from '@/components/ui/dialog'
import { useFormat } from '@/lib/format'

const refused = { states: ['refused'] as const }

// DatePlanNotices adds to the preview of a date plan what its summary says
// (R5 design D14, D16, D17): the files already at their date; the copies
// among the files, with a link to the duplicates list; the files that leave
// a file of the same name behind; and the identical copies refused, which
// the owner may discard after a confirmation.
export function DatePlanNotices({ plan }: { plan: PlanResult }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  if (isSetMtimePlan(plan)) {
    const { unchanged } = plan.summary
    return unchanged > 0 ? (
      <p className="text-sm">{t('dates.preview.unchanged', { count: unchanged, formatted: fmt.count(unchanged) })}</p>
    ) : null
  }
  if (!isDateOrganizePlan(plan)) {
    return null
  }
  const { files_with_copies: copies, split_siblings: siblings } = plan.summary
  return (
    <>
      {copies > 0 && (
        <p role="status" className="rounded-md border border-amber-300 bg-amber-50 p-2 text-sm">
          <Trans
            i18nKey="dates.preview.copies"
            components={{
              duplicatesLink: (
                <Link
                  to={{
                    pathname: '/opportunities/duplicates',
                    search: `?${new URLSearchParams({ source: plan.action.source_id })}`,
                  }}
                  className="font-medium text-primary underline"
                />
              ),
            }}
          />
        </p>
      )}
      {siblings > 0 && (
        <p role="status" className="rounded-md border border-amber-300 bg-amber-50 p-2 text-sm">
          {t('dates.preview.siblings', { count: siblings, formatted: fmt.count(siblings) })}
        </p>
      )}
      {plan.action.counts.refused > 0 && <IdenticalCopies actionId={plan.action.id} />}
    </>
  )
}

// IdenticalCopies counts the files refused because an identical copy takes
// their name, and offers to discard them: set-decision discard on their
// entries, 1,000 to a request, only once the owner confirms.
function IdenticalCopies({ actionId }: { actionId: string }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const queryClient = useQueryClient()
  const csrfToken = useCsrfToken()
  const [confirming, setConfirming] = useState(false)

  const copies = useQuery({
    queryKey: [...actionItemsQueryKey(actionId, refused), 'identical_copy'],
    queryFn: async ({ signal }) => {
      const ids: string[] = []
      let cursor: string | null = null
      do {
        const page = await fetchActionItems(actionId, refused, cursor, signal)
        for (const item of page.items) {
          if (item.reason === 'identical_copy' && item.entry !== null) {
            ids.push(item.entry.id)
          }
        }
        cursor = page.next_cursor
      } while (cursor !== null)
      return ids
    },
    staleTime: Infinity,
  })

  const discard = useMutation({
    mutationFn: async (ids: string[]) => {
      let applied = 0
      let skipped = 0
      for (let start = 0; start < ids.length; start += maxBulkIds) {
        const result = await setDecision({ entry_ids: ids.slice(start, start + maxBulkIds) }, 'discard', csrfToken)
        applied += result.applied
        skipped += result.skipped_count
      }
      return { applied, skipped }
    },
    onSuccess: () => {
      setConfirming(false)
      return refreshAfterDecision(queryClient)
    },
  })

  const ids = copies.data ?? []
  if (copies.isError) {
    return <ErrorBanner error={copies.error} onRetry={() => void copies.refetch()} />
  }
  if (ids.length === 0) {
    return null
  }
  const count = ids.length

  return (
    <div className="grid justify-items-start gap-2 text-sm">
      <p>{t('dates.preview.identical', { count, formatted: fmt.count(count) })}</p>
      {discard.data === undefined ? (
        <Button size="sm" variant="outline" disabled={discard.isPending} onClick={() => setConfirming(true)}>
          {t('dates.preview.discard')}
        </Button>
      ) : (
        <p role="status" className="font-medium">
          {t('dates.preview.discarded', { count: discard.data.applied, formatted: fmt.count(discard.data.applied) })}
          {discard.data.skipped > 0 &&
            ` ${t('dates.preview.discardSkipped', { count: discard.data.skipped, formatted: fmt.count(discard.data.skipped) })}`}
        </p>
      )}
      {confirming && (
        <Dialog
          title={t('dates.preview.discardTitle', { count, formatted: fmt.count(count) })}
          description={t('dates.preview.discardHelp')}
          onClose={() => setConfirming(false)}
          alert
        >
          {discard.isError && <ErrorBanner error={discard.error} />}
          <div className="flex justify-end gap-2">
            <Button variant="outline" onClick={() => setConfirming(false)}>
              {t('dates.preview.discardCancel')}
            </Button>
            <Button disabled={discard.isPending} onClick={() => discard.mutate(ids)}>
              {discard.isPending ? t('dates.preview.discarding') : t('dates.preview.discardConfirm')}
            </Button>
          </div>
        </Dialog>
      )}
    </div>
  )
}
