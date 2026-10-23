import { createColumnHelper, tableFeatures, useTable } from '@tanstack/react-table'
import { useVirtualizer } from '@tanstack/react-virtual'
import {
  Fragment,
  createContext,
  use,
  useEffect,
  useEffectEvent,
  useId,
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
import { PlaceBar } from '@/components/PlaceBar'
import { Button } from '@/components/ui/button'
import { useEntryLink } from '@/detail/useEntryLink'
import { withShare } from '@/lib/composition'
import { useFormat } from '@/lib/format'
import { unreadableText } from '@/lib/places'
import { cn } from '@/lib/utils'

// EntryTable is the virtualized table of entry rows that the Map (a
// folder's children) and Search (results) share. Sorting and paging happen
// on the server: a sortable header asks for another order, and scrolling
// near the end asks for the next page. Clicking a row opens its detail
// panel (?entry=); a folder's name drills into it. The table stays inside
// its card (design D23): when the card is narrow its lowest-priority
// columns hide, and what still does not fit scrolls sideways inside it. The
// Map turns on the keys (TableKeyboard) and the Only here mode (places).

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
  // places turns on the Only here mode (R5c design D8): the Where copies
  // are column replaces Has copies, each row takes the emphasis of its
  // place, and a name shows another copy of it on a second line.
  places?: boolean
  // archiveName names an archive the open level is in by its ID, for a
  // member placed through a copy of its archive.
  archiveName?: (id: string) => string | undefined
  // tableRef receives the table, which can take the focus.
  tableRef?: Ref<HTMLDivElement>
  hasMore: boolean
  loadingMore: boolean
  onLoadMore: () => void
}

export const rowHeight = 36
// locatedRowHeight leaves room for the location line, or the line naming
// another copy, under the name.
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
  places: boolean
  archiveName: (id: string) => string | undefined
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

// Emphasis is how strongly a row stands out in the Only here mode (R5c
// design D8): an only copy most, a copy only on this source less, what has
// a copy elsewhere or nothing to copy muted, and what is not known yet
// marked as such. Disposable files are muted whatever their place.
type Emphasis = 'strong' | 'light' | 'muted' | 'unknown'

function emphasisOf(entry: EntryRow): Emphasis | null {
  if (entry.place === undefined || entry.place === null) {
    return null
  }
  if (entry.disposable === true) {
    return 'muted'
  }
  switch (entry.place) {
    case 'only_copy':
      return 'strong'
    case 'this_source':
      return 'light'
    case 'other_source':
    case 'none':
      return 'muted'
    case 'unchecked':
    case 'unreadable':
    case 'not_computed':
      return 'unknown'
  }
}

// emphasisStyles are each emphasis's background tint, drawn only while the
// row is neither hovered nor selected so that those still win, and its
// text and left bar. The bar is drawn over the row's edge, so it moves no
// column.
const barBase = "before:absolute before:inset-y-0 before:left-0 before:content-['']"
const emphasisStyles: Record<Emphasis, { tint?: string; rest: string }> = {
  strong: { tint: 'bg-red-500/10', rest: `${barBase} before:w-1 before:bg-red-500` },
  light: { tint: 'bg-orange-400/10', rest: `${barBase} before:w-1 before:bg-orange-400` },
  muted: { rest: 'text-muted-foreground' },
  unknown: { rest: `${barBase} before:border-l-2 before:border-dashed before:border-muted-foreground` },
}

// pathEnd is the end of a path: its last `names` names.
function pathEnd(path: string, names: number): string {
  return path.split('/').slice(-names).join('/')
}

// NotePath is a path named on the line under a name: shown by its end,
// which tells the copy apart, and whole in the note's tooltip and
// accessible description.
interface NotePath {
  shown: string
  full: string
}

type NoteKey =
  | `map.places.copy.${'at' | 'inside' | 'viaArchive'}${'' | 'On'}`
  | `map.places.covered.${'same' | 'inside'}${'' | 'On'}`

// noteTag splits a note's sentence around its pieces: <path/>, <on/>, and
// <archive/>.
const noteTag = /(<(?:path|on|archive)\/>)/

const stopPropagation = (event: { stopPropagation: () => void }) => event.stopPropagation()

// noteNameKept is how many characters, about, of a name in a note stay
// when its line is short of room.
const noteNameKept = 10

