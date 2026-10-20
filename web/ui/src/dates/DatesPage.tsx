import { skipToken, useQuery } from '@tanstack/react-query'
import { useId } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { Link, useSearchParams } from 'react-router'

import {
  confidences,
  dateFlags,
  dateSources,
  datesSummaryQueryKey,
  fetchDatesSummary,
  mediaProgressKey,
  metaStates,
  type MediaJob,
} from '@/api/dates'
import { useSources, type Source } from '@/api/sources'
import { ErrorBanner } from '@/app/ErrorBanner'
import { PageTitle } from '@/app/PageTitle'
import { SourceFilter } from '@/components/SourceFilter'
import { Card } from '@/components/ui/card'
import { CamerasSection } from '@/dates/CamerasSection'
import { DatesList } from '@/dates/DatesList'
import { DetailPanel } from '@/detail/DetailPanel'
import { useFormat } from '@/lib/format'
import { useSourceParam } from '@/lib/sourceParams'
import { cn } from '@/lib/utils'

// mediaPhases names the `phase` values of a media job's progress (R5
// design D4).
const mediaPhases: Record<number, 'list' | 'read' | 'derive' | 'cameras'> = {
  1: 'list',
  2: 'read',
  3: 'derive',
  4: 'cameras',
}

// DatesPage is the Dates screen (spec §11, R5 design D21): for the source
// chosen at the top, the totals of its media dates with the time zone they
// are read in, its media job's progress, its cameras with their suggested
// shifts, and the list of its photos and videos with their dates, filters,
// corrections, and the two date plans. With every source chosen and more
// than one source, it shows their totals and asks for one. The detail
// panel of ?entry= opens beside it.
export function DatesPage() {
  const { t } = useTranslation()
  const [params] = useSearchParams()
  const chosen = useSourceParam()
  const sources = useSources()
  const all = sources.data?.sources ?? []
  const sourceId = chosen ?? (all.length === 1 ? (all[0]?.id ?? null) : null)
  const source = all.find((s) => s.id === sourceId)

  return (
    <div className={cn('grid items-start gap-4', params.has('entry') && '3xl:grid-cols-[minmax(0,1fr)_26rem]')}>
      <div className="grid max-w-5xl min-w-0 gap-4">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <PageTitle>{t('pages.dates')}</PageTitle>
          <SourceFilter />
        </div>
        <p className="text-sm text-muted-foreground">{t('dates.help')}</p>
        {sources.isPending && (
          <p role="status" className="text-sm text-muted-foreground">
            {t('app.loading')}
          </p>
        )}
        {sources.isError && <ErrorBanner error={sources.error} onRetry={() => void sources.refetch()} />}
        {sources.data !== undefined && all.length === 0 && (
          <p className="text-sm text-muted-foreground">
            <Trans
              i18nKey="dates.noSources"
              components={{ sourcesLink: <Link to="/sources" className="text-primary underline" /> }}
            />
          </p>
        )}
        {all.length > 0 && <SummaryCard source={sourceId} />}
        {source !== undefined && <MediaProgress source={source} />}
        {sources.data !== undefined && all.length > 0 && sourceId === null && (
          <p className="text-sm">{t('dates.chooseSource')}</p>
        )}
        {source !== undefined && (
          <>
            <CamerasSection key={`cameras-${source.id}`} source={source} />
            <DatesList key={`list-${source.id}`} source={source} />
          </>
        )}
      </div>
      <DetailPanel />
    </div>
  )
}

