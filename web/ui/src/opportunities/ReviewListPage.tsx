import { useInfiniteQuery } from '@tanstack/react-query'
import { useEffect, useEffectEvent, useId, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router'

import type { Copy } from '@/api/content'
import { useDecide } from '@/api/decisions'
import { changeDates, isMember, type EntryRow } from '@/api/entries'
import type { Decision } from '@/api/home'
import {
  fetchReviewPage,
  isReviewList,
  reviewQueryKey,
  type Card,
  type ReviewListName,
  type ReviewRow,
} from '@/api/opportunities'
import { ErrorBanner } from '@/app/ErrorBanner'
import { NotFoundPage } from '@/app/NotFoundPage'
import { PageTitle } from '@/app/PageTitle'
import { DecisionButtons } from '@/components/DecisionButtons'
import { SourceFilter } from '@/components/SourceFilter'
import { Button } from '@/components/ui/button'
import { DetailPanel } from '@/detail/DetailPanel'
import { useEntryLink } from '@/detail/useEntryLink'
import { useFormat } from '@/lib/format'
import { useSourceLabel, useSourceParam } from '@/lib/sourceParams'
import { cn } from '@/lib/utils'
import { ListSelectAll } from '@/opportunities/ListSelectAll'
import { summaryLine } from '@/opportunities/summary'

// ReviewListPage is the review list of one opportunity card (spec §11.5, R2
// design D13), /opportunities/<list>?source=&decided=1: its open rows, and
// the decided ones when asked, decided with the usual controls or from the
// keyboard.
export function ReviewListPage() {
  const { list } = useParams()
  if (!isReviewList(list)) {
    return <NotFoundPage />
  }
  return <ReviewList key={list} list={list} />
}

// CopyItem is one copy a duplicates row expands into: a copy of a file, or a
// side of a folder relation.
interface CopyItem {
  ref: string
  sourceId: string
  path: string
  effDecision: Decision
  // own is the own decision when known (a relation's sides report it).
  own?: Decision | null
  member: boolean
  hardLink: boolean
  offline: boolean
}

function fromCopy(copy: Copy): CopyItem {
  return {
    ref: copy.ref,
    sourceId: copy.source_id,
    path: copy.path,
    effDecision: copy.eff_decision,
    member: copy.archive_id !== null,
    hardLink: copy.hard_link,
    offline: copy.offline,
  }
}

function fromRow(row: EntryRow): CopyItem {
  return {
    ref: row.id,
    sourceId: row.source_id,
    path: row.path,
    effDecision: row.eff_decision,
    own: row.decision,
    member: isMember(row),
    hardLink: false,
    offline: false,
  }
}

// copyItems lists what a duplicates row expands into: its copies, or the
// two sides of its relation.
function copyItems(row: ReviewRow): CopyItem[] {
  if (row.copies !== null) {
    return row.copies.map(fromCopy)
  }
  if (row.relation !== null && row.entry !== null) {
    return [fromRow(row.entry), fromRow(row.relation.other)]
  }
  return []
}

// Section is a part of the list: its open rows, or its decided ones.
type Section = 'open' | 'decided'

// Target is what the keyboard moves through: a row, or a copy of an
// expanded duplicates row, in a section. row is the key of its row;
// entryId is what K, D, and L decide (null: the target has no decision of
// its own); open is what Enter does.
interface Target {
  key: string
  row: string
  section: Section
  entryId: string | null
  open: () => void
}

// A row's key names its section: a row decided from the open rows leaves
// them, even when it shows again among the decided ones.
function rowKey(section: Section, row: ReviewRow) {
  return `${section}:row:${row.id}`
}

function copyKey(section: Section, row: ReviewRow, item: CopyItem) {
  return `${section}:copy:${row.id}:${item.ref}`
}

const keyChoices: Record<string, 'keep' | 'discard' | 'later'> = { k: 'keep', d: 'discard', l: 'later' }

function ReviewList({ list }: { list: ReviewListName }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const entryLink = useEntryLink()
  const [params, setParams] = useSearchParams()
  const source = useSourceParam()
  const showDecided = params.get('decided') === '1'
  const decidedId = useId()
  const listRef = useRef<HTMLDivElement>(null)
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => new Set())
  // cursor is the selected target, its row's key, and its index when it was
  // selected: when a decided row leaves the list, the row that took its
  // place is selected, and a hidden copy hands the selection to its row.
  // focus is set when the keyboard selected it.
  const [cursor, setCursor] = useState<{ key: string; row: string; index: number; focus: boolean } | null>(null)
  const decide = useDecide()
  const duplicates = list === 'duplicates'

  const openRows = useInfiniteQuery({
    queryKey: reviewQueryKey(list, source, false),
    queryFn: ({ pageParam, signal }) => fetchReviewPage(list, source, false, pageParam, signal),
    initialPageParam: null as string | null,
    getNextPageParam: (page) => page.next_cursor,
  })
  const decidedRows = useInfiniteQuery({
    queryKey: reviewQueryKey(list, source, true),
    queryFn: ({ pageParam, signal }) => fetchReviewPage(list, source, true, pageParam, signal),
    initialPageParam: null as string | null,
    getNextPageParam: (page) => page.next_cursor,
    enabled: showDecided,
  })
  const rows = useMemo(() => openRows.data?.pages.flatMap((page) => page.items) ?? [], [openRows.data])
  const decided = useMemo(
    () => (showDecided ? (decidedRows.data?.pages.flatMap((page) => page.items) ?? []) : []),
    [showDecided, decidedRows.data],
  )
  const card = openRows.data?.pages[0]?.card
  const sections = {
    open: { rows, pages: openRows },
    decided: { rows: decided, pages: decidedRows },
  }

  const toggle = (row: ReviewRow) =>
    setExpanded((current) => {
      const next = new Set(current)
      if (next.has(row.id)) {
        next.delete(row.id)
      } else {
        next.add(row.id)
      }
      return next
    })

  const targets: Target[] = []
  for (const section of ['open', 'decided'] as const) {
    for (const row of sections[section].rows) {
      const key = rowKey(section, row)
      if (!duplicates) {
        const entry = row.entry
        if (entry !== null) {
          targets.push({
            key,
            row: key,
            section,
            entryId: isMember(entry) ? null : entry.id,
            open: () => void navigate({ search: entryLink(entry.id) }),
          })
        }
        continue
      }
      targets.push({ key, row: key, section, entryId: null, open: () => toggle(row) })
      if (expanded.has(row.id)) {
        for (const item of copyItems(row)) {
          targets.push({
            key: copyKey(section, row, item),
            row: key,
            section,
            entryId: item.member ? null : item.ref,
            open: () => void navigate({ search: entryLink(item.ref) }),
          })
        }
      }
    }
  }
  const cursorAt = cursor === null ? -1 : targets.findIndex((target) => target.key === cursor.key)

  // keyboardCursor selects the target at index from the keyboard.
  const keyboardCursor = (index: number) => {
    const target = targets[index]
    return target === undefined ? null : { key: target.key, row: target.row, index, focus: true }
  }

  // A selected target that left the list hands the selection to its row (a
  // copy hidden), or to the target that took its place (a decided row).
  if (cursor !== null && cursorAt < 0) {
    const row = targets.findIndex((target) => target.key === cursor.row)
    setCursor(keyboardCursor(row >= 0 ? row : Math.min(cursor.index, targets.length - 1)))
  }

  const move = (delta: 1 | -1) => {
    if (targets.length === 0) {
      return
    }
    const index = cursorAt < 0 ? (delta === 1 ? 0 : targets.length - 1) : cursorAt + delta
    setCursor(keyboardCursor(Math.max(0, Math.min(targets.length - 1, index))))
  }

  const onKey = useEffectEvent((event: KeyboardEvent) => {
    if (event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey) {
      return
    }
    // Keys typed into a field, or inside a dialog or the detail panel, are
    // theirs.
    const origin = event.target instanceof Element ? event.target : null
    if (origin !== null && origin.closest('input, select, textarea, [contenteditable="true"], dialog, aside') !== null) {
      return
    }
    const current = cursorAt < 0 ? undefined : targets[cursorAt]
    const key = event.key.toLowerCase()
    const choice = keyChoices[key]
    if (choice !== undefined) {
      if (current !== undefined && current.entryId !== null) {
        event.preventDefault()
        decide.mutate({ id: current.entryId, choice })
      }
    } else if (key === 'j' || event.key === 'ArrowDown') {
      event.preventDefault()
      move(1)
    } else if (event.key === 'ArrowUp') {
      event.preventDefault()
      move(-1)
    } else if (event.key === 'Enter') {
      // A focused link or button acts on Enter itself.
      if (current !== undefined && (origin === null || origin.closest('a, button, summary') === null)) {
        event.preventDefault()
        current.open()
      }
    }
  })

  useEffect(() => {
    const listener = (event: KeyboardEvent) => onKey(event)
    document.addEventListener('keydown', listener)
    return () => document.removeEventListener('keydown', listener)
  }, [])

  // A target the keyboard selected takes the focus, so it scrolls into view
  // and assistive technology follows.
  useEffect(() => {
    if (cursor?.focus === true) {
      listRef.current?.querySelector<HTMLElement>(`[data-review-key="${cursor.key}"]`)?.focus()
    }
  }, [cursor])

  const select = (key: string) => {
    const index = targets.findIndex((target) => target.key === key)
    if (index >= 0 && cursor?.key !== key) {
      setCursor({ key, row: targets[index]!.row, index, focus: false })
    }
  }

  const setDecided = (on: boolean) => {
    const next = new URLSearchParams(params)
    if (on) {
      next.set('decided', '1')
    } else {
      next.delete('decided')
    }
    setParams(next)
  }

  const listLabel = t(`opportunities.list.${list}`)
  const rowProps = {
    duplicates,
    cursorKey: cursor?.key ?? null,
    expanded,
    onSelect: select,
    onToggle: toggle,
  }

  return (
    <div className={cn('grid items-start gap-4', params.has('entry') && '3xl:grid-cols-[minmax(0,1fr)_26rem]')}>
      <div ref={listRef} className="grid min-w-0 gap-4">
        <Link
          to={{ pathname: '/opportunities', search: source === null ? '' : `?${new URLSearchParams({ source })}` }}
          className="text-sm font-medium text-primary hover:underline"
        >
          {t('review.back')}
        </Link>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <PageTitle>{listLabel}</PageTitle>
          <SourceFilter />
        </div>
        <CardFigures card={card} list={list} />
        <p className="text-xs text-muted-foreground">{t('review.keys')}</p>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <label htmlFor={decidedId} className="flex items-center gap-2 text-sm">
            <input
              id={decidedId}
              type="checkbox"
              checked={showDecided}
              onChange={(event) => setDecided(event.target.checked)}
            />
            {t('review.showDecided')}
          </label>
          {duplicates ? (
            <p className="text-sm text-muted-foreground">{t('review.noSelectAll')}</p>
          ) : (
            card !== undefined && rows.length > 0 && <ListSelectAll list={list} source={source} />
          )}
        </div>
        {decide.isError && <ErrorBanner error={decide.error} onDismiss={() => decide.reset()} />}

        {openRows.isPending && (
          <p role="status" className="text-sm text-muted-foreground">
            {t('app.loading')}
          </p>
        )}
        {openRows.isError && <ErrorBanner error={openRows.error} onRetry={() => void openRows.refetch()} />}
        {openRows.data !== undefined && (
          <RowList
            section="open"
            label={t('review.rows', { list: listLabel })}
            rows={rows}
            emptyText={t('review.noRows')}
            hasMore={openRows.hasNextPage}
            loadingMore={openRows.isFetchingNextPage}
            onLoadMore={() => void openRows.fetchNextPage()}
            {...rowProps}
          />
        )}

        {showDecided && (
          <section aria-label={t('review.decided')} className="grid gap-2">
            <h2 className="text-base font-semibold">{t('review.decided')}</h2>
            {decidedRows.isError && (
              <ErrorBanner error={decidedRows.error} onRetry={() => void decidedRows.refetch()} />
            )}
            {decidedRows.data !== undefined && (
              <RowList
                section="decided"
                label={t('review.decidedRows', { list: listLabel })}
                rows={decided}
                emptyText={t('review.noDecided')}
                hasMore={decidedRows.hasNextPage}
                loadingMore={decidedRows.isFetchingNextPage}
                onLoadMore={() => void decidedRows.fetchNextPage()}
                {...rowProps}
              />
            )}
          </section>
        )}
      </div>
      <DetailPanel />
    </div>
  )
}