// NoteName is a name in a note: cut from its start, as the location line
// is, so that its end stays, and never narrower than about noteNameKept
// characters. A name as short as that never shrinks.
function NoteName({ text }: { text: string }) {
  return (
    <span
      dir="rtl"
      className={cn(
        'truncate text-left',
        [...text].length > noteNameKept ? 'min-w-[min(100%,10ch)]' : 'max-w-full shrink-0',
      )}
    >
      <bdi dir="ltr">{text}</bdi>
    </span>
  )
}

// NoteLink is the link of a note under a name, its sentence laid out piece
// by piece, which Trans cannot do for the words between the pieces. The
// words give way first, down to nothing; then the names, down to their
// last characters; the other copy's source never shrinks. The link itself
// gives way before what follows it on the line, but never gets narrower
// than its names' last characters and the source; it clips what still
// does not fit, so that it never draws over what follows it. Giving way
// first is a shrink weight so large that the others lose no part of a
// pixel, and so show no ellipsis, before it is spent. The link's name is
// the words it shows; its tooltip and description are the sentence with
// the whole paths.
function NoteLink({
  to,
  i18nKey,
  path,
  source,
  archive = '',
}: {
  to: To
  i18nKey: NoteKey
  path: NotePath
  source: string
  archive?: string
}) {
  const { t } = useTranslation()
  const on = t('map.places.copy.on', { source })
  const parts = t(i18nKey).split(noteTag)
  const whole: Record<string, string> = { '<path/>': path.full, '<on/>': on, '<archive/>': archive }
  const shown: Record<string, string> = { '<path/>': path.shown, '<on/>': on, '<archive/>': archive }
  // The link's floor, in characters: the kept end of each name, the source,
  // and a gap between each two.
  const kept = parts.filter((part) => Object.hasOwn(shown, part))
  const floor = kept.reduce(
    (sum, part) => sum + Math.min([...(shown[part] ?? '')].length, part === '<on/>' ? Infinity : noteNameKept),
    0,
  )
  return (
    <Link
      to={to}
      title={parts.map((part) => whole[part] ?? part).join('')}
      className="flex shrink-[1000000] gap-1 overflow-hidden hover:underline"
      style={{ minWidth: `min(100%, calc(${floor}ch + ${Math.max(kept.length - 1, 0)} * 0.25rem))` }}
      onClick={stopPropagation}
    >
      {parts.map((part, i) => {
        switch (part) {
          case '<path/>':
            return <NoteName key={i} text={path.shown} />
          case '<archive/>':
            return <NoteName key={i} text={archive} />
          case '<on/>':
            return (
              <span key={i} className="shrink-0">
                {on}
              </span>
            )
          default: {
            // The spaces stay in the text, outside the words, for copying
            // and for the link's name; the gap draws them.
            const [, before = '', words = '', after = ''] = /^(\s*)(.*?)(\s*)$/s.exec(part) ?? []
            return (
              <Fragment key={i}>
                {before}
                {words !== '' && <span className="min-w-0 shrink-[1000000] truncate">{words}</span>}
                {after}
              </Fragment>
            )
          }
        }
      })}
    </Link>
  )
}

