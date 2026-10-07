import { createColumnHelper, tableFeatures, useTable } from '@tanstack/react-table'
import { useVirtualizer } from '@tanstack/react-virtual'
import {
  createContext,
  use,
  useEffect,
  useEffectEvent,
  useMemo,
  useRef,
  useState,
  type ReactNode,
  type Ref,
  type UIEvent,
} from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useNavigate, type To } from 'react-router'

import {
  changeDates,
  displayKind,
  duplicationOf,
  isDrillable,
  isMember,
  type ChildSort,
  type EntryRow,
  type SortOrder,
} from '@/api/entries'
import { useSources } from '@/api/sources'
import { CompositionBar } from '@/components/CompositionBar'
import { Button } from '@/components/ui/button'
import { useEntryLink } from '@/detail/useEntryLink'
import { withShare } from '@/lib/composition'
import { useFormat } from '@/lib/format'
import { cn } from '@/lib/utils'

// EntryTable is the virtualized table of entry rows that the Map (a
// folder's children) and Search (results) share. Sorting and paging happen
// on the server: a sortable header asks for another order, and scrolling
// near the end asks for the next page. Clicking a row opens its detail
// panel (?entry=); a folder's name drills into it. The table stays inside
// its card (design D23): when the card is narrow its lowest-priority
// columns hide, and what still does not fit scrolls sideways inside it. The
// Map turns on the keys (TableKeyboard).

export interface TableSort {
  sort: ChildSort
  order: SortOrder
  onSort: (sort: ChildSort) => void
}

export interface TableSelection {
  isSelected: (id: string) => boolean
  toggle: (row: EntryRow) => void
}

// TableKeyboard turns on the Map's keys (r2b design D9), the review lists'
// model: the up and down arrows move the selected row, which opens its
// details; Enter opens it (onOpen); Escape closes the details (onClose).
// Keys typed in a field, in a dialog, or in the detail panel are theirs.
export interface TableKeyboard {
  onOpen: (row: EntryRow) => void
  onClose: () => void
}

interface EntryTableProps {
  label: string
  rows: EntryRow[]
  // rowCount is the number of rows in the whole list, when known.
  rowCount?: number
  emptyText: string
  sort: TableSort
  // folderLink is where a folder's name leads.
  folderLink: (row: EntryRow) => To
  // location shows under each name the folder that holds it, after its
  // source's label for 'sourceAndFolder' (r2b design D9).
  location?: RowLocation
  hoveredId?: string | null
  onHover?: (id: string | null) => void
  selectedId: string | null
  selection?: TableSelection
  keyboard?: TableKeyboard
  // tableRef receives the table, which can take the focus.
  tableRef?: Ref<HTMLDivElement>
  hasMore: boolean
  loadingMore: boolean
  onLoadMore: () => void
}

export const rowHeight = 36
// locatedRowHeight leaves room for the location line under the name.
const locatedRowHeight = 48
// loadAhead is how many rows before the end scrolling asks for the next page.
const loadAhead = 20

// RowLinks is what the cells need from the screen around the table. The
// cells are components defined once, below, so that opening an entry's
// details re-renders them instead of mounting them anew, which would drop
// the focus from the link that opened it.
interface RowLinks {
  entryLink: (id: string | null) => string
  folderLink: (row: EntryRow) => To
  selection: TableSelection | undefined
  location: RowLocation | undefined
  // sourceLabel names a source by its ID, or gives the ID while loading.
  sourceLabel: (id: string) => string
}

type RowLocation = 'folder' | 'sourceAndFolder'

const RowLinksContext = createContext<RowLinks | null>(null)

function useRowLinks(): RowLinks {
  const links = use(RowLinksContext)
  if (links === null) {
    throw new Error('a table cell outside EntryTable')
  }
  return links
}

interface CellProps {
  row: { original: EntryRow }
}

// rowName is a row's name; a source's top folder is named after the source.
function rowName(entry: EntryRow, sourceLabel: (id: string) => string): string {
  return entry.path === '' ? sourceLabel(entry.source_id) : entry.name
}