function CardFigures({ card, list }: { card: Card | undefined; list: ReviewListName }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  if (card === undefined) {
    return null
  }
  return (
    <div className="grid gap-1 text-sm">
      <p>
        <span className="text-xl font-semibold">{fmt.bytes(card.bytes)}</span>{' '}
        {t('opportunities.rows', { count: card.rows, formatted: fmt.count(card.rows) })} ·{' '}
        {t(`opportunities.basis.${card.basis}`)}
      </p>
      <p className="text-muted-foreground">{t(`opportunities.help.${list}`)}</p>
    </div>
  )
}

interface RowListProps {
  label: string
  rows: ReviewRow[]
  emptyText: string
  hasMore: boolean
  loadingMore: boolean
  onLoadMore: () => void
  section: Section
  duplicates: boolean
  cursorKey: string | null
  expanded: ReadonlySet<string>
  onSelect: (key: string) => void
  onToggle: (row: ReviewRow) => void
}

function RowList({ label, rows, emptyText, hasMore, loadingMore, onLoadMore, ...rowProps }: RowListProps) {
  const { t } = useTranslation()
  if (rows.length === 0 && !hasMore) {
    return <p className="text-sm text-muted-foreground">{emptyText}</p>
  }
  return (
    <>
      <ul aria-label={label} className="grid gap-2">
        {rows.map((row) =>
          rowProps.duplicates ? (
            <DuplicatesRow key={row.id} row={row} {...rowProps} />
          ) : (
            <EntryReviewRow key={row.id} row={row} {...rowProps} />
          ),
        )}
      </ul>
      {hasMore && (
        <div className="flex justify-center">
          <Button variant="outline" size="sm" disabled={loadingMore} onClick={onLoadMore}>
            {loadingMore ? t('map.loadingMore') : t('map.loadMore')}
          </Button>
        </div>
      )}
    </>
  )
}

