import { useInfiniteQuery, useMutation } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { Link, useNavigate, useSearchParams } from 'react-router'

import { buckets, compareQueryKey, fetchCompare, isBucket, type Bucket, type CompareItem } from '@/api/compare'
import { checkNow } from '@/api/content'
import { isMember, type EntryRow } from '@/api/entries'
import { planMerge } from '@/api/organize'
import { useSources } from '@/api/sources'
import { ErrorBanner } from '@/app/ErrorBanner'
import { PageTitle } from '@/app/PageTitle'
import { useCsrfToken } from '@/app/session'
import { DecisionButtons } from '@/components/DecisionButtons'
import { Button } from '@/components/ui/button'
import { DetailPanel } from '@/detail/DetailPanel'
import { useEntryLink } from '@/detail/useEntryLink'
import { useFormat } from '@/lib/format'
import { useSourceLabel } from '@/lib/sourceParams'
import { cn } from '@/lib/utils'
import { OrganizeOutcome } from '@/organize/OrganizeOutcome'
import { useOrganize } from '@/organize/useOrganize'

// ComparePage answers "which copy do I keep?" (spec §11.6, R2 design D11):
// two folders or archives side by side, named in the address
// (/compare?left=&right=&bucket=) so a comparison can be bookmarked, their
// files in five groups, each decided with the usual controls. Without a
// group in the address, the server opens on the first group that holds
// files and names it, and the address takes that group (r2b D11). The files
// only on one side of two folders can be moved into the other (R3 design
// D13).
export function ComparePage() {
  const { t } = useTranslation()
  const [params] = useSearchParams()
  const left = params.get('left') ?? ''
  const right = params.get('right') ?? ''
  const bucketParam = params.get('bucket')
  const bucket = isBucket(bucketParam) ? bucketParam : null

  if (left === '' || right === '') {
    return (
      <>
        <PageTitle>{t('pages.compare')}</PageTitle>
        <p className="text-sm text-muted-foreground">{t('compare.choose')}</p>
      </>
    )
  }
  return <Comparison key={`${left} ${right}`} left={left} right={right} bucket={bucket} />
}

