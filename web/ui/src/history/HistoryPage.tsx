import { useInfiniteQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  actionItemsQueryKey,
  cancelAction,
  cleanupKinds,
  entryOps,
  exportUrl,
  fetchActionItems,
  fetchHistory,
  historyQueryKey,
  historyQueryRoot,
  isActive,
  planUndo,
  resolveRecovery,
  type Action,
  type Item,
} from '@/api/organize'
import { sourcesQueryKey } from '@/api/sources'
import { ErrorBanner } from '@/app/ErrorBanner'
import { PageTitle } from '@/app/PageTitle'
import { useCsrfToken } from '@/app/session'
import { Button } from '@/components/ui/button'
import { useFormat } from '@/lib/format'
import { useSourceLabel } from '@/lib/sourceParams'
import { ItemLine } from '@/organize/ItemLine'
import { OrganizeOutcome } from '@/organize/OrganizeOutcome'
import { useActionTitle, useOrganize } from '@/organize/useOrganize'

// HistoryPage lists every action that was run, newest first (R3 design
// D16): what it did, when, its state and item counts, with Undo (never for
// a cleanup, restore, or purge, R4 design D13), Cancel while it waits or
// runs, its items on demand, the export of its items as CSV (D16), and the
// items that need the owner's check. Organize job events keep it live.
export function HistoryPage() {
  const { t } = useTranslation()
  const pages = useInfiniteQuery({
    queryKey: historyQueryKey(),
    queryFn: ({ pageParam, signal }) => fetchHistory(pageParam, signal),
    initialPageParam: null as string | null,
    getNextPageParam: (page) => page.next_cursor,
  })
  const actions = pages.data?.pages.flatMap((page) => page.items) ?? []

  return (
    <div className="grid max-w-4xl gap-4">
      <PageTitle>{t('pages.history')}</PageTitle>
      <p className="text-sm text-muted-foreground">{t('history.help')}</p>
      {pages.isPending && (
        <p role="status" className="text-sm text-muted-foreground">
          {t('app.loading')}
        </p>
      )}
      {pages.isError && <ErrorBanner error={pages.error} onRetry={() => void pages.refetch()} />}
      {pages.data !== undefined &&
        (actions.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t('history.empty')}</p>
        ) : (
          <ul aria-label={t('history.list')} className="grid gap-3">
            {actions.map((action) => (
              <li key={action.id}>
                <ActionCard action={action} />
              </li>
            ))}
          </ul>
        ))}
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
  )
}

function ActionCard({ action }: { action: Action }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const queryClient = useQueryClient()
  const csrfToken = useCsrfToken()
  const actionTitle = useActionTitle()
  const sourceLabel = useSourceLabel()
  const headingId = useId()
  const [open, setOpen] = useState(false)
  const organize = useOrganize()

  const cancel = useMutation({
    mutationFn: () => cancelAction(action.id, csrfToken),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: historyQueryRoot }),
  })

  // A cleanup, restore, or purge counts its entries; other kinds count
  // their steps. Every way an item can end without being done counts as
  // not done.
  const c = action.entries ?? action.counts
  const counts = [
    ['done', c.done],
    ['refused', c.refused],
    ['conflict', c.conflict],
    ['blocked', c.blocked],
    ['failed', c.failed + c.changed + c.not_permitted + c.offline + c.no_safe_rename + c.not_empty],
    ['not_attempted', c.not_attempted],
    ['manual_recovery', action.counts.manual_recovery],
  ] as const
  const undoable = action.undo.possible && !cleanupKinds.includes(action.kind)

  return (
    <article aria-labelledby={headingId} className="grid gap-2 rounded-lg border bg-card p-3 text-sm">
      <header className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <h2 id={headingId} className="font-semibold break-all">
          {actionTitle(action)}
        </h2>
        <span className="rounded-full bg-secondary px-2 py-0.5 text-xs font-medium">
          {t(`history.state.${action.state}`)}
        </span>
      </header>
      <p className="text-muted-foreground">
        {sourceLabel(action.source_id)} · {fmt.dateTime(action.finished_at ?? action.started_at ?? action.created_at)} ·{' '}
        {t('history.size', {
          files: t('units.files', { count: action.files, formatted: fmt.count(action.files) }),
          bytes: fmt.bytes(action.bytes),
        })}
      </p>
      {action.kind === 'purge' && action.state === 'done' && (
        <p>
          {t('history.purged', {
            files: t('units.files', { count: action.deleted_files, formatted: fmt.count(action.deleted_files) }),
            bytes: fmt.bytes(action.deleted_bytes),
            freed: fmt.bytes(action.freed_bytes),
          })}
        </p>
      )}
      <ul aria-label={t('history.counts')} className="flex flex-wrap gap-x-4 gap-y-1">
        {counts
          .filter(([key, count]) => key === 'done' || count > 0)
          .map(([key, count]) => (
            <li key={key} className={key === 'manual_recovery' ? 'font-medium text-amber-900' : undefined}>
              {t(`history.count.${key}`, { formatted: fmt.count(count) })}
            </li>
          ))}
      </ul>

      {action.counts.manual_recovery > 0 && (
        <section className="grid gap-2 rounded-md border border-amber-300 bg-amber-50 p-3">
          <h3 className="font-semibold">{t('history.recovery.title')}</h3>
          <p>{t('history.recovery.help')}</p>
          <ActionItems action={action} recovery />
        </section>
      )}

      <div className="flex flex-wrap items-center gap-2">
        {undoable && (
          <Button
            size="sm"
            variant="outline"
            disabled={organize.pending}
            onClick={() => organize.start((token) => planUndo(action.id, null, token))}
          >
            {t('history.undo')}
          </Button>
        )}
        {action.undo.reason === 'already_undone' && <span className="text-muted-foreground">{t('history.undone')}</span>}
        {isActive(action) && (
          <Button size="sm" variant="outline" disabled={cancel.isPending} onClick={() => cancel.mutate()}>
            {cancel.isPending ? t('history.cancelling') : t('history.cancel')}
          </Button>
        )}
        <Button size="sm" variant="ghost" aria-expanded={open} onClick={() => setOpen(!open)}>
          {open ? t('history.hideItems') : t('history.showItems')}
        </Button>
        <Button asChild size="sm" variant="link" className="px-1">
          <a href={exportUrl(action.id)} download>
            {t('history.export')}
          </a>
        </Button>
      </div>
      {cancel.isError && <ErrorBanner error={cancel.error} onDismiss={() => cancel.reset()} />}
      <OrganizeOutcome organize={organize} />
      {open && <ActionItems action={action} recovery={false} />}
    </article>
  )
}

