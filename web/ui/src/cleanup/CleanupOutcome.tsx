import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'

import { refreshAfterCleanup } from '@/api/cleanup'
import {
  actionItemsQueryKey,
  actionQueryKey,
  fetchAction,
  fetchActionItems,
  isActive,
  type Action,
  type ItemsFilter,
} from '@/api/organize'
import { useSources } from '@/api/sources'
import { ErrorBanner } from '@/app/ErrorBanner'
import { Button } from '@/components/ui/button'
import { useFormat } from '@/lib/format'
import { CleanupPreview } from '@/cleanup/CleanupPreview'
import { ItemLine } from '@/organize/ItemLine'
import type { Organize } from '@/organize/useOrganize'

// CleanupOutcome shows what a cleanup or restore started with useOrganize is
// doing: the plan's error, its preview, and the action last run, live.
export function CleanupOutcome({
  organize,
  onChooseDestination,
}: {
  organize: Organize
  onChooseDestination?: (destinationId: string) => void
}) {
  return (
    <>
      {organize.error !== null && (
        <ErrorBanner error={organize.error} overrides={organize.overrides} onDismiss={organize.dismissError} />
      )}
      {organize.ran !== null && <CleanupStatus ran={organize.ran} onClose={organize.clear} />}
      {organize.preview !== null && (
        <CleanupPreview
          key={organize.preview.action.id}
          plan={organize.preview}
          onRan={organize.onRan}
          onClose={organize.closePreview}
          onChooseDestination={onChooseDestination}
        />
      )}
    </>
  )
}

// CleanupStatus follows a cleanup, restore, or purge that was sent to run
// until it ends, then refetches what it changed. A purge reports what it
// deleted and the space it freed, and on ZFS that snapshots may keep that
// space (R4 design D11); a purge that stopped shows its comparison with the
// disk when that did not pass: what changed, and where.
export function CleanupStatus({ ran, onClose }: { ran: Action; onClose: () => void }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const queryClient = useQueryClient()
  const sources = useSources()
  const status = useQuery({
    queryKey: actionQueryKey(ran.id),
    queryFn: ({ signal }) => fetchAction(ran.id, signal),
    initialData: ran,
  })
  const action = status.data
  const active = isActive(action)
  useEffect(() => {
    if (!active) {
      void refreshAfterCleanup(queryClient)
    }
  }, [active, queryClient])

  // A purge whose comparison with the disk found a change stops with
  // nothing deleted; its verify step says what changed.
  const stopped = action.kind === 'purge' && action.state === 'stopped'
  const verifyFilter: ItemsFilter = { ops: ['verify'] }
  const verify = useQuery({
    queryKey: actionItemsQueryKey(action.id, verifyFilter),
    queryFn: ({ signal }) => fetchActionItems(action.id, verifyFilter, null, signal),
    enabled: stopped,
  })
  const unverified = stopped ? (verify.data?.items.filter((item) => item.state !== 'done') ?? []) : []

  const c = action.entries ?? action.counts
  const notAll =
    c.conflict + c.refused + c.blocked + c.failed + c.changed + c.not_permitted + c.offline + c.no_safe_rename +
      c.not_empty + c.not_attempted + c.manual_recovery > 0
  const zfs = sources.data?.sources.find((s) => s.id === action.source_id)?.volume.fs_type === 'zfs'

  let text: string = t(`organize.status.pending.${action.kind}`)
  if (action.state === 'done') {
    text = t(`organize.status.done.${action.kind}`)
  } else if (action.state === 'stopped') {
    text = t('organize.status.stopped')
  }

  return (
    <div role="status" className="grid gap-2 rounded-md border bg-card p-3 text-sm">
      <p className="font-medium">{text}</p>
      {action.kind === 'purge' && !active && (
        <>
          <p>
            {t('cleanup.purge.deleted', {
              files: t('units.files', { count: action.deleted_files, formatted: fmt.count(action.deleted_files) }),
              bytes: fmt.bytes(action.deleted_bytes),
            })}
          </p>
          <p className="font-medium">{t('cleanup.purge.freed', { bytes: fmt.bytes(action.freed_bytes) })}</p>
          {zfs && <p>{t('cleanup.purge.zfs')}</p>}
        </>
      )}
      {unverified.length > 0 && (
        <ul aria-label={t('cleanup.purge.stoppedAt')} className="grid gap-1">
          {unverified.map((item) => (
            <ItemLine key={item.id} item={item} showState />
          ))}
        </ul>
      )}
      {!active && notAll && <p>{t('organize.status.notAll')}</p>}
      <div className="flex flex-wrap items-center gap-2">
        <Button asChild size="sm" variant="link" className="px-1">
          <Link to="/history">{t('organize.status.history')}</Link>
        </Button>
        <Button size="sm" variant="ghost" onClick={onClose}>
          {t('organize.status.close')}
        </Button>
      </div>
    </div>
  )
}
