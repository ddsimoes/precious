import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { fetchOpportunities, opportunitiesQueryKey } from '@/api/opportunities'
import { ErrorBanner } from '@/app/ErrorBanner'
import { PageTitle } from '@/app/PageTitle'
import { CoverageFigures } from '@/components/CoverageFigures'
import { SourceFilter } from '@/components/SourceFilter'
import { Card } from '@/components/ui/card'
import { useFormat } from '@/lib/format'
import { useSourceParam } from '@/lib/sourceParams'
import { CardList } from '@/opportunities/CardList'

// OpportunitiesPage answers "what should I do first?" (spec §11.4): the
// opportunity cards of every source or of the one chosen (?source=), largest
// first, each opening its review list, and how much was checked for copies.
export function OpportunitiesPage() {
  const { t } = useTranslation()
  const fmt = useFormat()
  const source = useSourceParam()
  const opportunities = useQuery({
    queryKey: opportunitiesQueryKey(source),
    queryFn: ({ signal }) => fetchOpportunities(source, signal),
  })
  const data = opportunities.data

  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <PageTitle>{t('pages.opportunities')}</PageTitle>
        <SourceFilter />
      </div>
      {opportunities.isPending && (
        <p role="status" className="text-sm text-muted-foreground">
          {t('app.loading')}
        </p>
      )}
      {opportunities.isError && (
        <ErrorBanner error={opportunities.error} onRetry={() => void opportunities.refetch()} />
      )}
      {data !== undefined && (
        <>
          <CardList cards={data.cards} source={source} />
          <p className="text-sm text-muted-foreground">
            {data.computed_at === null
              ? t('opportunities.notComputed')
              : t('opportunities.computedAt', { time: fmt.dateTime(data.computed_at) })}
          </p>
          <Card className="p-4">
            <section aria-label={t('coverage.title')} className="grid gap-3">
              <h2 className="text-base font-semibold">{t('coverage.title')}</h2>
              <CoverageFigures coverage={data.coverage} />
            </section>
          </Card>
        </>
      )}
    </div>
  )
}