// ActionItems lists an action's items, paged: all of them with their
// states, or only those that need the owner's check, each with I fixed it.
function ActionItems({ action, recovery }: { action: Action; recovery: boolean }) {
  const { t } = useTranslation()
  // A cleanup, restore, or purge lists one step per entry, and a purge its
  // comparison with the disk first; the steps that need a check are listed
  // whatever they do.
  const filter = recovery
    ? { states: ['manual_recovery'] as const }
    : { ops: entryOps(action.kind) ?? undefined }
  const pages = useInfiniteQuery({
    queryKey: actionItemsQueryKey(action.id, filter),
    queryFn: ({ pageParam, signal }) => fetchActionItems(action.id, filter, pageParam, signal),
    initialPageParam: null as string | null,
    getNextPageParam: (page) => page.next_cursor,
  })
  const items = pages.data?.pages.flatMap((page) => page.items) ?? []
  const label = recovery ? t('history.recovery.list') : t('history.items')

  return (
    <div className="grid gap-2">
      {pages.isPending && (
        <p role="status" className="text-muted-foreground">
          {t('app.loading')}
        </p>
      )}
      {pages.isError && <ErrorBanner error={pages.error} onRetry={() => void pages.refetch()} />}
      {items.length > 0 && (
        <ul aria-label={label} className="grid gap-1">
          {items.map((item) =>
            recovery ? <RecoveryItem key={item.id} item={item} /> : <ItemLine key={item.id} item={item} showState />,
          )}
        </ul>
      )}
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
  )
}

// RecoveryItem is a step Precious could not confirm: both of its paths and
// what was found at each. Once the owner put things right, I fixed it marks
// it resolved and scans the source again.
function RecoveryItem({ item }: { item: Item }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const csrfToken = useCsrfToken()
  const resolve = useMutation({
    mutationFn: () => resolveRecovery(item.id, csrfToken),
    onSuccess: () =>
      Promise.all([
        queryClient.invalidateQueries({ queryKey: historyQueryRoot }),
        queryClient.invalidateQueries({ queryKey: sourcesQueryKey }),
      ]),
  })

  return (
    <li className="grid gap-1 rounded-md border border-amber-300 bg-card p-2">
      <span className="break-all">{t('history.recovery.from', { path: item.from?.path ?? '' })}</span>
      <span className="break-all">{t('history.recovery.to', { path: item.to?.path ?? '' })}</span>
      {item.found !== null && (
        <span>
          {t('history.recovery.found', {
            from: t(`organize.found.${item.found.from}`),
            to: t(`organize.found.${item.found.to}`),
          })}
        </span>
      )}
      {resolve.isError && <ErrorBanner error={resolve.error} onDismiss={() => resolve.reset()} />}
      <div>
        <Button size="sm" variant="outline" disabled={resolve.isPending} onClick={() => resolve.mutate()}>
          {t('history.recovery.fixed')}
        </Button>
      </div>
    </li>
  )
}
