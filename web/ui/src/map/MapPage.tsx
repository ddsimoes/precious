import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, Navigate, useNavigate, useParams, useSearchParams, type To } from 'react-router'

import {
  childSorts,
  childrenQueryKey,
  entryQueryKey,
  fetchChildren,
  fetchEntry,
  fetchTreemap,
  isDrillable,
  isMember,
  treemapQueryKey,
  type ChildSort,
  type EntryRow,
  type SortOrder,
} from '@/api/entries'
import { useSources } from '@/api/sources'
import { useTags } from '@/api/tags'
import { ErrorBanner } from '@/app/ErrorBanner'
import { PageTitle } from '@/app/PageTitle'
import { EntryTable, type TableKeyboard } from '@/components/EntryTable'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { DetailPanel } from '@/detail/DetailPanel'
import { useEntryLink } from '@/detail/useEntryLink'
import { cn } from '@/lib/utils'
import { colorModes, type ColorContext, type ColorMode } from '@/map/colors'
import { MapPath } from '@/map/MapPath'
import { pathSteps } from '@/map/pathSteps'
import { MapStart } from '@/map/MapStart'
import { Treemap } from '@/map/Treemap'
import { ViewerDialog } from '@/viewer/ViewerDialog'

// MapPage answers "where is my space?" (spec §11.2): a treemap and a table of
// one folder, side by side and synchronized, with the detail panel of
// ?entry=. The view (sort, order, color, tag) is kept in the address.
export function MapPage() {
  const { entryId } = useParams()
  return entryId === undefined ? <MapStart /> : <MapFolder folderId={entryId} />
}

function isSort(value: string | null): value is ChildSort {
  return childSorts.some((s) => s === value)
}

function isColorMode(value: string | null): value is ColorMode {
  return colorModes.some((m) => m === value)
}

