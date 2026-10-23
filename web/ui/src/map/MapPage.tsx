import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, Navigate, useNavigate, useParams, useSearchParams, type To } from 'react-router'

import { checkedShare } from '@/api/content'
import {
  childSorts,
  childrenQueryKey,
  entryQueryKey,
  fetchChildren,
  fetchEntry,
  fetchTreemap,
  isDrillable,
  isMember,
  placesSorts,
  treemapQueryKey,
  type ChildSort,
  type EntryDetail,
  type EntryRow,
  type SortOrder,
} from '@/api/entries'
import { useSources } from '@/api/sources'
import { useTags } from '@/api/tags'
import { ErrorBanner } from '@/app/ErrorBanner'
import { PageTitle } from '@/app/PageTitle'
import { EntryTable, type TableKeyboard } from '@/components/EntryTable'
import { PlaceBar } from '@/components/PlaceBar'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { DetailPanel } from '@/detail/DetailPanel'
import { useEntryLink } from '@/detail/useEntryLink'
import { useFormat } from '@/lib/format'
import { placeAmountText, placeParts } from '@/lib/places'
import { cn } from '@/lib/utils'
import { colorModes, placeColors, type ColorContext, type ColorMode } from '@/map/colors'
import { MapPath } from '@/map/MapPath'
import { pathSteps } from '@/map/pathSteps'
import { MapStart } from '@/map/MapStart'
import { Treemap } from '@/map/Treemap'
import { ViewerDialog } from '@/viewer/ViewerDialog'

// MapPage answers "where is my space?" (spec §11.2): a treemap and a table of
// one folder, side by side and synchronized, with the detail panel of
// ?entry=. The view (sort, order, color, tag, and the Only here mode) is
// kept in the address.
export function MapPage() {
  const { entryId } = useParams()
  return entryId === undefined ? <MapStart /> : <MapFolder folderId={entryId} />
}

