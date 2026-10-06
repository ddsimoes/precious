import { useQuery } from '@tanstack/react-query'
import { useId, type ReactNode } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { Link } from 'react-router'

import { fetchHome, homeQueryKey, type Decision, type Family, type Home } from '@/api/home'
import { useSources, type Source } from '@/api/sources'
import { ErrorBanner } from '@/app/ErrorBanner'
import { PageTitle } from '@/app/PageTitle'
import { ScanProgress } from '@/app/ScanProgress'
import { CoverageFigures } from '@/components/CoverageFigures'
import { SourceFilter } from '@/components/SourceFilter'
import { Card } from '@/components/ui/card'
import { BarList } from '@/home/BarList'
import { HashingProgress } from '@/home/HashingProgress'
import { useFormat } from '@/lib/format'
import { useSourceParam } from '@/lib/sourceParams'
import { CardList } from '@/opportunities/CardList'

const families: Family[] = ['personal', 'programs', 'disposable', 'containers']
const decisions: Decision[] = ['keep', 'discard', 'later', 'undecided']

// HomePage answers "how is my disk?" for every source together, or for the
// one chosen in the filter (kept in the address as ?source=).
export function HomePage() {
  const { t } = useTranslation()
  const source = useSourceParam()
  const sources = useSources()

  const home = useQuery({
    queryKey: homeQueryKey(source),
    queryFn: ({ signal }) => fetchHome(source, signal),
  })

  if (sources.data?.sources.length === 0) {
    return (
      <>
        <PageTitle>{t('pages.home')}</PageTitle>
        <p className="text-sm text-muted-foreground">
          <Trans
            i18nKey="home.noSources"
            components={{ sourcesLink: <Link to="/sources" className="font-medium text-primary underline" /> }}
          />
        </p>
      </>
    )
  }

  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <PageTitle>{t('pages.home')}</PageTitle>
        <SourceFilter />
      </div>

      {sources.isError && <ErrorBanner error={sources.error} onRetry={() => void sources.refetch()} />}
      {home.isPending && (
        <p role="status" className="text-sm text-muted-foreground">
          {t('app.loading')}
        </p>
      )}
      {home.isError && <ErrorBanner error={home.error} onRetry={() => void home.refetch()} />}
      {home.data !== undefined && (
        <HomeFigures home={home.data} source={source} sources={sources.data?.sources ?? []} />
      )}
    </div>
  )
}

function HomeFigures({ home, source, sources }: { home: Home; source: string | null; sources: Source[] }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const sourceLabel = (id: string) => sources.find((s) => s.id === id)?.label ?? id

  const byFamily = families.map((family) => {
    const amount = home.by_family.find((a) => a.family === family)
    return {
      key: family,
      label: t(`home.family.${family}`),
      bytes: amount?.bytes ?? 0,
      files: amount?.files ?? 0,
    }
  })
  const byKind = [...home.by_kind]
    .sort((a, b) => b.bytes - a.bytes)
    .map((a) => ({ key: a.kind, label: t(`home.kind.${a.kind}`), bytes: a.bytes, files: a.files }))
  const byYear = [...home.by_year]
    .sort((a, b) => a.year - b.year)
    .map((a) => ({ key: String(a.year), label: String(a.year), bytes: a.bytes, files: a.files }))
  const byDecision = decisions.map((decision) => ({
    key: decision,
    label: t(`home.decision.${decision}`),
    bytes: home.decisions[decision].bytes,
    files: home.decisions[decision].files,
  }))

  return (
    <>
      {home.partial && (
        <p role="status" className="rounded-md border border-amber-300 bg-amber-50 p-3 text-sm">
          {t('home.partial')}
        </p>
      )}

      <Section title={t('home.totals')}>
        <dl className="grid grid-cols-3 gap-4">
          <Figure label={t('home.size')} value={fmt.bytes(home.totals.bytes)} />
          <Figure label={t('home.files')} value={fmt.count(home.totals.files)} />
          <Figure label={t('home.folders')} value={fmt.count(home.totals.dirs)} />
        </dl>
      </Section>

      {home.scans.length > 0 && (
        <Section title={t('home.scans')}>
          <ul className="grid gap-3">
            {home.scans.map((scan) => (
              <li key={scan.job_id} className="grid gap-1">
                <span className="text-sm font-medium">{sourceLabel(scan.source_id)}</span>
                <ScanProgress label={sourceLabel(scan.source_id)} state={scan.state} progress={scan.progress} />
              </li>
            ))}
          </ul>
        </Section>
      )}

      {home.hashing.length > 0 && (
        <Section title={t('hashing.title')}>
          <ul className="grid gap-3">
            {home.hashing.map((job) => (
              <li key={job.job_id} className="grid gap-1">
                <span className="text-sm font-medium">{sourceLabel(job.source_id)}</span>
                <HashingProgress job={job} label={sourceLabel(job.source_id)} />
              </li>
            ))}
          </ul>
        </Section>
      )}

      <Section title={t('opportunities.title')}>
        <CardList cards={home.cards} source={source} />
        <Link
          to={{ pathname: '/opportunities', search: source === null ? '' : `?${new URLSearchParams({ source })}` }}
          className="text-sm font-medium text-primary hover:underline"
        >
          {t('opportunities.all')}
        </Link>
      </Section>

      <div className="grid gap-4 lg:grid-cols-2">
        <Section title={t('coverage.title')}>
          <CoverageFigures coverage={home.coverage} />
        </Section>
        <Section title={t('home.decisions')}>
          <BarList label={t('home.decisions')} bars={byDecision} whole={home.totals.bytes} />
        </Section>
        <Section title={t('home.byFamily')}>
          <BarList label={t('home.byFamily')} bars={byFamily} />
        </Section>
        <Section title={t('home.byKind')}>
          <BarList label={t('home.byKind')} bars={byKind} />
        </Section>
        <Section title={t('home.byYear')}>
          <BarList label={t('home.byYear')} bars={byYear} />
        </Section>
      </div>
    </>
  )
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  const headingId = useId()
  return (
    <Card className="p-4">
      <section aria-labelledby={headingId} className="grid gap-3">
        <h2 id={headingId} className="text-base font-semibold">
          {title}
        </h2>
        {children}
      </section>
    </Card>
  )
}

function Figure({ label, value }: { label: string; value: string }) {
  return (
    <div className="grid gap-1">
      <dt className="text-sm text-muted-foreground">{label}</dt>
      <dd className="text-2xl font-semibold">{value}</dd>
    </div>
  )
}