function MapFolder({ folderId }: { folderId: string }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const entryLink = useEntryLink()
  const [params, setParams] = useSearchParams()
  const sources = useSources()
  const colorId = useId()
  const tagId = useId()
  const [hoveredId, setHoveredId] = useState<string | null>(null)
  const [now] = useState(() => Date.now())
  // viewing is the file Enter opened in the viewer.
  const [viewing, setViewing] = useState<EntryRow | null>(null)
  const tableRef = useRef<HTMLDivElement>(null)
  // focusTable asks for the table's focus once it shows the new order.
  const focusTable = useRef(false)

  const sortParam = params.get('sort')
  const sort: ChildSort = isSort(sortParam) ? sortParam : 'bytes'
  const orderParam = params.get('order')
  const order: SortOrder =
    orderParam === 'asc' || orderParam === 'desc' ? orderParam : sort === 'name' ? 'asc' : 'desc'
  const colorParam = params.get('color')
  const colorMode: ColorMode = isColorMode(colorParam) ? colorParam : 'family'
  const tagParam = params.get('tag')
  const shownTag = tagParam === null || tagParam === '' ? null : Number(tagParam)
  const selectedId = params.get('entry')

  const folder = useQuery({
    queryKey: entryQueryKey(folderId),
    queryFn: ({ signal }) => fetchEntry(folderId, signal),
  })
  const treemap = useQuery({
    queryKey: treemapQueryKey(folderId),
    queryFn: ({ signal }) => fetchTreemap(folderId, signal),
  })
  const children = useInfiniteQuery({
    queryKey: childrenQueryKey(folderId, sort, order),
    queryFn: ({ pageParam, signal }) => fetchChildren(folderId, sort, order, pageParam, signal),
    initialPageParam: null as string | null,
    getNextPageParam: (page) => page.next_cursor,
  })
  const tags = useTags(colorMode === 'tag')

  const rows = useMemo(() => children.data?.pages.flatMap((page) => page.items) ?? [], [children.data])

  // viewSearch keeps the view parameters when moving to another folder.
  const viewSearch = useMemo(() => {
    const next = new URLSearchParams(params)
    next.delete('entry')
    const query = next.toString()
    return query === '' ? '' : `?${query}`
  }, [params])
  const folderLink = useCallback(
    (row: Pick<EntryRow, 'id'>): To => ({ pathname: `/map/${row.id}`, search: viewSearch }),
    [viewSearch],
  )
  const drill = useCallback((id: string) => void navigate(folderLink({ id })), [navigate, folderLink])
  const select = useCallback((id: string) => void navigate({ search: entryLink(id) }), [navigate, entryLink])
  const keyboard = useMemo<TableKeyboard>(
    () => ({
      onOpen: (row) => {
        if (isDrillable(row)) {
          drill(row.id)
        } else if (row.kind === 'file') {
          setViewing(row)
        }
      },
      onClose: () => void navigate({ search: entryLink(null) }),
    }),
    [drill, navigate, entryLink],
  )
  const { fetchNextPage, hasNextPage, isFetchingNextPage } = children
  const loadMore = useCallback(() => void fetchNextPage(), [fetchNextPage])

  const setView = (changes: Record<string, string | null>) => {
    const next = new URLSearchParams(params)
    for (const [key, value] of Object.entries(changes)) {
      if (value === null) {
        next.delete(key)
      } else {
        next.set(key, value)
      }
    }
    setParams(next)
  }
  const sortBy = (key: ChildSort) => {
    if (key === sort) {
      setView({ order: order === 'asc' ? 'desc' : 'asc' })
    } else {
      setView({ sort: key, order: key === 'name' ? 'asc' : 'desc' })
    }
  }
  // The treemap's rest leads to the table sorted by size, where the
  // children too small to draw are listed.
  const showSmaller = () => {
    if (sort === 'bytes' && order === 'desc') {
      tableRef.current?.focus()
    } else {
      focusTable.current = true
      setView({ sort: 'bytes', order: 'desc' })
    }
  }
  useEffect(() => {
    if (focusTable.current && children.data !== undefined && tableRef.current !== null) {
      focusTable.current = false
      tableRef.current.focus()
    }
  }, [children.data])

  const colorContext = useMemo<ColorContext>(
    () => ({
      now,
      tagId: shownTag,
      folderHasTag: shownTag !== null && (folder.data?.intent.tags.some((tag) => tag.id === shownTag) ?? false),
    }),
    [now, shownTag, folder.data],
  )

  const detail = folder.data
  if (detail !== undefined && !isDrillable(detail.entry)) {
    // A file opens in its folder, with its details.
    const parent = detail.ancestors.at(-1)
    if (parent !== undefined) {
      return <Navigate to={{ pathname: `/map/${parent.id}`, search: `?entry=${detail.entry.id}` }} replace />
    }
  }
  const rootLabel = sources.data?.sources.find((s) => s.id === detail?.entry.source_id)?.label ?? t('entry.root')
  const folderName = detail === undefined ? '' : detail.ancestors.length === 0 ? rootLabel : detail.entry.name

  return (
    // The panel takes a column only on wide windows (3xl); on narrower ones
    // it opens over the Map (DetailPanel).
    <div className={cn('grid items-start gap-4', selectedId !== null && '3xl:grid-cols-[minmax(0,1fr)_26rem]')}>
      <div className="@container grid min-w-0 gap-4">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <PageTitle>{t('pages.map')}</PageTitle>
          <div className="flex flex-wrap items-center gap-3 text-sm">
            <div className="flex items-center gap-2">
              <Label htmlFor={colorId}>{t('map.color')}</Label>
              <select
                id={colorId}
                value={colorMode}
                onChange={(event) => setView({ color: event.target.value })}
                className="h-9 rounded-md border border-input bg-card px-2"
              >
                {colorModes.map((mode) => (
                  <option key={mode} value={mode}>
                    {t(`map.colorBy.${mode}`)}
                  </option>
                ))}
              </select>
            </div>
            {colorMode === 'tag' && (
              <div className="flex items-center gap-2">
                <Label htmlFor={tagId}>{t('map.colorTag')}</Label>
                <select
                  id={tagId}
                  value={tagParam ?? ''}
                  onChange={(event) => setView({ tag: event.target.value === '' ? null : event.target.value })}
                  className="h-9 rounded-md border border-input bg-card px-2"
                >
                  <option value="">{t('map.noTagChoice')}</option>
                  {tags.data?.tags.map((tag) => (
                    <option key={tag.id} value={tag.id}>
                      {tag.name}
                    </option>
                  ))}
                </select>
              </div>
            )}
            {/* Search has no items inside archives. */}
            {detail?.entry.kind === 'directory' && !isMember(detail.entry) && (
              <Button asChild variant="outline" size="sm">
                <Link to={{ pathname: '/search', search: `?within=${folderId}` }}>{t('detail.searchHere')}</Link>
              </Button>
            )}
          </div>
        </div>

        {detail !== undefined && (
          <MapPath
            steps={pathSteps(detail.ancestors, { id: detail.entry.id, name: folderName }, rootLabel)}
            folderLink={folderLink}
          />
        )}
        {folder.isError && <ErrorBanner error={folder.error} onRetry={() => void folder.refetch()} />}

        {/* Table and treemap side by side when there is room for both, the
            table first and wider, so that it keeps its main columns and a
            detail panel opened over the Map covers the treemap rather than
            the table (design D23). */}
        <div className="grid gap-4 @min-[76rem]:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
          <div className="min-w-0">
            {children.isError && <ErrorBanner error={children.error} onRetry={() => void children.refetch()} />}
            {children.isPending && (
              <p role="status" className="text-sm text-muted-foreground">
                {t('app.loading')}
              </p>
            )}
            {children.data !== undefined && (
              <EntryTable
                label={t('map.contents', { name: folderName })}
                rows={rows}
                emptyText={t('map.emptyFolder')}
                sort={{ sort, order, onSort: sortBy }}
                folderLink={folderLink}
                hoveredId={hoveredId}
                onHover={setHoveredId}
                selectedId={selectedId}
                keyboard={keyboard}
                tableRef={tableRef}
                hasMore={hasNextPage}
                loadingMore={isFetchingNextPage}
                onLoadMore={loadMore}
              />
            )}
          </div>
          <div className="min-w-0">
            {treemap.isError && <ErrorBanner error={treemap.error} onRetry={() => void treemap.refetch()} />}
            {treemap.data !== undefined && (
              <Treemap
                data={treemap.data}
                folderName={folderName}
                colorMode={colorMode}
                colorContext={colorContext}
                hoveredId={hoveredId}
                selectedId={selectedId}
                onHover={setHoveredId}
                onDrill={drill}
                onSelect={select}
                onOther={showSmaller}
              />
            )}
          </div>
        </div>
      </div>
      <DetailPanel />
      {viewing !== null && <ViewerDialog entry={viewing} onClose={() => setViewing(null)} />}
    </div>
  )
}