// PlaceNote names, on the line under the name, another copy of a file or
// member (other_copy), or the folder or archive holding all of a folder's
// content (covered_by) with a link to Compare the two (R5c design D9). The
// line never wraps: the note's link gives way to what follows it, which
// stays whole beside it.
function PlaceNote({ entry, name }: { entry: EntryRow; name: string }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const { archiveName, entryLink, places, sourceLabel } = useRowLinks()
  if (!places) {
    return null
  }
  const line = 'flex min-w-0 items-baseline gap-1 whitespace-nowrap text-xs text-muted-foreground'
  const copy = entry.other_copy
  if (copy !== undefined && copy !== null) {
    const on = copy.source_id !== entry.source_id ? 'On' : ''
    // A loose copy shows its folder's name and its own; a member copy its
    // archive's file name and its own, with their whole paths behind.
    const path: NotePath =
      copy.archive_id === null
        ? { shown: pathEnd(copy.path, 2), full: copy.path }
        : {
            shown: t('map.places.copy.member', {
              archive: pathEnd(copy.archive_path, 1),
              member: pathEnd(copy.member_path, 1),
            }),
            full: t('map.places.copy.member', { archive: copy.archive_path, member: copy.member_path }),
          }
    let key: NoteKey = `map.places.copy.${copy.archive_id === null ? 'at' : 'inside'}${on}`
    let archive: string | undefined
    if (copy.via_archive) {
      // The member's own archive has the copy named; it is named by its
      // own name, or else by the name of its copy.
      key = `map.places.copy.viaArchive${on}`
      archive =
        (entry.archive_id === null ? undefined : archiveName(entry.archive_id)) ??
        pathEnd(copy.archive_id === null ? copy.path : copy.member_path, 1)
    }
    const extra = [
      ...(copy.offline ? [t('map.places.copy.offline')] : []),
      ...(copy.more > 0 ? [t('map.places.copy.more', { formatted: fmt.count(copy.more) })] : []),
    ].join(' ')
    return (
      <span className={line}>
        <NoteLink
          to={{ search: entryLink(copy.ref) }}
          i18nKey={key}
          path={path}
          source={sourceLabel(copy.source_id)}
          archive={archive}
        />{' '}
        {/* What follows the link gives way only once the link is at its
            floor, cut with an ellipsis under its own title. */}
        {extra !== '' && (
          <span title={extra} className="min-w-0 truncate">
            {extra}
          </span>
        )}
      </span>
    )
  }
  const covered = entry.covered_by
  if (covered === undefined || covered === null) {
    return null
  }
  const on = covered.other.source_id !== entry.source_id ? 'On' : ''
  return (
    <span className={line}>
      <NoteLink
        to={{ search: entryLink(covered.other.id) }}
        i18nKey={`map.places.covered.${covered.kind}${on}`}
        path={{ shown: pathEnd(covered.other.path, 2), full: covered.other.path }}
        source={sourceLabel(covered.other.source_id)}
      />{' '}
      <Link
        to={{ pathname: '/compare', search: `?${new URLSearchParams({ left: entry.id, right: covered.other.id })}` }}
        aria-label={t('map.places.covered.compareLabel', { name, path: covered.other.path })}
        className="shrink-0 text-primary underline"
        onClick={stopPropagation}
      >
        {t('map.places.covered.compare')}
      </Link>
    </span>
  )
}

