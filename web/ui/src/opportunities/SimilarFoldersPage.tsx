import { useInfiniteQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link, useSearchParams } from 'react-router'

import type { EntryRow } from '@/api/entries'
import { fetchSimilar, similarQueryKey, type Overlap } from '@/api/opportunities'
import { ErrorBanner } from '@/app/ErrorBanner'
import { PageTitle } from '@/app/PageTitle'
import { SourceFilter } from '@/components/SourceFilter'
import { Button } from '@/components/ui/button'
import { DetailPanel } from '@/detail/DetailPanel'
import { useEntryLink } from '@/detail/useEntryLink'
import { useFormat } from '@/lib/format'
import { useSourceLabel, useSourceParam } from '@/lib/sourceParams'
import { cn } from '@/lib/utils'

// SimilarFoldersPage lists the folders and archives related as overlap
// (spec "Similar folders are listed", r2b design D12) for every source or
// the one chosen (?source=), largest bytes in common first: both sides,
// what they share, and a Compare of the two, which counts what is only on
// each (ADR 0010). It is read-only: a similar folder is not a copy (§6.4),
// so it carries no decision controls and no bytes to free.
export function SimilarFoldersPage() {
  const { t } = useTranslation()
  const [params] = useSearchParams()
  const source = useSourceParam()
  const pages = useInfiniteQuery({
    queryKey: similarQueryKey(source),
    queryFn: ({ pageParam, signal }) => fetchSimilar(source, pageParam, signal),
    initialPageParam: null as string | null,
    getNextPageParam: (page) => page.next_cursor,
  })
  const items = pages.data?.pages.flatMap((page) => page.items) ?? []

  return (
    <div className={cn('grid items-start gap-4', params.has('entry') && '3xl:grid-cols-[minmax(0,1fr)_26rem]')}>
      <div className="grid min-w-0 gap-4">
        <Link
          to={{ pathname: '/opportunities', search: source === null ? '' : `?${new URLSearchParams({ source })}` }}
          className="text-sm font-medium text-primary hover:underline"
        >
          {t('review.back')}
        </Link>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <PageTitle>{t('similar.title')}</PageTitle>
          <SourceFilter />
        </div>
        <p className="text-sm text-muted-foreground">{t('similar.help')}</p>
        {pages.isPending && (
          <p role="status" className="text-sm text-muted-foreground">
            {t('app.loading')}
          </p>
        )}
        {pages.isError && <ErrorBanner error={pages.error} onRetry={() => void pages.refetch()} />}
        {pages.data !== undefined &&
          (items.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t('similar.none')}</p>
          ) : (
            <ul aria-label={t('similar.list')} className="grid gap-2">
              {items.map((overlap) => (
                <OverlapRow key={overlap.id} overlap={overlap} />
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
      <DetailPanel />
    </div>
  )
}

// OverlapRow is one pair: both sides, the bytes in common, and a Compare of
// the two, which alone counts what is only on each side (ADR 0010).
function OverlapRow({ overlap }: { overlap: Overlap }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const sourceLabel = useSourceLabel()
  const entryLink = useEntryLink()
  const name = (row: EntryRow) => (row.path === '' ? sourceLabel(row.source_id) : row.path)
  return (
    <li className="grid gap-2 rounded-lg border bg-card p-3 text-sm">
      <div className="grid gap-1">
        {[overlap.a, overlap.other].map((row) => (
          <Link
            key={row.id}
            to={{ search: entryLink(row.id) }}
            className="font-medium break-all text-primary hover:underline"
          >
            {name(row)}
          </Link>
        ))}
      </div>
      <p className="text-muted-foreground">{t('similar.inCommon', { bytes: fmt.bytes(overlap.matched_bytes) })}</p>
      <div>
        <Button asChild size="sm" variant="outline">
          <Link
            to={{ pathname: '/compare', search: `?${new URLSearchParams({ left: overlap.a.id, right: overlap.other.id })}` }}
          >
            {t('similar.compare')}
          </Link>
        </Button>
      </div>
    </li>
  )
}
