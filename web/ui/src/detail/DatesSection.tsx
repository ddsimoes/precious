import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { clearDateCorrection, entryDatesQueryKey, fetchEntryDates, refreshAfterCorrection } from '@/api/dates'
import type { EntryRow } from '@/api/entries'
import { ErrorBanner } from '@/app/ErrorBanner'
import { useCsrfToken } from '@/app/session'
import { Button } from '@/components/ui/button'
import { CorrectionDialog } from '@/dates/CorrectionDialog'
import { useDatesText } from '@/dates/text'
import { Fact, Section } from '@/detail/parts'
import { useFormat } from '@/lib/format'

// DatesSection is the detail panel's Dates section for a photo or video
// (R5 design D10, GET /api/entries/{id}/dates): its date with where it
// comes from, how precise and how sure it is, its flags, or its metadata
// state when the header was not read, every date found, its camera, the
// owner's correction, and the controls to correct it. It is absent when
// the server has no dates for the entry.
export function DatesSection({ entry }: { entry: EntryRow }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const text = useDatesText()
  const queryClient = useQueryClient()
  const csrfToken = useCsrfToken()
  const [correcting, setCorrecting] = useState(false)
  const dates = useQuery({
    queryKey: entryDatesQueryKey(entry.id),
    queryFn: ({ signal }) => fetchEntryDates(entry.id, signal),
  })
  const clear = useMutation({
    mutationFn: () => clearDateCorrection({ entry_id: entry.id }, csrfToken),
    onSuccess: () => refreshAfterCorrection(queryClient),
  })

  if (dates.data?.dates === null) {
    return null
  }
  const d = dates.data?.dates
  return (
    <Section title={t('dates.panel.title')}>
      {dates.isPending && (
        <p role="status" className="text-sm text-muted-foreground">
          {t('app.loading')}
        </p>
      )}
      {dates.isError && <ErrorBanner error={dates.error} onRetry={() => void dates.refetch()} />}
      {d !== undefined && (
        <>
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
            <Fact label={t('dates.panel.date')}>
              <span className="font-medium">{text.date(d.date)}</span>
            </Fact>
            <Fact label={t('dates.panel.from')}>
              {[
                t(`dates.source.${d.date.source}`),
                ...(d.date.refined ? [t('dates.refined')] : []),
                ...(d.date.corrected === null ? [] : [t(`dates.corrected.${d.date.corrected}`)]),
              ].join(', ')}
            </Fact>
            {d.date.precision !== null && (
              <Fact label={t('dates.panel.precision')}>{t(`dates.precision.${d.date.precision}`)}</Fact>
            )}
            <Fact label={t('dates.panel.confidence')}>{t(`dates.confidence.${d.date.confidence}`)}</Fact>
            {d.metadata !== 'read' && (
              <Fact label={t('dates.panel.metadata')}>{t(`dates.metadata.${d.metadata}`)}</Fact>
            )}
            {d.camera !== null && <Fact label={t('dates.panel.camera')}>{text.camera(d.camera)}</Fact>}
            {d.correction !== null && (
              <Fact label={t('dates.panel.correction')}>
                {t('dates.panel.correctedAt', {
                  correction: text.correction(d.correction),
                  time: fmt.dateTime(d.correction.created_at),
                })}
              </Fact>
            )}
          </dl>
          {d.flags.length > 0 && (
            <ul aria-label={t('dates.panel.flags')} className="grid gap-1 text-sm">
              {d.flags.map((flag) => (
                <li key={flag} className="rounded-md border border-amber-300 bg-amber-50 p-2">
                  <span className="font-medium">{t(`dates.flag.${flag}`)}</span>
                  <span className="block text-muted-foreground">{t(`dates.flagHelp.${flag}`)}</span>
                </li>
              ))}
            </ul>
          )}
          {d.candidates.length > 0 && (
            <div className="grid gap-1 text-sm">
              <h4 className="font-medium">{t('dates.panel.found')}</h4>
              <ul aria-label={t('dates.panel.found')} className="grid gap-0.5">
                {d.candidates.map((candidate) => (
                  <li key={`${candidate.source}-${candidate.local}`}>
                    {t(`dates.source.${candidate.source}`)}: {fmt.local(candidate.local, candidate.precision, candidate.offset_min)}
                    {!candidate.plausible && (
                      <span className="text-muted-foreground"> ({t('dates.panel.notPlausible')})</span>
                    )}
                  </li>
                ))}
              </ul>
            </div>
          )}
          <div className="flex flex-wrap gap-2">
            <Button size="sm" variant="outline" onClick={() => setCorrecting(true)}>
              {t('dates.panel.correct')}
            </Button>
            {d.correction !== null && (
              <Button size="sm" variant="outline" disabled={clear.isPending} onClick={() => clear.mutate()}>
                {clear.isPending ? t('dates.panel.clearing') : t('dates.panel.clear')}
              </Button>
            )}
          </div>
          {clear.isError && <ErrorBanner error={clear.error} onDismiss={() => clear.reset()} />}
          {correcting && (
            <CorrectionDialog
              title={t('dates.correct.titleOne', { name: entry.name })}
              scope={{ kind: 'single', entryId: entry.id, corrected: d.correction !== null }}
              onClose={() => setCorrecting(false)}
            />
          )}
        </>
      )}
    </Section>
  )
}