function NameCell({ row }: CellProps) {
  const { t } = useTranslation()
  const { entryLink, folderLink, sourceLabel } = useRowLinks()
  const entry = row.original
  const name = rowName(entry, sourceLabel)
  // An only copy's name is the strongest mark of the Only here mode.
  const bold = emphasisOf(entry) === 'strong' && 'font-bold'
  if (!isDrillable(entry)) {
    return (
      <span className="grid min-w-0">
        <Link
          to={{ search: entryLink(entry.id) }}
          className={cn('truncate hover:underline', bold)}
          onClick={(event) => event.stopPropagation()}
        >
          {name}
        </Link>
        <LocationLine entry={entry} />
        <PlaceNote entry={entry} name={name} />
      </span>
    )
  }
  // The lines under the name run under Details too, which leaves them the
  // cell's width but the icon's.
  return (
    <span className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-x-1">
      {entry.kind === 'directory' ? (
        <svg aria-hidden="true" viewBox="0 0 16 16" className="size-4 fill-amber-400">
          <path d="M1 3.5A1.5 1.5 0 0 1 2.5 2h3.6l1.5 1.5h5.9A1.5 1.5 0 0 1 15 5v7.5a1.5 1.5 0 0 1-1.5 1.5h-11A1.5 1.5 0 0 1 1 12.5z" />
        </svg>
      ) : (
        // An archive Precious read completely opens as a folder.
        <svg aria-hidden="true" viewBox="0 0 16 16" className="size-4 fill-amber-700">
          <path d="M2 2.5A1.5 1.5 0 0 1 3.5 1h9A1.5 1.5 0 0 1 14 2.5v11a1.5 1.5 0 0 1-1.5 1.5h-9A1.5 1.5 0 0 1 2 13.5zM7 2v2h2V2zm0 3v2h2V5zm0 3v2h2V8z" />
        </svg>
      )}
      <Link
        to={folderLink(entry)}
        className={cn('truncate font-medium text-primary hover:underline', bold)}
        onClick={(event) => event.stopPropagation()}
      >
        {name}
      </Link>
      <Button asChild variant="ghost" size="sm" className="h-6 px-2 text-xs text-muted-foreground">
        <Link
          to={{ search: entryLink(entry.id) }}
          aria-label={t('map.showDetails', { name })}
          onClick={(event) => event.stopPropagation()}
        >
          {t('detail.label')}
        </Link>
      </Button>
      <span className="col-span-2 col-start-2 grid min-w-0">
        <LocationLine entry={entry} />
        <PlaceNote entry={entry} name={name} />
      </span>
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

// PlaceWords are a row's place in words, and after them a qualifier in a
// span of its own: in a narrow column the qualifier moves under the words,
// and either is cut with an ellipsis only when it does not fit a line of
// its own. The whole sentence is their title.
function PlaceWords({ text, note }: { text: string; note: string | null }) {
  return (
    <span className="flex flex-wrap items-baseline gap-x-1" title={note === null ? text : `${text} ${note}`}>
      <span className="min-w-0 truncate">{text}</span>
      {note !== null && (
        <>
          {' '}
          <span className="min-w-0 max-w-full truncate text-xs">{note}</span>
        </>
      )}
    </span>
  )
}

// PlaceCell says where the copies of a row are, in words (R5c design D8):
// a file by its own place, and a folder or archive by its sums, with a bar
// of its bytes by place. A missing entry has no place and stays blank.
function PlaceCell({ row }: CellProps) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const entry = row.original
  const place = entry.place
  if (place === undefined || place === null) {
    return ''
  }
  let text: string
  let note: string | null = null
  if (!isDrillable(entry)) {
    switch (place) {
      case 'this_source':
        // The row and its named copy, and the copies beyond them (D9).
        text = t('map.places.file.this_source', { formatted: fmt.count((entry.other_copy?.more ?? 0) + 2) })
        break
      case 'other_source':
        text = t('map.places.file.other_source')
        if (entry.place_offline === true) {
          note = t('map.places.file.offlineNote')
        }
        break
      case 'none':
        text = t(
          entry.kind === 'file'
            ? 'map.places.file.empty'
            : entry.kind === 'symlink'
              ? 'map.places.file.link'
              : 'map.places.file.special',
        )
        break
      case 'not_computed':
        text = t('map.places.folder.not_computed')
        break
      default:
        text = t(`map.places.file.${place}`)
    }
    if (entry.disposable === true) {
      const disposable = t('map.places.file.disposable')
      note = note === null ? disposable : `${note} ${disposable}`
    }
    return <PlaceWords text={text} note={note} />
  }
  const sums = entry.places
  switch (place) {
    case 'only_copy':
    case 'this_source':
      text = t(`map.places.folder.${place}`, { bytes: fmt.bytes(sums?.[place].bytes ?? 0) })
      break
    case 'other_source': {
      const disposable = sums?.disposable.files ?? 0
      if (disposable > 0) {
        text = t('map.places.folder.otherBeforeDisposable')
        note = t('map.places.folder.apartDisposable', { count: disposable, formatted: fmt.count(disposable) })
      } else {
        text = t('map.places.folder.other_source')
      }
      break
    }
    case 'unreadable':
      text = unreadableText(entry, t)
      break
    case 'none':
      text = t((sums?.disposable.files ?? 0) > 0 ? 'map.places.folder.onlyDisposable' : 'map.places.folder.nothing')
      break
    default:
      text = t(`map.places.folder.${place}`)
  }
  return (
    <span className="grid gap-0.5">
      <PlaceWords text={text} note={note} />
      <PlaceBar sums={sums} />
    </span>
  )
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
  // The Only here mode's column, in place of Has copies (R5c design D8).
  helper.display({ id: 'place', cell: PlaceCell }),
  helper.display({ id: 'triage', cell: TriageCell }),
  helper.display({ id: 'decision', cell: DecisionCell }),
])

type ColumnId =
  | 'select'
  | 'name'
  | 'size'
  | 'files'
  | 'kind'
  | 'dates'
  | 'duplicated'
  | 'place'
  | 'triage'
  | 'decision'

function isColumnId(id: string | undefined): id is ColumnId {
  return id !== undefined && Object.hasOwn(columnLayout, id)
}