type RowProps = Omit<RowListProps, 'label' | 'rows' | 'emptyText' | 'hasMore' | 'loadingMore' | 'onLoadMore'> & {
  row: ReviewRow
}

const targetClass = 'rounded-lg border bg-card p-3 text-sm focus:outline-none focus-visible:ring-2 focus-visible:ring-ring'

// EntryReviewRow is a row of every list but duplicates: an outermost entry
// that matches the card, with its size, dates, suggestion, summary, and
// decision controls.
function EntryReviewRow({ row, section, cursorKey, onSelect }: RowProps) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const entryLink = useEntryLink()
  const sourceLabel = useSourceLabel()
  const entry = row.entry
  const key = rowKey(section, row)
  if (entry === null) {
    return null
  }
  const dates = fmt.dateSpan(...changeDates(entry)) ?? t('entry.unknownDate')
  return (
    <li
      data-review-key={key}
      tabIndex={-1}
      aria-current={key === cursorKey ? 'true' : undefined}
      onFocus={() => onSelect(key)}
      onClick={() => onSelect(key)}
      className={cn(targetClass, 'grid gap-1', key === cursorKey && 'border-primary bg-primary/5')}
    >
      <div className="flex flex-wrap items-baseline justify-between gap-x-3">
        <Link to={{ search: entryLink(entry.id) }} className="min-w-0 font-medium break-all text-primary hover:underline">
          {entry.path === '' ? sourceLabel(entry.source_id) : entry.path}
        </Link>
        <span className="font-semibold">{fmt.bytes(row.bytes)}</span>
      </div>
      <p className="text-muted-foreground">{summaryLine(row.summary, t, fmt)}</p>
      <p className="text-xs text-muted-foreground">
        {[
          sourceLabel(entry.source_id),
          dates,
          ...(entry.triage === null ? [] : [t('review.suggestion', { triage: t(`entry.triage.${entry.triage}`) })]),
        ].join(' · ')}
      </p>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span>{t('review.decision', { decision: t(`home.decision.${entry.eff_decision}`) })}</span>
        {isMember(entry) ? (
          <span className="text-muted-foreground">{t('review.memberDecision')}</span>
        ) : (
          <DecisionButtons entryId={entry.id} name={entry.name} own={entry.decision} />
        )}
      </div>
    </li>
  )
}

