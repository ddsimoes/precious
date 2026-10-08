import { useId } from 'react'
import { useTranslation } from 'react-i18next'

import { useSources, type Source } from '@/api/sources'
import { ErrorBanner } from '@/app/ErrorBanner'
import { PageTitle } from '@/app/PageTitle'
import { DraftCleanup } from '@/cleanup/DraftCleanup'
import { QuarantineList } from '@/cleanup/QuarantineList'
import { SourceFilter } from '@/components/SourceFilter'
import { Card } from '@/components/ui/card'
import { useSourceParam } from '@/lib/sourceParams'

// CleanupPage is the Cleanup screen (spec §11.9, R4 design D3, D6, D7): for
// each source, or the one chosen at the top, drafting a cleanup plan of its
// discarded items, and its quarantine with restore and the check before
// deleting for good.
export function CleanupPage() {
  const { t } = useTranslation()
  const source = useSourceParam()
  const sources = useSources()
  const shown = (sources.data?.sources ?? []).filter((s) => source === null || s.id === source)

  return (
    <div className="grid max-w-4xl gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <PageTitle>{t('pages.cleanup')}</PageTitle>
        <SourceFilter />
      </div>
      <p className="text-sm text-muted-foreground">{t('cleanup.help')}</p>
      {sources.isPending && (
        <p role="status" className="text-sm text-muted-foreground">
          {t('app.loading')}
        </p>
      )}
      {sources.isError && <ErrorBanner error={sources.error} onRetry={() => void sources.refetch()} />}
      {sources.data !== undefined && shown.length === 0 && (
        <p className="text-sm text-muted-foreground">{t('cleanup.noSources')}</p>
      )}
      {shown.map((s) => (
        <SourceCleanup key={s.id} source={s} />
      ))}
    </div>
  )
}

// SourceCleanup is one source's part of the Cleanup screen: its plans and
// its quarantine.
function SourceCleanup({ source }: { source: Source }) {
  const { t } = useTranslation()
  const headingId = useId()
  return (
    <Card className="p-4">
      <section aria-labelledby={headingId} className="grid gap-4">
        <h2 id={headingId} className="text-lg font-semibold">
          {source.label}
        </h2>
        <div className="grid gap-2">
          <h3 className="text-base font-semibold">{t('cleanup.plans')}</h3>
          <p className="text-sm text-muted-foreground">{t('cleanup.plansHelp')}</p>
          <DraftCleanup source={source} list={null} />
        </div>
        <div className="grid gap-2">
          <h3 className="text-base font-semibold">{t('cleanup.quarantine.title')}</h3>
          <p className="text-sm text-muted-foreground">{t('cleanup.quarantine.help')}</p>
          <QuarantineList source={source} />
        </div>
      </section>
    </Card>
  )
}
