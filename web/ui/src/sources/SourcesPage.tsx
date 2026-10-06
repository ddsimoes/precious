import { useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { homeQueryRoot } from '@/api/home'
import { sourcesQueryKey, useSources, type Source, type SourcesResponse } from '@/api/sources'
import { ErrorBanner } from '@/app/ErrorBanner'
import { PageTitle } from '@/app/PageTitle'
import { Button } from '@/components/ui/button'
import { PickerDialog } from '@/sources/PickerDialog'
import { SourceCard } from '@/sources/SourceCard'

export function SourcesPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const sources = useSources()
  const [picking, setPicking] = useState(false)
  const [added, setAdded] = useState<Source | null>(null)

  const onAdded = (source: Source) => {
    queryClient.setQueryData<SourcesResponse>(sourcesQueryKey, (data) =>
      data && { sources: [...data.sources.filter((s) => s.id !== source.id), source] },
    )
    void queryClient.invalidateQueries({ queryKey: homeQueryRoot })
    setPicking(false)
    setAdded(source)
  }

  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <PageTitle>{t('pages.sources')}</PageTitle>
        <Button onClick={() => setPicking(true)}>{t('sources.add')}</Button>
      </div>

      {added !== null && (
        <p role="status" className="rounded-md bg-muted p-3 text-sm">
          {t('sources.added', { label: added.label })}
        </p>
      )}

      {sources.isPending && (
        <p role="status" className="text-sm text-muted-foreground">
          {t('app.loading')}
        </p>
      )}
      {sources.isError && (
        <ErrorBanner error={sources.error} onRetry={() => void sources.refetch()} />
      )}
      {sources.data?.sources.length === 0 && (
        <p className="text-sm text-muted-foreground">{t('sources.empty')}</p>
      )}
      {sources.data !== undefined && sources.data.sources.length > 0 && (
        <ul aria-label={t('sources.listLabel')} className="grid gap-4">
          {sources.data.sources.map((source) => (
            <li key={source.id}>
              <SourceCard source={source} />
            </li>
          ))}
        </ul>
      )}

      {picking && <PickerDialog onAdded={onAdded} onClose={() => setPicking(false)} />}
    </div>
  )
}