// LocationLine shows the folder that holds an entry, cut from the left so
// its nearest folders stay readable, after its source's label when asked.
// A top-level entry has no folder above it.
function LocationLine({ entry }: { entry: EntryRow }) {
  const { t } = useTranslation()
  const { location, sourceLabel } = useRowLinks()
  if (location === undefined || entry.path === '') {
    return null
  }
  const slash = entry.path.lastIndexOf('/')
  const folder = slash < 0 ? '' : entry.path.slice(0, slash)
  const text =
    location === 'folder'
      ? folder
      : folder === ''
        ? sourceLabel(entry.source_id)
        : t('search.location', { source: sourceLabel(entry.source_id), folder })
  if (text === '') {
    return null
  }
  return (
    <span dir="rtl" title={text} className="truncate text-left text-xs text-muted-foreground">
      <bdi dir="ltr">{text}</bdi>
    </span>
  )
}

function SelectCell({ row }: CellProps) {
  const { t } = useTranslation()
  const { selection, sourceLabel } = useRowLinks()
  if (selection === undefined) {
    return null
  }
  return (
    <input
      type="checkbox"
      aria-label={t('search.selectRow', { name: rowName(row.original, sourceLabel) })}
      checked={selection.isSelected(row.original.id)}
      onChange={() => selection.toggle(row.original)}
      onClick={(event) => event.stopPropagation()}
    />
  )
}

function NameCell({ row }: CellProps) {
  const { t } = useTranslation()
  const { entryLink, folderLink, sourceLabel } = useRowLinks()
  const entry = row.original
  const name = rowName(entry, sourceLabel)
  if (!isDrillable(entry)) {
    return (
      <span className="grid min-w-0">
        <Link
          to={{ search: entryLink(entry.id) }}
          className="truncate hover:underline"
          onClick={(event) => event.stopPropagation()}
        >
          {name}
        </Link>
        <LocationLine entry={entry} />
      </span>
    )
  }
  return (
    <span className="flex min-w-0 items-center gap-1">
      {entry.kind === 'directory' ? (
        <svg aria-hidden="true" viewBox="0 0 16 16" className="size-4 shrink-0 fill-amber-400">
          <path d="M1 3.5A1.5 1.5 0 0 1 2.5 2h3.6l1.5 1.5h5.9A1.5 1.5 0 0 1 15 5v7.5a1.5 1.5 0 0 1-1.5 1.5h-11A1.5 1.5 0 0 1 1 12.5z" />
        </svg>
      ) : (
        // An archive Precious read completely opens as a folder.
        <svg aria-hidden="true" viewBox="0 0 16 16" className="size-4 shrink-0 fill-amber-700">
          <path d="M2 2.5A1.5 1.5 0 0 1 3.5 1h9A1.5 1.5 0 0 1 14 2.5v11a1.5 1.5 0 0 1-1.5 1.5h-9A1.5 1.5 0 0 1 2 13.5zM7 2v2h2V2zm0 3v2h2V5zm0 3v2h2V8z" />
        </svg>
      )}
      <span className="grid min-w-0">
        <Link
          to={folderLink(entry)}
          className="truncate font-medium text-primary hover:underline"
          onClick={(event) => event.stopPropagation()}
        >
          {name}
        </Link>
        <LocationLine entry={entry} />
      </span>
      <Button asChild variant="ghost" size="sm" className="ml-auto h-6 shrink-0 px-2 text-xs text-muted-foreground">
        <Link
          to={{ search: entryLink(entry.id) }}
          aria-label={t('map.showDetails', { name })}
          onClick={(event) => event.stopPropagation()}
        >
          {t('detail.label')}
        </Link>
      </Button>
    </span>
  )
}

// An entry that could not be read has no figures: its cells show a dash,
// never 0 B, 0 files, or 0% (r2b design D9).
const noFigure = '—'

// SizeCell shows the size, and under a folder's its composition bar.
function SizeCell({ row }: CellProps) {
  const fmt = useFormat()
  const entry = row.original
  if (entry.state === 'unreadable') {
    return noFigure
  }
  if (entry.kind !== 'directory') {
    return fmt.bytes(entry.total_bytes)
  }
  return (
    <span className="grid gap-0.5">
      <span className="truncate">{fmt.bytes(entry.total_bytes)}</span>
      <CompositionBar composition={entry.composition} />
    </span>
  )
}