// columnLayout gives each column its grid track and its narrowest width, in
// rem. Columns with a hide rank hide in that order when the card is too
// narrow for every column: Changed and Suggestion first, then Decision,
// then Has copies, then Files; name, size, and type or category always
// stay. In the Only here mode, Where copies are, which the mode is about,
// always stays too, and type or category hides last (placesHide), so that
// every row states its place in words. The mode's own tracks (places) give
// the name, which carries the other copy on its second line, the room the
// place's words and type or category leave.
interface ColumnTrack {
  track: string
  min: number
}
const columnLayout: Record<ColumnId, ColumnTrack & { hide?: number; placesHide?: number; places?: ColumnTrack }> = {
  select: { track: '2rem', min: 2 },
  name: { track: 'minmax(12rem, 1.5fr)', min: 12, places: { track: 'minmax(13rem, 3fr)', min: 13 } },
  size: { track: '6rem', min: 6, places: { track: '5.5rem', min: 5.5 } },
  files: { track: '5rem', min: 5, hide: 5, places: { track: '4.5rem', min: 4.5 } },
  kind: { track: 'minmax(9rem, 2fr)', min: 9, placesHide: 6, places: { track: 'minmax(7rem, 1fr)', min: 7 } },
  dates: { track: 'minmax(9rem, 1.5fr)', min: 9, hide: 1 },
  duplicated: { track: '6rem', min: 6, hide: 4 },
  place: { track: 'minmax(12rem, 1fr)', min: 12 },
  triage: { track: '6rem', min: 6, hide: 2 },
  decision: { track: '9rem', min: 9, hide: 3 },
}

function columnTrack(id: ColumnId, places: boolean): ColumnTrack {
  return (places ? columnLayout[id].places : undefined) ?? columnLayout[id]
}

// A row's column gap and side padding (gap-2, px-3), in rem.
const columnGap = 0.5
const rowPadding = 1.5

const columnSorts: Partial<Record<ColumnId, ChildSort>> = {
  name: 'name',
  size: 'bytes',
  files: 'files',
  dates: 'newest',
  place: 'only_here',
}

// rowWidth is the narrowest width, in rem, of a row of the columns ids.
function rowWidth(ids: ColumnId[], places: boolean): number {
  return ids.reduce((sum, id) => sum + columnTrack(id, places).min, 0) + columnGap * (ids.length - 1) + rowPadding
}

