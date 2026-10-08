import { useInfiniteQuery, useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { fetchKept, isCleanupPlan, keptQueryKey } from '@/api/cleanup'
import {
  actionItemsQueryKey,
  entryOps,
  exportUrl,
  fetchActionItems,
  runAction,
  type Item,
  type PlanResult,
  type RunResult,
} from '@/api/organize'
import { ErrorBanner } from '@/app/ErrorBanner'
import { useCsrfToken } from '@/app/session'
import { Button } from '@/components/ui/button'
import { Dialog } from '@/components/ui/dialog'
import { useFormat } from '@/lib/format'
import { FolderChooser } from '@/organize/FolderChooser'
import { useActionTitle } from '@/organize/useOrganize'

// The groups of a cleanup or restore plan's entries, in the order they are
// shown.
const groups = ['planned', 'blocked', 'conflict', 'refused'] as const

// CleanupPreview shows a cleanup or restore plan before it runs (R4 design
// D3, D6): how many entries it moves, blocks, or leaves out, its total
// size, for a cleanup what is known of copies, and every entry, paged, by
// group with its reason. A blocked entry lists the kept entries that block
// it. Running it is the approval; Close leaves the plan to expire. A
// restore with conflicts can be planned again into a chosen folder.
export function CleanupPreview({
  plan,
  onRan,
  onClose,
  onChooseDestination,
}: {
  plan: PlanResult
  onRan: (result: RunResult) => void
  onClose: () => void
  onChooseDestination?: (destinationId: string) => void
}) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const csrfToken = useCsrfToken()
  const actionTitle = useActionTitle()
  const [choosing, setChoosing] = useState(false)
  const { action } = plan
  const counts = action.entries ?? action.counts
  const restore = action.kind === 'restore'
  const filter = { ops: entryOps(action.kind) ?? undefined }

  const pages = useInfiniteQuery({
    queryKey: [...actionItemsQueryKey(action.id, filter), 'preview'],
    queryFn: ({ pageParam, signal }) => fetchActionItems(action.id, filter, pageParam, signal),
    initialPageParam: null as string | null,
    getNextPageParam: (page) => page.next_cursor,
    staleTime: Infinity,
  })
  const items = pages.data?.pages.flatMap((page) => page.items) ?? []

  const run = useMutation({
    mutationFn: () => runAction(action.id, csrfToken),
    onSuccess: onRan,
  })

  const files = t('units.files', { count: action.files, formatted: fmt.count(action.files) })
  return (
    <>
      <Dialog title={actionTitle(action)} onClose={onClose} alert className="max-w-2xl">
        <ul aria-label={t('cleanup.preview.counts')} className="grid gap-1 text-sm">
          {groups.map(
            (state) =>
              (state === 'planned' || counts[state] > 0) && (
                <li key={state} className={state === 'planned' ? 'font-medium' : undefined}>
                  {t(`cleanup.preview.${restore ? 'restore' : 'cleanup'}.${state}`, {
                    count: counts[state],
                    formatted: fmt.count(counts[state]),
                  })}
                </li>
              ),
          )}
          {counts.planned > 0 && <li>{t('organize.preview.total', { files, bytes: fmt.bytes(action.bytes) })}</li>}
        </ul>

        {isCleanupPlan(plan) && (
          <section aria-label={t('cleanup.summary.title')} className="grid gap-1 rounded-md border p-3 text-sm">
            <h3 className="font-semibold">{t('cleanup.summary.title')}</h3>
            <p>{t('cleanup.summary.withCopy', { bytes: fmt.bytes(plan.summary.with_copy_bytes) })}</p>
            <p>{t('cleanup.summary.noCopy', { bytes: fmt.bytes(plan.summary.no_copy_bytes) })}</p>
            <p>{t('cleanup.summary.unchecked', { bytes: fmt.bytes(plan.summary.unchecked_bytes) })}</p>
            <p className={plan.summary.personal_items > 0 ? 'font-medium text-amber-900' : undefined}>
              {t('cleanup.summary.personal', {
                count: plan.summary.personal_items,
                formatted: fmt.count(plan.summary.personal_items),
              })}
            </p>
            <p className="text-muted-foreground">{t('cleanup.summary.help')}</p>
          </section>
        )}

        {restore && counts.conflict > 0 && onChooseDestination !== undefined && (
          <div className="grid justify-items-start gap-2 text-sm">
            <p>{t('cleanup.preview.restoreConflicts')}</p>
            <Button size="sm" variant="outline" onClick={() => setChoosing(true)}>
              {t('organize.preview.chooseDestination')}
            </Button>
          </div>
        )}

        <div className="grid max-h-[50vh] gap-3 overflow-y-auto">
          {pages.isPending && (
            <p role="status" className="text-sm text-muted-foreground">
              {t('app.loading')}
            </p>
          )}
          {pages.isError && <ErrorBanner error={pages.error} onRetry={() => void pages.refetch()} />}
          {groups.map((state) => {
            const group = items.filter((item) => item.state === state)
            const label = t(`cleanup.preview.groups.${state}`)
            return (
              group.length > 0 && (
                <section key={state} className="grid gap-1">
                  <h3 className="text-sm font-semibold">{label}</h3>
                  <ul aria-label={label} className="grid gap-1">
                    {group.map((item) => (
                      <EntryItem key={item.id} actionId={action.id} item={item} restore={restore} />
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
        </div>

        {counts.planned === 0 && <p className="text-sm">{t('organize.preview.nothing')}</p>}
        <p className="text-sm text-muted-foreground">
          {action.kind === 'cleanup' ? t('cleanup.preview.expiry') : t('cleanup.preview.restoreExpiry')}
          {action.expires_at !== null && ` ${t('cleanup.preview.until', { time: fmt.dateTime(action.expires_at) })}`}
        </p>
        {run.isError && (
          <ErrorBanner error={run.error} overrides={{ action_expired: t('cleanup.preview.expired') }} />
        )}
        <div className="flex flex-wrap items-center justify-end gap-2">
          <Button asChild variant="link" className="mr-auto px-1">
            <a href={exportUrl(action.id)} download>
              {t('cleanup.export')}
            </a>
          </Button>
          <Button variant="outline" onClick={onClose}>
            {t('cleanup.preview.close')}
          </Button>
          <Button disabled={counts.planned === 0 || run.isPending} onClick={() => run.mutate()}>
            {run.isPending
              ? t('organize.preview.confirming')
              : restore
                ? t('cleanup.preview.runRestore')
                : t('cleanup.preview.run')}
          </Button>
        </div>
      </Dialog>
      {choosing && onChooseDestination !== undefined && (
        <FolderChooser
          title={t('cleanup.restoreChooser')}
          sourceIds={[action.source_id]}
          onChoose={(folder) => {
            setChoosing(false)
            onChooseDestination(folder.id)
          }}
          onClose={() => setChoosing(false)}
        />
      )}
    </>
  )
}

// EntryItem is one entry of a cleanup or restore plan: its path (where it
// is now, or for a restore where it goes back to), its size, and the reason
// it is blocked, in conflict, or left out. A blocked entry shows the kept
// entries inside it on demand.
function EntryItem({ actionId, item, restore }: { actionId: string; item: Item; restore: boolean }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const [open, setOpen] = useState(false)
  const kept = item.kept_count ?? 0
  return (
    <li className="grid gap-1 rounded-md border px-2 py-1.5 text-sm">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3">
        <span className="min-w-0 break-all">{(restore ? item.to?.path : null) ?? item.from?.path ?? ''}</span>
        <span className="text-muted-foreground">
          {fmt.bytes(item.bytes)} · {t('units.files', { count: item.files, formatted: fmt.count(item.files) })}
        </span>
      </div>
      {item.reason !== null && <span className="text-muted-foreground">{t(`organize.reason.${item.reason}`)}</span>}
      {item.state === 'blocked' && kept > 0 && (
        <div className="grid justify-items-start gap-1">
          <Button size="sm" variant="ghost" aria-expanded={open} onClick={() => setOpen(!open)}>
            {open
              ? t('cleanup.kept.hide')
              : t('cleanup.kept.show', { count: kept, formatted: fmt.count(kept) })}
          </Button>
          {open && <KeptEntries actionId={actionId} itemId={item.id} />}
        </div>
      )}
    </li>
  )
}

// KeptEntries lists the kept entries that block a cleanup item, 100 to a
// page.
function KeptEntries({ actionId, itemId }: { actionId: string; itemId: string }) {
  const { t } = useTranslation()
  const pages = useInfiniteQuery({
    queryKey: keptQueryKey(actionId, itemId),
    queryFn: ({ pageParam, signal }) => fetchKept(actionId, itemId, pageParam, signal),
    initialPageParam: null as string | null,
    getNextPageParam: (page) => page.next_cursor,
  })
  const rows = pages.data?.pages.flatMap((page) => page.items) ?? []
  return (
    <div className="grid w-full gap-1">
      {pages.isPending && (
        <p role="status" className="text-muted-foreground">
          {t('app.loading')}
        </p>
      )}
      {pages.isError && <ErrorBanner error={pages.error} onRetry={() => void pages.refetch()} />}
      {rows.length > 0 && (
        <ul aria-label={t('cleanup.kept.list')} className="grid gap-0.5 pl-3">
          {rows.map((row) => (
            <li key={row.id} className="break-all">
              {row.path}
            </li>
          ))}
        </ul>
      )}
      {pages.hasNextPage && (
        <div>
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
    </div>
  )
}