function Comparison({ left, right, bucket }: { left: string; right: string; bucket: Bucket | null }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const csrfToken = useCsrfToken()
  const navigate = useNavigate()
  const [params] = useSearchParams()
  const [checkStarted, setCheckStarted] = useState(false)
  const sources = useSources()
  const sourceLabel = useSourceLabel()
  const organize = useOrganize()
  // opened is the group the server chose for an address without one: the
  // address then names it, and its page stays the one already fetched.
  const [opened, setOpened] = useState<Bucket | null>(null)
  const requested = bucket !== null && bucket === opened ? null : bucket

  const pages = useInfiniteQuery({
    queryKey: compareQueryKey(left, right, requested),
    queryFn: ({ pageParam, signal }) =>
      fetchCompare(left, right, pageParam?.bucket ?? requested, pageParam?.cursor ?? null, signal),
    initialPageParam: null as { bucket: Bucket; cursor: string } | null,
    getNextPageParam: (page) => (page.next_cursor === null ? null : { bucket: page.bucket, cursor: page.next_cursor }),
  })
  const check = useMutation({
    mutationFn: () => checkNow([left, right], csrfToken),
    onSuccess: () => setCheckStarted(true),
  })
  const first = pages.data?.pages[0]
  const items = pages.data?.pages.flatMap((page) => page.items) ?? []

  // The files only on one side move into the other side when both are
  // folders, not archives, of one source Precious may change.
  let merge: { from: 'left' | 'right'; into: EntryRow } | null = null
  if (first !== undefined && (first.bucket === 'only_left' || first.bucket === 'only_right') && items.length > 0) {
    const source = sources.data?.sources.find((s) => s.id === first.left.source_id)
    const folders = [first.left, first.right].every((side) => side.kind === 'directory' && !isMember(side))
    if (
      folders &&
      first.left.source_id === first.right.source_id &&
      source !== undefined &&
      source.writes.enabled &&
      source.state === 'online'
    ) {
      merge =
        first.bucket === 'only_left' ? { from: 'left', into: first.right } : { from: 'right', into: first.left }
    }
  }

  const bucketSearch = (b: Bucket) => {
    const next = new URLSearchParams(params)
    next.set('bucket', b)
    next.delete('entry')
    return `?${next}`
  }

  // The server chose the group: the address takes it, replacing itself, and
  // the page already fetched stays the group's.
  const opening = requested === null && first !== undefined && first.bucket !== bucket ? first.bucket : null
  if (opening !== null && opening !== opened) {
    setOpened(opening)
  }
  useEffect(() => {
    if (opening !== null) {
      const next = new URLSearchParams(params)
      next.set('bucket', opening)
      void navigate({ search: `?${next}` }, { replace: true })
    }
  }, [opening, params, navigate])

  return (
    <div className={cn('grid items-start gap-4', params.has('entry') && '3xl:grid-cols-[minmax(0,1fr)_26rem]')}>
      <div className="grid min-w-0 gap-4">
        <PageTitle>{t('pages.compare')}</PageTitle>
        {pages.isError && (
          <ErrorBanner
            error={pages.error}
            overrides={{ invalid_request: t('compare.invalid') }}
            onRetry={() => void pages.refetch()}
          />
        )}
        {(pages.isPending || opening !== null) && (
          <p role="status" className="text-sm text-muted-foreground">
            {t('app.loading')}
          </p>
        )}
        {first !== undefined && opening === null && (
          <>
            <section aria-label={t('compare.sides')} className="grid gap-3 md:grid-cols-2">
              <Side label={t('compare.left')} row={first.left} />
              <Side label={t('compare.right')} row={first.right} />
            </section>
            <div className="flex flex-wrap items-center gap-3">
              <Button size="sm" variant="outline" disabled={check.isPending} onClick={() => check.mutate()}>
                {check.isPending ? t('compare.checking') : t('compare.checkNow')}
              </Button>
              {checkStarted && (
                <p role="status" className="text-sm">
                  {t('compare.checkStarted')}
                </p>
              )}
            </div>
            {check.isError && <ErrorBanner error={check.error} onDismiss={() => check.reset()} />}

            <nav aria-label={t('compare.groups')}>
              <ul className="grid gap-2 sm:grid-cols-3 xl:grid-cols-5">
                {buckets.map((b) => {
                  const amount = first.summary[b]
                  return (
                    <li key={b}>
                      <Link
                        to={{ search: bucketSearch(b) }}
                        aria-current={b === first.bucket ? 'page' : undefined}
                        className={cn(
                          'grid h-full gap-0.5 rounded-lg border bg-card p-2 text-sm hover:bg-accent',
                          b === first.bucket && 'border-primary bg-primary/5',
                        )}
                      >
                        <span className="font-medium">{t(`compare.bucket.${b}`)}</span>
                        <span className="text-muted-foreground">
                          {t('compare.groupFigures', {
                            files: t('units.files', { count: amount.files, formatted: fmt.count(amount.files) }),
                            bytes: fmt.bytes(amount.bytes),
                          })}
                        </span>
                      </Link>
                    </li>
                  )
                })}
              </ul>
            </nav>

            {merge !== null && (
              <div className="grid gap-1 text-sm">
                <div>
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={organize.pending}
                    onClick={() =>
                      organize.start((csrfToken) => planMerge(first.left.id, first.right.id, merge.from, csrfToken), {
                        always: true,
                      })
                    }
                  >
                    {t('compare.mergeInto', {
                      side: merge.into.path === '' ? sourceLabel(merge.into.source_id) : merge.into.path,
                    })}
                  </Button>
                </div>
                <p className="text-muted-foreground">{t('compare.mergeHelp')}</p>
              </div>
            )}
            <OrganizeOutcome organize={organize} />

            {items.length === 0 ? (
              <p className="text-sm text-muted-foreground">{t('compare.empty')}</p>
            ) : (
              <ul aria-label={t('compare.files', { group: t(`compare.bucket.${first.bucket}`) })} className="grid gap-2">
                {items.map((item) => (
                  <ItemRow key={`${item.left?.id ?? ''} ${item.right?.id ?? ''}`} item={item} />
                ))}
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
          </>
        )}
      </div>
      <DetailPanel />
    </div>
  )
}

// Side names one side of the comparison: its path, its source, and its size.
function Side({ label, row }: { label: string; row: EntryRow }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const sourceLabel = useSourceLabel()
  return (
    <div className="grid content-start gap-1 rounded-lg border bg-card p-3 text-sm">
      <span className="text-xs font-medium text-muted-foreground uppercase">{label}</span>
      <span className="font-semibold break-all">{row.path === '' ? sourceLabel(row.source_id) : row.path}</span>
      <span className="text-muted-foreground">
        {sourceLabel(row.source_id)} · {fmt.bytes(row.total_bytes)} ·{' '}
        {t('units.files', { count: row.total_files, formatted: fmt.count(row.total_files) })}
      </span>
      <Link to={`/map/${row.id}`} className="text-primary hover:underline">
        {t('compare.showInMap')}
      </Link>
    </div>
  )
}

// ItemRow is one file of a group, with its copy on each side that holds it:
// each side's path inside its side when the two differ, and for an extra
// copy, the file on the other side holding the same content (r2b D11).
function ItemRow({ item }: { item: CompareItem }) {
  const { t } = useTranslation()
  const entryLink = useEntryLink()
  const shown = item.left ?? item.right
  const apart = item.left_path !== null && item.right_path !== null && item.left_path !== item.right_path
  return (
    <li className="grid gap-2 rounded-lg border bg-card p-3 text-sm">
      {shown === null ? (
        <span className="font-medium break-all">{item.path}</span>
      ) : (
        <Link to={{ search: entryLink(shown.id) }} className="font-medium break-all text-primary hover:underline">
          {item.path}
        </Link>
      )}
      {item.twin !== null && (
        <p className="break-all text-muted-foreground">
          <Trans
            i18nKey={item.left === null ? 'compare.extraCopyOfLeft' : 'compare.extraCopyOfRight'}
            values={{ path: item.twin.path }}
            components={{
              twinLink: <Link to={{ search: entryLink(item.twin.entry.id) }} className="text-primary hover:underline" />,
            }}
          />
        </p>
      )}
      <div className="grid gap-2 lg:grid-cols-2">
        {item.left !== null && (
          <ItemSide label={t('compare.left')} row={item.left} path={apart ? item.left_path : null} />
        )}
        {item.right !== null && (
          <ItemSide label={t('compare.right')} row={item.right} path={apart ? item.right_path : null} />
        )}
      </div>
    </li>
  )
}

function ItemSide({ label, row, path }: { label: string; row: EntryRow; path: string | null }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const entryLink = useEntryLink()
  return (
    <div className="grid content-start gap-1 rounded-md border p-2">
      <span className="flex flex-wrap gap-x-2">
        <Link to={{ search: entryLink(row.id) }} className="font-medium text-primary hover:underline">
          {label}
        </Link>
        <span className="text-muted-foreground">
          {fmt.bytes(row.size)} · {t('review.decision', { decision: t(`home.decision.${row.eff_decision}`) })}
        </span>
      </span>
      {path !== null && <span className="break-all">{path}</span>}
      {isMember(row) ? (
        <span className="text-muted-foreground">{t('review.memberDecision')}</span>
      ) : (
        <DecisionButtons entryId={row.id} name={`${label}: ${row.path}`} own={row.decision} />
      )}
    </div>
  )
}
