import { useQuery } from '@tanstack/react-query'
import { Trans, useTranslation } from 'react-i18next'
import { useSearchParams } from 'react-router'

import { entryQueryKey, fetchEntry } from '@/api/entries'
import { searchFilters } from '@/api/search'
import { useSources } from '@/api/sources'
import { useTags } from '@/api/tags'
import { PageTitle } from '@/app/PageTitle'
import { SourceFilter } from '@/components/SourceFilter'
import { Button } from '@/components/ui/button'
import { DetailPanel } from '@/detail/DetailPanel'
import { useSourceParam } from '@/lib/sourceParams'
import { cn } from '@/lib/utils'
import { ManageTags } from '@/search/ManageTags'
import { SearchFilters } from '@/search/SearchFilters'
import { SearchResults } from '@/search/SearchResults'

// SearchPage answers "where is it?" (spec §11.3): the filters of design D11,
// kept in the address with the API's parameter names, the results with
// bulk decisions and tags, and the detail panel of ?entry=. Without a
// source in the address it searches the remembered one (r2b design D9).
export function SearchPage() {
  const { t } = useTranslation()
  const [params, setParams] = useSearchParams()
  const source = useSourceParam()
  const sourced = new URLSearchParams(params)
  if (source === null) {
    sourced.delete('source')
  } else {
    sourced.set('source', source)
  }
  const filters = searchFilters(sourced)
  const filtersKey = filters.toString()
  const within = params.get('within')
  const tags = useTags()

  const removeWithin = () => {
    const next = new URLSearchParams(params)
    next.delete('within')
    next.delete('entry')
    // "A copy outside this folder" needs the folder.
    next.delete('dup', 'elsewhere')
    setParams(next)
  }

  return (
    // The panel takes a column only on wide windows (3xl); on narrower ones
    // it opens over the results (DetailPanel).
    <div className={cn('grid items-start gap-4', params.has('entry') && '3xl:grid-cols-[minmax(0,1fr)_26rem]')}>
      <div className="grid min-w-0 gap-4">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <PageTitle>{t('pages.search')}</PageTitle>
          <SourceFilter />
        </div>
        {within !== null && <WithinFolder id={within} onRemove={removeWithin} />}
        {/* Keyed by the search: a new search resets the form and the selection. */}
        <SearchFilters key={`filters?${filtersKey}`} params={params} onSearch={(next) => setParams(next)} />
        {tags.data !== undefined && tags.data.tags.length > 0 && (
          <div>
            <ManageTags tags={tags.data.tags} />
          </div>
        )}
        <SearchResults key={`results?${filtersKey}`} filters={filters} />
      </div>
      <DetailPanel />
    </div>
  )
}

// WithinFolder names the folder the search is limited to.
function WithinFolder({ id, onRemove }: { id: string; onRemove: () => void }) {
  const { t } = useTranslation()
  const sources = useSources()
  const folder = useQuery({
    queryKey: entryQueryKey(id),
    queryFn: ({ signal }) => fetchEntry(id, signal),
  })
  const entry = folder.data?.entry
  const rootLabel = sources.data?.sources.find((s) => s.id === entry?.source_id)?.label ?? t('entry.root')
  const path = entry === undefined ? id : entry.path === '' ? rootLabel : entry.path

  return (
    <p className="flex flex-wrap items-center gap-2 rounded-md border bg-card px-3 py-2 text-sm">
      <span className="break-all">
        <Trans i18nKey="search.within" values={{ path }} components={{ strong: <strong /> }} />
      </span>
      <Button variant="outline" size="sm" onClick={onRemove}>
        {t('search.everywhere')}
      </Button>
    </p>
  )
}
