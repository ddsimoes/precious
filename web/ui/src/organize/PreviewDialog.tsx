import { useInfiniteQuery, useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  actionItemsQueryKey,
  fetchActionItems,
  planUndo,
  runAction,
  type PlanResult,
  type RunResult,
} from '@/api/organize'
import { ErrorBanner } from '@/app/ErrorBanner'
import { useCsrfToken } from '@/app/session'
import { Button } from '@/components/ui/button'
import { Dialog } from '@/components/ui/dialog'
import { DatePlanNotices } from '@/dates/DatePlanNotices'
import { useFormat } from '@/lib/format'
import { FolderChooser } from '@/organize/FolderChooser'
import { ItemLine } from '@/organize/ItemLine'
import { useActionTitle } from '@/organize/useOrganize'

// The groups of a planned action's items, in the order they are shown.
const groups = ['planned', 'conflict', 'refused'] as const

// PreviewDialog shows a planned action before it runs (R3 design D9, R3.4):
// its counts, every item paged from the history with its path before and
// after, the items left as they are and those not included with their
// reasons, and the kept items that would no longer be kept. A date plan
// adds what its summary says (R5). Confirm runs it; Cancel leaves it to
// expire. An undo with conflicts can be planned again into a chosen folder.
export function PreviewDialog({
  plan,
  onRan,
  onClose,
}: {
  plan: PlanResult
  onRan: (result: RunResult) => void
  onClose: () => void
}) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const csrfToken = useCsrfToken()
  const actionTitle = useActionTitle()
  const [current, setCurrent] = useState(plan)
  const [choosing, setChoosing] = useState(false)
  const { action } = current
  const counts = action.counts

  const pages = useInfiniteQuery({
    queryKey: [...actionItemsQueryKey(action.id), 'preview'],
    queryFn: ({ pageParam, signal }) => fetchActionItems(action.id, {}, pageParam, signal),
    initialPageParam: null as string | null,
    getNextPageParam: (page) => page.next_cursor,
    initialData: { pages: [{ items: current.items, next_cursor: current.next_cursor }], pageParams: [null] },
    staleTime: Infinity,
  })
  const items = pages.data.pages.flatMap((page) => page.items)

  const run = useMutation({
    mutationFn: () => runAction(action.id, csrfToken),
    onSuccess: onRan,
  })
  const replan = useMutation({
    mutationFn: (destination: string) => planUndo(action.undo_of ?? '', destination, csrfToken),
    onSuccess: (next) => {
      setCurrent(next)
      run.reset()
    },
  })

  return (
    <>
      <Dialog title={actionTitle(action)} onClose={onClose} alert className="max-w-2xl">
        <ul aria-label={t('history.counts')} className="grid gap-1 text-sm">
          {groups.map(
            (state) =>
              (state === 'planned' || counts[state] > 0) && (
                <li key={state} className={state === 'planned' ? 'font-medium' : undefined}>
                  {t(`organize.preview.${state}`, { count: counts[state], formatted: fmt.count(counts[state]) })}
                </li>
              ),
          )}
          {counts.planned > 0 && (
            <li>
              {t('organize.preview.total', {
                files: t('units.files', { count: action.files, formatted: fmt.count(action.files) }),
                bytes: fmt.bytes(action.bytes),
              })}
            </li>
          )}
        </ul>
        {action.kept_lost > 0 && (
          <p role="alert" className="rounded-md border border-amber-300 bg-amber-50 p-2 text-sm font-medium">
            {t('organize.preview.keptLost', { count: action.kept_lost, formatted: fmt.count(action.kept_lost) })}
          </p>
        )}
        <DatePlanNotices plan={current} />
        {action.kind === 'undo' && counts.conflict > 0 && action.undo_of !== null && (
          <div className="grid justify-items-start gap-2 text-sm">
            <p>{t('organize.preview.undoConflicts')}</p>
            <Button size="sm" variant="outline" disabled={replan.isPending} onClick={() => setChoosing(true)}>
              {t('organize.preview.chooseDestination')}
            </Button>
          </div>
        )}
        {replan.isError && <ErrorBanner error={replan.error} onDismiss={() => replan.reset()} />}

        <div className="grid max-h-[50vh] gap-3 overflow-y-auto">
          {groups.map((state) => {
            const group = items.filter((item) => item.state === state)
            return (
              group.length > 0 && (
                <section key={state} className="grid gap-1">
                  <h3 className="text-sm font-semibold">{t(`organize.preview.groups.${state}`)}</h3>
                  <ul aria-label={t(`organize.preview.groups.${state}`)} className="grid gap-1">
                    {group.map((item) => (
                      <ItemLine key={item.id} item={item} />
                    ))}
                  </ul>
                </section>
              )
            )
          })}
          {pages.hasNextPage && (
            <div className="flex justify-center">
              <Button
                variant="outline"
                size="sm"
                disabled={pages.isFetchingNextPage}
                onClick={() => void pages.fetchNextPage()}
              >
                {pages.isFetchingNextPage ? t('map.loadingMore') : t('map.loadMore')}
              </Button>
            </div>
          )}
          {pages.isFetchNextPageError && <ErrorBanner error={pages.error} />}
        </div>

        {counts.planned === 0 && <p className="text-sm">{t('organize.preview.nothing')}</p>}
        <p className="text-sm text-muted-foreground">{t('organize.preview.expiry')}</p>
        {run.isError && <ErrorBanner error={run.error} />}
        <div className="flex justify-end gap-2">
          <Button variant="outline" onClick={onClose}>
            {t('organize.preview.cancel')}
          </Button>
          <Button disabled={counts.planned === 0 || run.isPending || replan.isPending} onClick={() => run.mutate()}>
            {run.isPending ? t('organize.preview.confirming') : t('organize.preview.confirm')}
          </Button>
        </div>
      </Dialog>
      {choosing && (
        <FolderChooser
          title={t('organize.chooser.undoTitle')}
          sourceIds={[action.source_id]}
          onChoose={(folder) => {
            setChoosing(false)
            replan.mutate(folder.id)
          }}
          onClose={() => setChoosing(false)}
        />
      )}
    </>
  )
}
