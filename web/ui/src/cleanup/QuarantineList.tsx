import { useInfiniteQuery, useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useNavigate } from 'react-router'

import { checkPurge, fetchQuarantine, quarantineQueryKey, type Quarantined } from '@/api/cleanup'
import { maxBulkIds } from '@/api/decisions'
import type { Source } from '@/api/sources'
import { ErrorBanner } from '@/app/ErrorBanner'
import { useCsrfToken } from '@/app/session'
import { CleanupOutcome } from '@/cleanup/CleanupOutcome'
import { useRestore } from '@/cleanup/useRestore'
import { Button } from '@/components/ui/button'
import { useFormat } from '@/lib/format'

// maxCheckItems is the most items one check-purge may hold.
const maxCheckItems = 10_000

// QuarantineList is the quarantine browser of one source (R4 design D1,
// D6, D7): its items newest first, each with where it came from, its size,
// when it was quarantined, and its last check. The owner selects items and
// restores them, or checks them before deleting them for good.
export function QuarantineList({ source }: { source: Source }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const navigate = useNavigate()
  const csrfToken = useCsrfToken()
  const [selected, setSelected] = useState<ReadonlySet<string>>(() => new Set())
  const { organize, restore, chooseDestination } = useRestore()

  const pages = useInfiniteQuery({
    queryKey: quarantineQueryKey(source.id),
    queryFn: ({ pageParam, signal }) => fetchQuarantine(source.id, pageParam, signal),
    initialPageParam: null as string | null,
    getNextPageParam: (page) => page.next_cursor,
  })
  const items = pages.data?.pages.flatMap((page) => page.items) ?? []
  const total = pages.data?.pages[0]?.total
  // A selected item that left the quarantine is no longer selected.
  const chosen = items.filter((item) => selected.has(item.entry.id)).map((item) => item.entry.id)

  const check = useMutation({
    mutationFn: () => checkPurge({ entry_ids: chosen }, csrfToken),
    onSuccess: (started) => void navigate(`/cleanup/checks/${encodeURIComponent(started.check_id)}`),
  })

  const toggle = (id: string) =>
    setSelected((current) => {
      const next = new Set(current)
      if (next.has(id)) {
        next.delete(id)
      } else {
        next.add(id)
      }
      return next
    })

  const writable = source.writes.enabled && source.state === 'online'
  const listLabel = t('cleanup.quarantine.list', { source: source.label })

  return (
    <div className="grid gap-3">
      {pages.isPending && (
        <p role="status" className="text-sm text-muted-foreground">
          {t('app.loading')}
        </p>
      )}
      {pages.isError && <ErrorBanner error={pages.error} onRetry={() => void pages.refetch()} />}
      {total !== undefined && (
        <p className="text-sm">
          {t('cleanup.quarantine.total', {
            files: t('units.files', { count: total.files, formatted: fmt.count(total.files) }),
            bytes: fmt.bytes(total.bytes),
          })}
        </p>
      )}
      {pages.data !== undefined && items.length === 0 && (
        <p className="text-sm text-muted-foreground">{t('cleanup.quarantine.empty')}</p>
      )}
      {items.length > 0 && (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm font-medium">
              {t('cleanup.quarantine.selected', { count: chosen.length, formatted: fmt.count(chosen.length) })}
            </span>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => setSelected(new Set(items.map((item) => item.entry.id)))}
            >
              {t('cleanup.quarantine.selectShown')}
            </Button>
            {chosen.length > 0 && (
              <Button size="sm" variant="ghost" onClick={() => setSelected(new Set())}>
                {t('cleanup.quarantine.clear')}
              </Button>
            )}
            <Button
              size="sm"
              variant="outline"
              disabled={chosen.length === 0 || chosen.length > maxBulkIds || !writable || organize.pending}
              onClick={() => restore(chosen)}
            >
              {t('cleanup.quarantine.restore')}
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={chosen.length === 0 || chosen.length > maxCheckItems || check.isPending}
              onClick={() => check.mutate()}
            >
              {check.isPending ? t('cleanup.quarantine.checking') : t('cleanup.quarantine.check')}
            </Button>
          </div>
          {!writable && <p className="text-sm text-muted-foreground">{t('cleanup.quarantine.restoreOff')}</p>}
          {chosen.length > maxBulkIds && (
            <p className="text-sm text-muted-foreground">
              {t('cleanup.quarantine.tooMany', { formatted: fmt.count(maxBulkIds) })}
            </p>
          )}
          <p className="text-sm text-muted-foreground">{t('cleanup.quarantine.checkHelp')}</p>
          {check.isError && <ErrorBanner error={check.error} onDismiss={() => check.reset()} />}
          <CleanupOutcome organize={organize} onChooseDestination={chooseDestination} />
          <ul aria-label={listLabel} className="grid gap-2">
            {items.map((item) => (
              <QuarantinedItem
                key={item.entry.id}
                item={item}
                selected={selected.has(item.entry.id)}
                onToggle={() => toggle(item.entry.id)}
              />
            ))}
          </ul>
        </>
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

function QuarantinedItem({
  item,
  selected,
  onToggle,
}: {
  item: Quarantined
  selected: boolean
  onToggle: () => void
}) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const name = item.original?.path ?? item.entry.name
  return (
    <li className="grid gap-1 rounded-md border bg-card p-2 text-sm">
      <label className="flex items-start gap-2">
        <input type="checkbox" className="mt-1" checked={selected} onChange={onToggle} />
        <span className="min-w-0 font-medium break-all">{name}</span>
      </label>
      <p className="pl-6 text-muted-foreground">
        {[
          ...(item.original === null ? [t('cleanup.originUnknown')] : []),
          fmt.bytes(item.bytes),
          t('units.files', { count: item.files, formatted: fmt.count(item.files) }),
          item.quarantined_at === null
            ? t('cleanup.quarantine.whenUnknown')
            : t('cleanup.quarantine.when', { time: fmt.dateTime(item.quarantined_at) }),
        ].join(' · ')}
      </p>
      <p className="pl-6">
        {item.check === null ? (
          <span className="text-muted-foreground">{t('cleanup.quarantine.notChecked')}</span>
        ) : (
          <Link
            to={`/cleanup/checks/${encodeURIComponent(item.check.id)}`}
            className="font-medium text-primary hover:underline"
          >
            {t(`cleanup.quarantine.checkState.${item.check.state}`)}
          </Link>
        )}
      </p>
    </li>
  )
}