function FilesCell({ row }: CellProps) {
  const fmt = useFormat()
  if (row.original.kind !== 'directory') {
    return ''
  }
  return row.original.state === 'unreadable' ? noFigure : fmt.count(row.original.total_files)
}

// KindCell shows the category, or the file type of what has none, with a
// mixed folder's dominant share; an entry that could not be read says so.
function KindCell({ row }: CellProps) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const entry = row.original
  if (entry.state === 'unreadable') {
    return <span className="text-amber-700">{t('entry.unreadable')}</span>
  }
  let label: string
  if (entry.category !== null && entry.category !== 'unknown') {
    label = t(`entry.category.${entry.category}`)
  } else {
    const kind = displayKind(entry)
    label = kind === null ? t(`entry.kind.${entry.kind}`) : t(`entry.fileKind.${kind}`)
  }
  const text = withShare(label, entry.composition, t, fmt)
  return <span title={text}>{text}</span>
}

// DatesCell leaves a folder with no file inside blank: it has no change
// dates, unlike a folder whose files' dates are unknown.
function DatesCell({ row }: CellProps) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const entry = row.original
  const empty = entry.kind === 'directory' && entry.total_files === 0
  return fmt.dateSpan(...changeDates(entry)) ?? (empty ? '' : t('entry.unknownDate'))
}

function TriageCell({ row }: CellProps) {
  const { t } = useTranslation()
  return row.original.triage === null ? '' : t(`entry.triage.${row.original.triage}`)
}

// DuplicatedCell shows the share of a folder's bytes that have another
// copy, from its figures (R2 design D10), and a checked file's number of
// copies (r2b design D9).
function DuplicatedCell({ row }: CellProps) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const entry = row.original
  if (entry.state === 'unreadable') {
    return noFigure
  }
  // An unreadable file never gets checked: "Not checked" would promise it.
  if (entry.content_state === 'unreadable') {
    return t('map.dupCell.unreadable')
  }
  const duplication = duplicationOf(entry)
  if (duplication === null) {
    return ''
  }
  const percent = fmt.percent(duplication.fraction)
  if (duplication.checked) {
    if (entry.kind === 'directory') {
      return percent
    }
    const copies = entry.copies ?? 1
    return copies > 1
      ? t('map.dupCell.copies', { count: copies, formatted: fmt.count(copies) })
      : t('map.dupCell.noOtherCopy')
  }
  return duplication.fraction === 0 ? t('map.dupCell.unchecked') : t('map.dupCell.partial', { percent })
}

// DecisionCell stays blank for what only follows "undecided", and says an
// inherited decision briefly, with its full wording as the title (r2b
// design D9).
function DecisionCell({ row }: CellProps) {
  const { t } = useTranslation()
  const entry = row.original
  if (entry.decision !== null) {
    return t(`home.decision.${entry.decision}`)
  }
  if (entry.eff_decision === 'undecided') {
    return ''
  }
  const effective = t(`home.decision.${entry.eff_decision}`)
  if (isMember(entry)) {
    return <span title={t('entry.withArchive', { decision: effective })}>{t('entry.withArchiveShort')}</span>
  }
  return (
    <span title={t('entry.inherited', { decision: effective })}>
      {t('entry.inheritedShort', { decision: effective })}
    </span>
  )
}

const features = tableFeatures({})
const helper = createColumnHelper<typeof features, EntryRow>()

// The columns, in order. Their headers are drawn by EntryTable.
const allColumns = helper.columns([
  helper.display({ id: 'select', cell: SelectCell }),
  helper.display({ id: 'name', cell: NameCell }),
  helper.display({ id: 'size', cell: SizeCell }),
  helper.display({ id: 'files', cell: FilesCell }),
  helper.display({ id: 'kind', cell: KindCell }),
  helper.display({ id: 'dates', cell: DatesCell }),
  helper.display({ id: 'duplicated', cell: DuplicatedCell }),
  helper.display({ id: 'triage', cell: TriageCell }),
  helper.display({ id: 'decision', cell: DecisionCell }),
])

