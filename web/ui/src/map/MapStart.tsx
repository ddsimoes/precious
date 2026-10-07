import { useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query'
import { Trans, useTranslation } from 'react-i18next'
import { Link, Navigate, useLocation } from 'react-router'

import { entryQueryKey, fetchEntry } from '@/api/entries'
import { useSources } from '@/api/sources'
import { ErrorBanner } from '@/app/ErrorBanner'
import { PageTitle } from '@/app/PageTitle'
import { useRememberedSource } from '@/app/sourceChoice'
import { useSourceParam } from '@/lib/sourceParams'

// maxDescent bounds the folders followed from a source's top; a real chain
// of single folders is far shorter.
const maxDescent = 64

// startFolder follows only_folder from a source's top folder (r2b design
// D9): the first folder that holds more than one entry, or anything but one
// folder. Each folder's detail is fetched into the entry cache, where the
// Map then finds the last one.
async function startFolder(queryClient: QueryClient, rootId: string): Promise<string> {
  let id = rootId
  for (let depth = 0; depth < maxDescent; depth++) {
    const folderId = id
    const detail = await queryClient.fetchQuery({
      queryKey: entryQueryKey(folderId),
      queryFn: ({ signal }) => fetchEntry(folderId, signal),
    })
    if (detail.only_folder === null) {
      break
    }
    id = detail.only_folder
  }
  return id
}

// MapStart opens the source in the address, or else the source last chosen
// on this browser, or else the first source (r2b design D9). It starts on
// the first folder below the source's top that holds more than one folder,
// with a replace navigation, so Back leaves the Map. Without a source it
// points to Sources.
export function MapStart() {
  const { t } = useTranslation()
  const location = useLocation()
  const queryClient = useQueryClient()
  const sources = useSources()
  const addressed = useSourceParam()
  const remembered = useRememberedSource()
  const chosen = addressed ?? remembered
  const list = sources.data?.sources ?? []
  const source = list.find((s) => s.id === chosen) ?? list[0]
  const rootId = source?.root_entry_id ?? null

  const start = useQuery({
    queryKey: ['map-start', rootId],
    queryFn: () => startFolder(queryClient, rootId ?? ''),
    enabled: rootId !== null,
    // The chain is followed again on every visit: a rescan may change it.
    gcTime: 0,
  })

  if (sources.isError) {
    return <ErrorBanner error={sources.error} onRetry={() => void sources.refetch()} />
  }
  if (sources.isSuccess && source === undefined) {
    return (
      <>
        <PageTitle>{t('pages.map')}</PageTitle>
        <p className="text-sm text-muted-foreground">
          <Trans
            i18nKey="map.noSources"
            components={{ sourcesLink: <Link to="/sources" className="font-medium text-primary underline" /> }}
          />
        </p>
      </>
    )
  }
  if (start.isError) {
    return <ErrorBanner error={start.error} onRetry={() => void start.refetch()} />
  }
  if (start.data === undefined) {
    return (
      <p role="status" className="text-sm text-muted-foreground">
        {t('app.loading')}
      </p>
    )
  }
  return <Navigate to={{ pathname: `/map/${start.data}`, search: location.search }} replace />
}