// isSort accepts the sorts of every listing, and with the Only here mode on
// (places) its own sort as well (R5c design D7).
function isSort(value: string | null, places: boolean): value is ChildSort {
  return childSorts.some((s) => s === value) || (places && placesSorts.some((s) => s === value))
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
  const placesId = useId()
  const placesHintId = useId()
  const hideId = useId()
  const [hoveredId, setHoveredId] = useState<string | null>(null)
  const [now] = useState(() => Date.now())
  // viewing is the file Enter opened in the viewer.
  const [viewing, setViewing] = useState<EntryRow | null>(null)
  const tableRef = useRef<HTMLDivElement>(null)
  // focusTable asks for the table's focus once it shows the new order.
  const focusTable = useRef(false)

  // The Only here mode (R5c design D7): places=1 turns it on, and with it
  // hide=other_source hides what has a copy on another source.
  const placesOn = params.get('places') === '1'
  const hideOn = placesOn && params.get('hide') === 'other_source'
  const sortParam = params.get('sort')
  const sort: ChildSort = isSort(sortParam, placesOn) ? sortParam : 'bytes'
  const orderParam = params.get('order')
  const order: SortOrder =
    orderParam === 'asc' || orderParam === 'desc' ? orderParam : sort === 'name' ? 'asc' : 'desc'
  const colorParam = params.get('color')
  const colorMode: ColorMode = isColorMode(colorParam) ? colorParam : 'family'
  const tagParam = params.get('tag')
  const shownTag = tagParam === null || tagParam === '' ? null : Number(tagParam)
  const selectedId = params.get('entry')

  const folder = useQuery({
    queryKey: entryQueryKey(folderId, placesOn),
    queryFn: ({ signal }) => fetchEntry(folderId, signal, placesOn),
  })
  // The treemap takes the places whenever it is colored by them.
  const treemapPlaces = colorMode === 'places'
  const treemap = useQuery({
    queryKey: treemapQueryKey(folderId, treemapPlaces),
    queryFn: ({ signal }) => fetchTreemap(folderId, signal, treemapPlaces),
  })
  const view = { places: placesOn, hide: hideOn }
  const children = useInfiniteQuery({
    queryKey: childrenQueryKey(folderId, sort, order, view),
    queryFn: ({ pageParam, signal }) => fetchChildren(folderId, sort, order, pageParam, signal, view),
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
  // Turning the mode on colors the treemap by place; turning it off takes
  // away what only the mode means (R5c design D7).
  const setPlaces = (on: boolean) => {
    if (on) {
      setView({ places: '1', color: 'places' })
    } else {
      setView({
        places: null,
        hide: null,
        ...(sortParam === 'only_here' && { sort: 'bytes' }),
        ...(colorParam === 'places' && { color: 'family' }),
      })
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
              <input
                id={placesId}
                type="checkbox"
                role="switch"
                checked={placesOn}
                onChange={(event) => setPlaces(event.target.checked)}
                aria-describedby={placesHintId}
              />
              <Label htmlFor={placesId}>{t('map.places.toggle')}</Label>
              <span id={placesHintId} className="max-w-64 text-xs text-muted-foreground">
                {t('map.places.hint')}
              </span>
            </div>
            {placesOn && (
              <div className="flex items-center gap-2">
                <input
                  id={hideId}
                  type="checkbox"
                  checked={hideOn}
                  onChange={(event) => setView({ hide: event.target.checked ? 'other_source' : null })}
                />
                <Label htmlFor={hideId}>{t('map.places.hide')}</Label>
              </div>
            )}
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
            {placesOn && detail !== undefined && <PlacesTotals detail={detail} />}
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
                places={placesOn}
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

// PlacesTotals is the Only here mode's line above the table (R5c design
// D8): the open folder's bytes by place, in words and as a bar; the share
// checked on every source while it is not all, since an only copy may yet
// turn out to have one (R2.7); and whether the figures are being updated.
// An open archive, or a folder inside one, gives its members' unpacked
// sizes.
function PlacesTotals({ detail }: { detail: EntryDetail }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const titleId = useId()
  const entry = detail.entry
  // A missing folder has no place.
  if (entry.place === undefined || entry.place === null) {
    return null
  }
  const archive = entry.kind !== 'directory' && entry.archive_state === 'complete'
  const sums = archive ? (entry.member_places ?? entry.places) : entry.places
  const parts = placeParts(sums)
  const share = checkedShare(detail.coverage)
  let figures
  if (entry.place === 'not_computed' || sums === undefined || sums === null) {
    figures = <span>{t('map.places.notComputed')}</span>
  } else if (parts.length === 0) {
    figures = <span>{t('map.places.totalsNothing')}</span>
  } else {
    figures = (
      <ul className="flex flex-wrap gap-x-3 gap-y-1">
        {parts.map((part) => (
          <li key={part.key} className="flex items-center gap-1">
            <span aria-hidden="true" className="size-3 rounded-sm" style={{ backgroundColor: placeColors[part.key] }} />
            {placeAmountText(part.key, part.bytes, t, fmt)}
          </li>
        ))}
      </ul>
    )
  }
  return (
    <div role="group" aria-labelledby={titleId} className="mb-2 grid gap-1 text-sm">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span id={titleId} className="font-medium">
          {t(archive || isMember(entry) ? 'map.places.totalsArchive' : 'map.places.totals')}
        </span>
        {figures}
        {detail.places_updating === true && (
          <span role="status" className="text-muted-foreground">
            {t('map.places.updating')}
          </span>
        )}
      </div>
      <PlaceBar sums={sums} />
      {share < 1 && (
        <p className="text-xs text-muted-foreground">
          {/* A share not yet whole never reads 100%. */}
          {t('map.places.coverage', { pct: fmt.percent(Math.min(share, 0.99)) })}
        </p>
      )}
    </div>
  )
}
