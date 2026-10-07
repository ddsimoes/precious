import { useInfiniteQuery } from '@tanstack/react-query'
import { useId } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { Link, useSearchParams } from 'react-router'

import { isMember, lastChange } from '@/api/entries'
import { fetchGems, gemLists, gemSections, gemsQueryKey, type Gem, type GemSection } from '@/api/gems'
import { ErrorBanner } from '@/app/ErrorBanner'
import { PageTitle } from '@/app/PageTitle'
import { CoverageClaim } from '@/components/CoverageFigures'
import { DecisionButtons } from '@/components/DecisionButtons'
import { SourceFilter } from '@/components/SourceFilter'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { DetailPanel } from '@/detail/DetailPanel'
import { useEntryLink } from '@/detail/useEntryLink'
import { useFormat } from '@/lib/format'
import { useSourceLabel, useSourceParam } from '@/lib/sourceParams'
import { cn } from '@/lib/utils'
import { ListSelectAll } from '@/opportunities/ListSelectAll'

// GemsPage answers "what is valuable?" (spec §11.7, R2 design D14) for
// every source or the one chosen (?source=): three sections, each with the
// share checked for copies, decision controls on every file, and select
// all. A file not checked yet is counted, never listed as having no other
// copy (I7).
export function GemsPage() {
  const { t } = useTranslation()
  const [params] = useSearchParams()
  const source = useSourceParam()
  return (
    <div className={cn('grid items-start gap-4', params.has('entry') && '3xl:grid-cols-[minmax(0,1fr)_26rem]')}>
      <div className="grid min-w-0 gap-4">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <PageTitle>{t('pages.gems')}</PageTitle>
          <SourceFilter />
        </div>
        {gemSections.map((section) => (
          <GemsSection key={`${section} ${source ?? ''}`} section={section} source={source} />
        ))}
      </div>
      <DetailPanel />
    </div>
  )
}

function GemsSection({ section, source }: { section: GemSection; source: string | null }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const headingId = useId()
  const pages = useInfiniteQuery({
    queryKey: gemsQueryKey(section, source),
    queryFn: ({ pageParam, signal }) => fetchGems(section, source, pageParam, signal),
    initialPageParam: null as string | null,
    getNextPageParam: (page) => page.next_cursor,
  })
  const first = pages.data?.pages[0]
  const items = pages.data?.pages.flatMap((page) => page.items) ?? []
  const unchecked = first?.coverage.unchecked.files ?? 0
  const title = t(`gems.section.${section}`)

  return (
    <Card className="p-4">
      <section aria-labelledby={headingId} className="grid gap-3">
        <h2 id={headingId} className="text-base font-semibold">
          {title}
        </h2>
        <p className="text-sm text-muted-foreground">{t(`gems.help.${section}`)}</p>
        {first !== undefined && (
          <div className="grid gap-0.5">
            <CoverageClaim coverage={first.coverage} />
            {unchecked > 0 && (
              <p className="text-xs text-muted-foreground">
                {t('gems.unchecked', { count: unchecked, formatted: fmt.count(unchecked) })}
              </p>
            )}
          </div>
        )}
        {pages.isPending && (
          <p role="status" className="text-sm text-muted-foreground">
            {t('app.loading')}
          </p>
        )}
        {pages.isError && <ErrorBanner error={pages.error} onRetry={() => void pages.refetch()} />}
        {first !== undefined &&
          (items.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t('gems.empty')}</p>
          ) : (
            <>
              <ListSelectAll list={gemLists[section]} source={source} />
              <ul aria-label={title} className="grid gap-2">
                {items.map((gem) => (
                  <GemRow key={gem.entry.id} gem={gem} />
                ))}
              </ul>
            </>
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
      </section>
    </Card>
  )
}

// GemRow is one file: its path, size, and date, the group or the folder
// pair it was found in, and its decision controls.
function GemRow({ gem }: { gem: Gem }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const entryLink = useEntryLink()
  const sourceLabel = useSourceLabel()
  const { entry, group, relation } = gem
  const time = lastChange(entry)
  return (
    <li className="grid gap-1 rounded-lg border p-3 text-sm">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3">
        <Link to={{ search: entryLink(entry.id) }} className="min-w-0 font-medium break-all text-primary hover:underline">
          {entry.path}
        </Link>
        <span className="font-semibold">{fmt.bytes(entry.size)}</span>
      </div>
      <p className="text-xs text-muted-foreground">
        {sourceLabel(entry.source_id)} · {time === null ? t('entry.unknownDate') : fmt.date(time)}
      </p>
      {group !== null && (
        <p className="text-muted-foreground">
          <Trans
            i18nKey="gems.inside"
            values={{ path: group.path }}
            components={{ groupLink: <Link to={{ search: entryLink(group.id) }} className="text-primary underline" /> }}
          />
        </p>
      )}
      {relation !== null && (
        <p className="text-muted-foreground">
          <Trans
            i18nKey="gems.notIn"
            values={{ path: relation.other.path }}
            components={{
              otherLink: <Link to={{ search: entryLink(relation.other.id) }} className="text-primary underline" />,
            }}
          />
        </p>
      )}
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span>{t('review.decision', { decision: t(`home.decision.${entry.eff_decision}`) })}</span>
        {isMember(entry) ? (
          <span className="text-muted-foreground">{t('review.memberDecision')}</span>
        ) : (
          <DecisionButtons entryId={entry.id} name={entry.name} own={entry.decision} />
        )}
      </div>
    </li>
  )
}