type ColumnId = 'select' | 'name' | 'size' | 'files' | 'kind' | 'dates' | 'duplicated' | 'triage' | 'decision'

function isColumnId(id: string | undefined): id is ColumnId {
  return id !== undefined && Object.hasOwn(columnLayout, id)
}

// columnLayout gives each column its grid track and its narrowest width, in
// rem. Columns with a hide rank hide in that order when the card is too
// narrow for every column: Changed and Suggestion first, then Decision,
// then Duplicated; name, size, and type or category always stay.
const columnLayout: Record<ColumnId, { track: string; min: number; hide?: number }> = {
  select: { track: '2rem', min: 2 },
  name: { track: 'minmax(12rem, 1.5fr)', min: 12 },
  size: { track: '6rem', min: 6 },
  files: { track: '5rem', min: 5, hide: 5 },
  kind: { track: 'minmax(9rem, 2fr)', min: 9 },
  dates: { track: 'minmax(9rem, 1.5fr)', min: 9, hide: 1 },
  duplicated: { track: '6rem', min: 6, hide: 4 },
  triage: { track: '6rem', min: 6, hide: 2 },
  decision: { track: '9rem', min: 9, hide: 3 },
}
// A row's column gap and side padding (gap-2, px-3), in rem.
const columnGap = 0.5
const rowPadding = 1.5

const columnSorts: Partial<Record<ColumnId, ChildSort>> = {
  name: 'name',
  size: 'bytes',
  files: 'files',
  dates: 'newest',
}

// rowWidth is the narrowest width, in rem, of a row of the columns ids.
function rowWidth(ids: ColumnId[]): number {
  return ids.reduce((sum, id) => sum + columnLayout[id].min, 0) + columnGap * (ids.length - 1) + rowPadding
}

// visibleColumns drops columns in their hide order until the row fits in
// width rem; null (not measured yet) keeps them all.
function visibleColumns(ids: ColumnId[], width: number | null): ColumnId[] {
  const hideOrder = ids
    .filter((id) => columnLayout[id].hide !== undefined)
    .sort((a, b) => (columnLayout[a].hide ?? 0) - (columnLayout[b].hide ?? 0))
  let visible = ids
  for (const id of hideOrder) {
    if (width === null || rowWidth(visible) <= width) {
      break
    }
    visible = visible.filter((v) => v !== id)
  }
  return visible
}