// DuplicatesRow is a row of the duplicates list: a folder relation or a
// group of copies of one file, which expands into its copies, each decided
// on its own (R2 design D2).
function DuplicatesRow({ row, section, cursorKey, expanded, onSelect, onToggle }: RowProps) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const entryLink = useEntryLink()
  const sourceLabel = useSourceLabel()
  const key = rowKey(section, row)
  const items = copyItems(row)
  const open = expanded.has(row.id)
  const copiesId = useId()
  const first = items[0]
  const name = first?.path.split('/').at(-1) ?? ''

  let title: string
  if (row.relation !== null && row.entry !== null) {
    title = `${row.entry.path} · ${row.relation.other.path}`
  } else {
    title = t('review.copies', { count: items.length, formatted: fmt.count(items.length), name })
  }

  return (
    <li
      data-review-key={key}
      tabIndex={-1}
      aria-current={key === cursorKey ? 'true' : undefined}
      onFocus={() => onSelect(key)}
      onClick={() => onSelect(key)}
      className={cn(targetClass, 'grid gap-2', key === cursorKey && 'border-primary bg-primary/5')}
    >
      <div className="flex flex-wrap items-baseline justify-between gap-x-3">
        <span className="min-w-0 font-medium break-all">{title}</span>
        <span className="font-semibold">{t('review.redundant', { bytes: fmt.bytes(row.bytes) })}</span>
      </div>
      <p className="text-muted-foreground">
        {row.relation !== null && `${t(`review.relation.${row.relation.kind}`)} · `}
        {summaryLine(row.summary, t, fmt)}
      </p>
      <div className="flex flex-wrap gap-2">
        <Button
          size="sm"
          variant="outline"
          aria-expanded={open}
          aria-controls={copiesId}
          onClick={(event) => {
            event.stopPropagation()
            onToggle(row)
          }}
        >
          {open ? t('review.hideCopies') : t('review.showCopies')}
        </Button>
        {row.relation !== null && row.entry !== null && (
          <Button asChild size="sm" variant="outline">
            <Link
              to={{
                pathname: '/compare',
                search: `?${new URLSearchParams({ left: row.entry.id, right: row.relation.other.id })}`,
              }}
            >
              {t('review.compare')}
            </Link>
          </Button>
        )}
      </div>
      {open && (
        <ul id={copiesId} aria-label={t('review.copiesList')} className="grid gap-2">
          {items.map((item) => {
            const itemKey = copyKey(section, row, item)
            return (
              <li
                key={item.ref}
                data-review-key={itemKey}
                tabIndex={-1}
                aria-current={itemKey === cursorKey ? 'true' : undefined}
                onFocus={(event) => {
                  event.stopPropagation()
                  onSelect(itemKey)
                }}
                onClick={(event) => {
                  event.stopPropagation()
                  onSelect(itemKey)
                }}
                className={cn(
                  'grid gap-1 rounded-md border p-2 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring',
                  itemKey === cursorKey && 'border-primary bg-primary/5',
                )}
              >
                <Link to={{ search: entryLink(item.ref) }} className="break-all text-primary hover:underline">
                  {item.path}
                </Link>
                <span className="text-xs text-muted-foreground">
                  {[
                    sourceLabel(item.sourceId),
                    ...(item.hardLink ? [t('detail.hardLink')] : []),
                    ...(item.offline ? [t('detail.offline')] : []),
                    ...(item.member ? [t('detail.inArchive')] : []),
                  ].join(' · ')}
                </span>
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span>{t('review.decision', { decision: t(`home.decision.${item.effDecision}`) })}</span>
                  {item.member ? (
                    <span className="text-muted-foreground">{t('review.memberDecision')}</span>
                  ) : (
                    <DecisionButtons entryId={item.ref} name={item.path} own={item.own} />
                  )}
                </div>
              </li>
            )
          })}
        </ul>
      )}
    </li>
  )
}