// SummaryCard shows the totals of one source's media dates, or of every
// source: the files by metadata state, by where their date comes from, by
// how sure it is, and by flag, the cameras to look at, and the time zone
// the dates are read in, with a notice when the server sets none.
function SummaryCard({ source }: { source: string | null }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const headingId = useId()
  const summary = useQuery({
    queryKey: datesSummaryQueryKey(source),
    queryFn: ({ signal }) => fetchDatesSummary(source, signal),
  })
  const s = summary.data

  return (
    <Card className="p-4">
      <section aria-labelledby={headingId} className="grid gap-3 text-sm">
        <h2 id={headingId} className="text-lg font-semibold">
          {t('dates.summary.title')}
        </h2>
        {summary.isPending && <p className="text-muted-foreground">{t('app.loading')}</p>}
        {summary.isError && <ErrorBanner error={summary.error} onRetry={() => void summary.refetch()} />}
        {s !== undefined && (
          <>
            <p className="text-base font-medium">{t('dates.summary.media', { count: s.media, formatted: fmt.count(s.media) })}</p>
            {s.time_zone_set ? (
              <p>{t('dates.summary.zone', { zone: s.time_zone })}</p>
            ) : (
              <p role="status" className="rounded-md border border-amber-300 bg-amber-50 p-2">
                {t('dates.summary.zoneUnset', { zone: s.time_zone })}
              </p>
            )}
            <p className="text-muted-foreground">
              {s.summary_at === null
                ? t('dates.summary.notYet')
                : t('dates.summary.updated', { time: fmt.dateTime(s.summary_at) })}
            </p>
            <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
              <Totals
                title={t('dates.summary.metadata')}
                rows={metaStates.map((state) => [t(`dates.metadata.${state}`), s.metadata[state]])}
              />
              <Totals
                title={t('dates.summary.bySource')}
                rows={dateSources.map((source) => [t(`dates.source.${source}`), s.by_source[source]])}
              />
              <Totals
                title={t('dates.summary.byConfidence')}
                rows={confidences.map((c) => [t(`dates.confidence.${c}`), s.by_confidence[c]])}
              />
              <Totals
                title={t('dates.summary.flags')}
                rows={dateFlags.map((flag) => [t(`dates.flag.${flag}`), s.flags[flag]])}
              />
              <Totals
                title={t('dates.summary.cameras')}
                rows={[
                  [t('dates.summary.camerasOffset'), s.cameras.offset],
                  [t('dates.summary.camerasDisagrees'), s.cameras.disagrees],
                ]}
              />
            </div>
          </>
        )}
      </section>
    </Card>
  )
}

// Totals is one titled list of counts.
function Totals({ title, rows }: { title: string; rows: Array<[string, number]> }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  return (
    <div className="grid content-start gap-1">
      <h3 className="font-semibold">{title}</h3>
      <ul aria-label={title} className="grid gap-0.5">
        {rows.map(([label, count]) => (
          <li key={label} className={count === 0 ? 'text-muted-foreground' : undefined}>
            {t('dates.summary.value', { label, formatted: fmt.count(count) })}
          </li>
        ))}
      </ul>
    </div>
  )
}

// MediaProgress shows the source's media job while it runs, from its
// events: what it is doing, the files read, and the dates worked out.
function MediaProgress({ source }: { source: Source }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const job = useQuery<MediaJob | null>({ queryKey: mediaProgressKey(source.id), queryFn: skipToken })
  const current = job.data
  if (current === undefined || current === null) {
    return null
  }
  const p = current.progress
  const phase = mediaPhases[p.phase ?? 0]
  let stateText: string | null = null
  if (current.state !== 'running') {
    stateText = t(`scan.state.${current.state}`)
  } else if (phase !== undefined) {
    stateText = t(`dates.progress.phase.${phase}`)
  }
  const done = phase === 'derive' || phase === 'cameras' ? (p.media ?? 0) : (p.files ?? 0)
  const total = phase === 'derive' || phase === 'cameras' ? (p.of_media ?? 0) : (p.of_files ?? 0)

  return (
    <div role="status" aria-label={t('dates.progress.label')} className="grid gap-1 rounded-md border bg-card p-3 text-sm">
      <p className="font-medium">
        {t('dates.progress.label')}
        {stateText !== null && ` · ${stateText}`}
      </p>
      {p.of_files !== undefined && (
        <p>
          {t('dates.progress.files', {
            done: fmt.count(p.files ?? 0),
            total: fmt.count(p.of_files),
            bytes: fmt.bytes(p.bytes ?? 0),
          })}
        </p>
      )}
      {p.of_media !== undefined && (
        <p>{t('dates.progress.media', { done: fmt.count(p.media ?? 0), total: fmt.count(p.of_media) })}</p>
      )}
      {(p.unreadable ?? 0) > 0 && (
        <p>{t('dates.progress.unreadable', { formatted: fmt.count(p.unreadable ?? 0) })}</p>
      )}
      {(p.changed ?? 0) > 0 && <p>{t('dates.progress.changed', { formatted: fmt.count(p.changed ?? 0) })}</p>}
      <progress className="w-full" max={total} value={Math.min(done, total)} />
    </div>
  )
}