export function EntryTable({
  label,
  rows,
  rowCount,
  emptyText,
  sort,
  folderLink,
  location,
  hoveredId = null,
  onHover,
  selectedId,
  selection,
  keyboard,
  tableRef,
  hasMore,
  loadingMore,
  onLoadMore,
}: EntryTableProps) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const entryLink = useEntryLink()
  const sources = useSources()
  const scrollRef = useRef<HTMLDivElement>(null)
  const headerRef = useRef<HTMLDivElement>(null)
  // width is the room for a row inside the card, in rem, once measured.
  const [width, setWidth] = useState<number | null>(null)

  useEffect(() => {
    const scroller = scrollRef.current
    if (scroller === null || typeof ResizeObserver === 'undefined') {
      return
    }
    const observer = new ResizeObserver(() => {
      const rem = Number.parseFloat(getComputedStyle(document.documentElement).fontSize) || 16
      setWidth(scroller.clientWidth / rem)
    })
    observer.observe(scroller)
    return () => observer.disconnect()
  }, [])

  const columnIds = visibleColumns(
    allColumns.map((column) => column.id).filter(isColumnId).filter((id) => selection !== undefined || id !== 'select'),
    width,
  )
  const columnKey = columnIds.join(' ')
  const columns = useMemo(
    () => allColumns.filter((column) => isColumnId(column.id) && columnKey.split(' ').includes(column.id)),
    [columnKey],
  )
  const links = useMemo<RowLinks>(() => {
    const labels = new Map(sources.data?.sources.map((s) => [s.id, s.label]))
    return { entryLink, folderLink, selection, location, sourceLabel: (id) => labels.get(id) ?? id }
  }, [entryLink, folderLink, selection, location, sources.data])

  const table = useTable({ features, columns, data: rows, getRowId: (row) => row.id })
  const tableRows = table.getRowModel().rows
  const gridColumns = columnIds.map((id) => columnLayout[id].track).join(' ')
  // Header and rows share one narrowest width, so a table that still does
  // not fit scrolls sideways with its header aligned.
  const minWidth = `${rowWidth(columnIds)}rem`
  const scrollHeader = (event: UIEvent<HTMLDivElement>) => {
    if (headerRef.current !== null) {
      headerRef.current.scrollLeft = event.currentTarget.scrollLeft
    }
  }

  // The React Compiler cannot memoize useVirtualizer's results and skips this
  // component, which is what the virtualizer needs to re-render on scroll.
  // eslint-disable-next-line react-hooks/incompatible-library
  const virtualizer = useVirtualizer({
    count: tableRows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => (location === undefined ? rowHeight : locatedRowHeight),
    getItemKey: (index) => tableRows[index]?.id ?? index,
    overscan: 10,
  })
  const items = virtualizer.getVirtualItems()
  const lastIndex = items.at(-1)?.index

  useEffect(() => {
    if (lastIndex !== undefined && lastIndex >= tableRows.length - loadAhead && hasMore && !loadingMore) {
      onLoadMore()
    }
  }, [lastIndex, tableRows.length, hasMore, loadingMore, onLoadMore])

  // A row selected elsewhere (the treemap, the detail panel) is scrolled to.
  const selectedIndex = selectedId === null ? -1 : tableRows.findIndex((row) => row.id === selectedId)
  useEffect(() => {
    if (selectedIndex >= 0) {
      virtualizer.scrollToIndex(selectedIndex, { align: 'auto' })
    }
  }, [selectedIndex, virtualizer])

  // focusId is the row the keys selected: it takes the focus once the
  // virtualizer has drawn it, so assistive technology follows.
  const focusId = useRef<string | null>(null)
  useEffect(() => {
    const id = focusId.current
    if (id === null || id !== selectedId) {
      return
    }
    const row = scrollRef.current?.querySelector<HTMLElement>(`[data-row-id="${id}"]`)
    if (row !== null && row !== undefined) {
      focusId.current = null
      row.focus({ preventScroll: true })
    }
  })

  const onKey = useEffectEvent((event: KeyboardEvent) => {
    if (keyboard === undefined || event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey) {
      return
    }
    const origin = event.target instanceof Element ? event.target : null
    const fields = 'input, select, textarea, [contenteditable]:not([contenteditable="false"]), dialog, aside'
    if (origin?.closest(fields) != null || document.querySelector('dialog[open]') !== null) {
      return
    }
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      if (tableRows.length === 0) {
        return
      }
      event.preventDefault()
      const down = event.key === 'ArrowDown'
      const index = selectedIndex < 0 ? (down ? 0 : tableRows.length - 1) : selectedIndex + (down ? 1 : -1)
      if (index >= tableRows.length) {
        // Past the last loaded row, the next page comes first.
        if (hasMore && !loadingMore) {
          onLoadMore()
        }
        return
      }
      const row = tableRows[Math.max(0, index)]
      if (row === undefined || row.id === selectedId) {
        return
      }
      focusId.current = row.id
      // Walking the rows replaces the address instead of adding to history.
      void navigate({ search: entryLink(row.id) }, { replace: selectedId !== null })
    } else if (event.key === 'Enter') {
      // A focused link or button acts on Enter itself.
      const row = tableRows[selectedIndex]
      if (row !== undefined && origin?.closest('a, button, summary') == null) {
        event.preventDefault()
        keyboard.onOpen(row.original)
      }
    } else if (event.key === 'Escape' && selectedId !== null) {
      event.preventDefault()
      keyboard.onClose()
    }
  })

  const keys = keyboard !== undefined
  useEffect(() => {
    if (!keys) {
      return
    }
    const listener = (event: KeyboardEvent) => onKey(event)
    document.addEventListener('keydown', listener)
    return () => document.removeEventListener('keydown', listener)
  }, [keys])

  const headerCell = (id: ColumnId): ReactNode => {
    const content = id === 'select' ? <span className="sr-only">{t('map.columns.select')}</span> : t(`map.columns.${id}`)
    const key = columnSorts[id]
    if (key === undefined) {
      return content
    }
    const active = sort.sort === key
    return (
      <button
        type="button"
        className="flex items-center gap-1 font-medium hover:underline"
        onClick={() => sort.onSort(key)}
      >
        {content}
        {active && <span aria-hidden="true">{sort.order === 'asc' ? '▲' : '▼'}</span>}
      </button>
    )
  }

  return (
    <RowLinksContext value={links}>
      <div
        ref={tableRef}
        role="table"
        aria-label={label}
        aria-rowcount={(rowCount ?? tableRows.length) + 1}
        tabIndex={tableRef === undefined ? undefined : -1}
        className="flex min-h-0 min-w-0 flex-col overflow-hidden rounded-lg border bg-card text-sm"
      >
        {/* The header follows the rows' sideways scroll; both reserve the
            scroll bar's gutter, so their columns line up. */}
        <div ref={headerRef} role="rowgroup" className="overflow-hidden border-b bg-muted/50 [scrollbar-gutter:stable]">
          <div
            role="row"
            aria-rowindex={1}
            className="grid items-center gap-2 px-3 py-2 text-left text-xs text-muted-foreground"
            style={{ gridTemplateColumns: gridColumns, minWidth }}
          >
            {columnIds.map((id) => {
              const key = columnSorts[id]
              return (
                <div
                  key={id}
                  role="columnheader"
                  aria-sort={
                    key !== undefined && sort.sort === key
                      ? sort.order === 'asc'
                        ? 'ascending'
                        : 'descending'
                      : undefined
                  }
                  className="min-w-0 truncate"
                >
                  {headerCell(id)}
                </div>
              )
            })}
          </div>
        </div>
        <div
          ref={scrollRef}
          role="rowgroup"
          className="h-[60vh] min-h-64 overflow-auto [scrollbar-gutter:stable]"
          onScroll={scrollHeader}
        >
          {tableRows.length === 0 && !hasMore && <p className="p-4 text-muted-foreground">{emptyText}</p>}
          <div className="relative w-full" style={{ height: virtualizer.getTotalSize(), minWidth }}>
            {items.map((item) => {
              const row = tableRows[item.index]
              if (row === undefined) {
                return null
              }
              const entry = row.original
              return (
                <div
                  key={row.id}
                  role="row"
                  aria-rowindex={item.index + 2}
                  aria-selected={entry.id === selectedId}
                  data-hovered={entry.id === hoveredId ? 'true' : undefined}
                  data-row-id={entry.id}
                  // With the keys, the selected row is the table's tab stop.
                  tabIndex={keyboard === undefined ? undefined : entry.id === selectedId ? 0 : -1}
                  className={cn(
                    'absolute top-0 left-0 grid w-full cursor-pointer items-center gap-2 border-b px-3',
                    entry.id === hoveredId && 'bg-accent',
                    entry.id === selectedId && 'bg-primary/10',
                    entry.state === 'missing' && 'text-muted-foreground line-through',
                  )}
                  style={{
                    gridTemplateColumns: gridColumns,
                    height: item.size,
                    transform: `translateY(${item.start}px)`,
                  }}
                  onMouseEnter={() => onHover?.(entry.id)}
                  onMouseLeave={() => onHover?.(null)}
                  onClick={() => void navigate({ search: entryLink(entry.id) })}
                >
                  {row.getAllCells().map((cell) => (
                    <div key={cell.id} role="cell" className="min-w-0 truncate">
                      <table.FlexRender cell={cell} />
                    </div>
                  ))}
                </div>
              )
            })}
          </div>
          {hasMore && (
            <div className="flex justify-center p-2">
              <Button variant="outline" size="sm" disabled={loadingMore} onClick={onLoadMore}>
                {loadingMore ? t('map.loadingMore') : t('map.loadMore')}
              </Button>
            </div>
          )}
        </div>
      </div>
    </RowLinksContext>
  )
}