// visibleColumns drops columns in their hide order, that of the Only here
// mode with places, until the row fits in width rem; null (not measured
// yet) keeps them all.
function visibleColumns(ids: ColumnId[], width: number | null, places: boolean): ColumnId[] {
  const rank = (id: ColumnId) =>
    places ? (columnLayout[id].placesHide ?? columnLayout[id].hide) : columnLayout[id].hide
  const hideOrder = ids.filter((id) => rank(id) !== undefined).sort((a, b) => (rank(a) ?? 0) - (rank(b) ?? 0))
  let visible = ids
  for (const id of hideOrder) {
    if (width === null || rowWidth(visible, places) <= width) {
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
  places = false,
  archiveName,
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
  const duplicatedHintId = useId()
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

  // The Only here mode shows where copies are in place of Has copies.
  const columnIds = visibleColumns(
    allColumns
      .map((column) => column.id)
      .filter(isColumnId)
      .filter((id) => selection !== undefined || id !== 'select')
      .filter((id) => id !== (places ? 'duplicated' : 'place')),
    width,
    places,
  )
  const columnKey = columnIds.join(' ')
  const columns = useMemo(
    () => allColumns.filter((column) => isColumnId(column.id) && columnKey.split(' ').includes(column.id)),
    [columnKey],
  )
  const links = useMemo<RowLinks>(() => {
    const labels = new Map(sources.data?.sources.map((s) => [s.id, s.label]))
    return {
      entryLink,
      folderLink,
      selection,
      location,
      places,
      archiveName: archiveName ?? (() => undefined),
      sourceLabel: (id) => labels.get(id) ?? id,
    }
  }, [entryLink, folderLink, selection, location, places, archiveName, sources.data])

  const table = useTable({ features, columns, data: rows, getRowId: (row) => row.id })
  const tableRows = table.getRowModel().rows
  const gridColumns = columnIds.map((id) => columnTrack(id, places).track).join(' ')
  // Header and rows share one narrowest width, so a table that still does
  // not fit scrolls sideways with its header aligned.
  const minWidth = `${rowWidth(columnIds, places)}rem`
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
    estimateSize: () => (location === undefined && !places ? rowHeight : locatedRowHeight),
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

  // walk is where the arrows are going while the address lags behind them:
  // the row they chose last, and the rows they left on the way. Keys pressed
  // faster than the address changes go on from that row instead of being
  // lost (r2b E2). It ends when the address arrives.
  const walk = useRef<{ left: (string | null)[]; to: string } | null>(null)
  useEffect(() => {
    if (walk.current?.to === selectedId) {
      walk.current = null
    }
  }, [selectedId])

  const onKey = useEffectEvent((event: KeyboardEvent) => {
    if (keyboard === undefined || event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey) {
      return
    }
    const origin = event.target instanceof Element ? event.target : null
    const fields = 'input, select, textarea, [contenteditable]:not([contenteditable="false"]), dialog, aside'
    if (origin?.closest(fields) != null || document.querySelector('dialog[open]') !== null) {
      return
    }
    const pending = walk.current !== null && walk.current.left.includes(selectedId) ? walk.current : null
    const current = pending?.to ?? selectedId
    const currentIndex = current === null ? -1 : tableRows.findIndex((row) => row.id === current)
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      if (tableRows.length === 0) {
        return
      }
      event.preventDefault()
      const down = event.key === 'ArrowDown'
      const index = currentIndex < 0 ? (down ? 0 : tableRows.length - 1) : currentIndex + (down ? 1 : -1)
      if (index >= tableRows.length) {
        // Past the last loaded row, the next page comes first.
        if (hasMore && !loadingMore) {
          onLoadMore()
        }
        return
      }
      const row = tableRows[Math.max(0, index)]
      if (row === undefined || row.id === current) {
        return
      }
      focusId.current = row.id
      walk.current = { left: pending === null ? [selectedId] : [...pending.left, pending.to], to: row.id }
      // Walking the rows replaces the address instead of adding to history.
      void navigate({ search: entryLink(row.id) }, { replace: current !== null })
    } else if (event.key === 'Enter') {
      // A focused link or button acts on Enter itself.
      const row = tableRows[currentIndex]
      if (row !== undefined && origin?.closest('a, button, summary') == null) {
        event.preventDefault()
        walk.current = null
        keyboard.onOpen(row.original)
      }
    } else if (event.key === 'Escape' && current !== null) {
      event.preventDefault()
      walk.current = null
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
    const content =
      id === 'select' ? (
        <span className="sr-only">{t('map.columns.select')}</span>
      ) : id === 'place' ? (
        t('map.places.column')
      ) : (
        t(`map.columns.${id}`)
      )
    const key = columnSorts[id]
    if (key === undefined) {
      return content
    }
    const active = sort.sort === key
    return (
      <button
        type="button"
        className="flex items-center gap-1 font-medium hover:underline"
        title={id === 'place' ? t('map.places.sort') : undefined}
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
        {/* The Has copies column's hint, its header's description (ADR 0010),
            outside the header so that it stays out of the header's name. */}
        <span id={duplicatedHintId} hidden>
          {t('map.columns.duplicatedHint')}
        </span>
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
                  title={id === 'duplicated' ? t('map.columns.duplicatedHint') : undefined}
                  aria-describedby={id === 'duplicated' ? duplicatedHintId : undefined}
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
              const hovered = entry.id === hoveredId
              const selected = entry.id === selectedId
              const emphasis = places ? emphasisOf(entry) : null
              const style = emphasis === null ? null : emphasisStyles[emphasis]
              return (
                <div
                  key={row.id}
                  role="row"
                  aria-rowindex={item.index + 2}
                  aria-selected={selected}
                  data-hovered={hovered ? 'true' : undefined}
                  data-row-id={entry.id}
                  data-emphasis={emphasis ?? undefined}
                  // With the keys, the selected row is the table's tab stop.
                  tabIndex={keyboard === undefined ? undefined : selected ? 0 : -1}
                  className={cn(
                    'absolute top-0 left-0 grid w-full cursor-pointer items-center gap-2 border-b px-3',
                    style?.rest,
                    // Hover and selection win over a place's tint.
                    !hovered && !selected && style?.tint,
                    hovered && 'bg-accent',
                    selected && 'bg-primary/10',
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
