import { useInfiniteQuery, useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useSearchParams } from 'react-router'

import { buckets, compareQueryKey, fetchCompare, isBucket, type Bucket, type CompareItem } from '@/api/compare'
import { checkNow } from '@/api/content'
import { isMember, type EntryRow } from '@/api/entries'
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

// ComparePage answers "which copy do I keep?" (spec §11.6, R2 design D11):
// two folders or archives side by side, named in the address
// (/compare?left=&right=&bucket=) so a comparison can be bookmarked, their
// files in five groups, each decided with the usual controls.
export function ComparePage() {
  const { t } = useTranslation()
  const [params] = useSearchParams()
  const left = params.get('left') ?? ''
  const right = params.get('right') ?? ''
  const bucketParam = params.get('bucket')
  const bucket: Bucket = isBucket(bucketParam) ? bucketParam : 'only_left'

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

function Comparison({ left, right, bucket }: { left: string; right: string; bucket: Bucket }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const csrfToken = useCsrfToken()
  const [params] = useSearchParams()
  const [checkStarted, setCheckStarted] = useState(false)

  const pages = useInfiniteQuery({
    queryKey: compareQueryKey(left, right, bucket),
    queryFn: ({ pageParam, signal }) => fetchCompare(left, right, bucket, pageParam, signal),
    initialPageParam: null as string | null,
    getNextPageParam: (page) => page.next_cursor,
  })
  const check = useMutation({
    mutationFn: () => checkNow([left, right], csrfToken),
    onSuccess: () => setCheckStarted(true),
  })
  const first = pages.data?.pages[0]
  const items = pages.data?.pages.flatMap((page) => page.items) ?? []

  const bucketSearch = (b: Bucket) => {
    const next = new URLSearchParams(params)
    next.set('bucket', b)
    next.delete('entry')
    return `?${next}`
  }

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
        {pages.isPending && (
          <p role="status" className="text-sm text-muted-foreground">
            {t('app.loading')}
          </p>
        )}
        {first !== undefined && (
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
                        aria-current={b === bucket ? 'page' : undefined}
                        className={cn(
                          'grid h-full gap-0.5 rounded-lg border bg-card p-2 text-sm hover:bg-accent',
                          b === bucket && 'border-primary bg-primary/5',
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

            {items.length === 0 ? (
              <p className="text-sm text-muted-foreground">{t('compare.empty')}</p>
            ) : (
              <ul aria-label={t('compare.files', { group: t(`compare.bucket.${bucket}`) })} className="grid gap-2">
                {items.map((item) => (
                  <ItemRow key={item.path_b64} item={item} />
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

// ItemRow is one file of a group, with its copy on each side that holds it.
function ItemRow({ item }: { item: CompareItem }) {
  const { t } = useTranslation()
  const entryLink = useEntryLink()
  const shown = item.left ?? item.right
  return (
    <li className="grid gap-2 rounded-lg border bg-card p-3 text-sm">
      {shown === null ? (
        <span className="font-medium break-all">{item.path}</span>
      ) : (
        <Link to={{ search: entryLink(shown.id) }} className="font-medium break-all text-primary hover:underline">
          {item.path}
        </Link>
      )}
      <div className="grid gap-2 lg:grid-cols-2">
        {item.left !== null && <ItemSide label={t('compare.left')} row={item.left} />}
        {item.right !== null && <ItemSide label={t('compare.right')} row={item.right} />}
      </div>
    </li>
  )
}

function ItemSide({ label, row }: { label: string; row: EntryRow }) {
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
      {isMember(row) ? (
        <span className="text-muted-foreground">{t('review.memberDecision')}</span>
      ) : (
        <DecisionButtons entryId={row.id} name={`${label}: ${row.path}`} own={row.decision} />
      )}
    </div>
  )
}
