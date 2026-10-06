import { useInfiniteQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useCallback, useId, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useSearchParams, type To } from 'react-router'

import {
  createSelection,
  decisionChoices,
  maxBulkIds,
  refreshAfterDecision,
  setDecision,
  type DecisionChoice,
  type DecisionTargets,
  type Selection,
} from '@/api/decisions'
import { childSorts, type ChildSort, type EntryRow, type SortOrder } from '@/api/entries'
import { fetchSearch, searchQueryKey, selectionQuery } from '@/api/search'
import { refreshAfterTagChange, setTags, useTags, type TagTargets } from '@/api/tags'
import { ApiError } from '@/app/api'
import { ErrorBanner } from '@/app/ErrorBanner'
import { useCsrfToken } from '@/app/session'
import { BulkReport, SelectAllDialog, type Report } from '@/components/BulkSelection'
import { EntryTable } from '@/components/EntryTable'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { useFormat } from '@/lib/format'

// countCap is the largest exact match count the server reports.
const countCap = 10_000

// SearchResults lists the matches of one search, virtualized and paged by
// cursor, and applies decisions and tags to the rows picked one by one or to
// every result through a selection, which the owner confirms after seeing
// its count, bytes, and kept entries. It is keyed by the search, so a new
// search starts with nothing selected.
export function SearchResults({ filters }: { filters: URLSearchParams }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const queryClient = useQueryClient()
  const csrfToken = useCsrfToken()
  const [params, setParams] = useSearchParams()
  const [picked, setPicked] = useState<Set<string>>(() => new Set())
  const [selection, setSelection] = useState<Selection | null>(null)
  const [proposal, setProposal] = useState<Selection | null>(null)
  const [report, setReport] = useState<Report | null>(null)
  const tags = useTags()
  const tagSelectId = useId()
  const [tagChoice, setTagChoice] = useState('')

  const sortParam = filters.get('sort')
  const sort: ChildSort = childSorts.find((s) => s === sortParam) ?? 'bytes'
  const orderParam = filters.get('order')
  const order: SortOrder =
    orderParam === 'asc' || orderParam === 'desc' ? orderParam : sort === 'name' ? 'asc' : 'desc'

  const results = useInfiniteQuery({
    queryKey: searchQueryKey(filters),
    queryFn: ({ pageParam, signal }) => fetchSearch(filters, pageParam, signal),
    initialPageParam: null as string | null,
    getNextPageParam: (page) => page.next_cursor,
  })
  const rows = useMemo(() => results.data?.pages.flatMap((page) => page.items) ?? [], [results.data])
  const count = results.data?.pages[0]?.count
  const { fetchNextPage, hasNextPage, isFetchingNextPage } = results
  const loadMore = useCallback(() => void fetchNextPage(), [fetchNextPage])

  const clearSelection = () => {
    setPicked(new Set())
    setSelection(null)
  }

  const tableSelection = useMemo(
    () => ({
      isSelected: (id: string) => selection !== null || picked.has(id),
      toggle: (row: EntryRow) => {
        // Changing one row leaves "all results" for a choice row by row.
        const next = new Set(selection === null ? picked : [])
        if (next.has(row.id)) {
          next.delete(row.id)
        } else {
          next.add(row.id)
        }
        setSelection(null)
        setPicked(next)
      },
    }),
    [picked, selection],
  )
  const folderLink = useCallback((row: EntryRow): To => ({ pathname: `/map/${row.id}` }), [])
  const sortBy = (key: ChildSort) => {
    const next = new URLSearchParams(params)
    if (key === sort) {
      next.set('order', order === 'asc' ? 'desc' : 'asc')
    } else {
      next.set('sort', key)
      next.set('order', key === 'name' ? 'asc' : 'desc')
    }
    setParams(next)
  }

  const selectAll = useMutation({
    mutationFn: () => createSelection(selectionQuery(filters), csrfToken),
    onSuccess: (created) => setProposal(created),
  })

  // A bulk request names the selection, or the rows picked one by one.
  const targets: TagTargets | null =
    selection !== null
      ? { selection_id: selection.selection_id }
      : picked.size > 0 && picked.size <= maxBulkIds
        ? { entry_ids: [...picked] }
        : null

  const onBulkError = (error: Error) => {
    if (error instanceof ApiError && error.code === 'selection_expired') {
      setSelection(null)
    }
  }
  const decide = useMutation({
    mutationFn: ({ to, choice }: { to: DecisionTargets; choice: DecisionChoice }) =>
      setDecision(to, choice, csrfToken),
    onSuccess: async (result) => {
      setReport({ kind: 'decision', result })
      clearSelection()
      await refreshAfterDecision(queryClient)
    },
    onError: onBulkError,
  })
  const tag = useMutation({
    mutationFn: ({ to, change }: { to: TagTargets; change: { add?: number[]; remove?: number[] } }) =>
      setTags(to, change, csrfToken),
    onSuccess: async (result) => {
      setReport({ kind: 'tags', result })
      clearSelection()
      setTagChoice('')
      await refreshAfterTagChange(queryClient)
    },
    onError: onBulkError,
  })
  const busy = decide.isPending || tag.isPending

  let countText = ''
  if (count === '10000+') {
    countText = t('search.countCapped', { formatted: fmt.count(countCap) })
  } else if (count !== undefined) {
    countText = t('search.count', { count, formatted: fmt.count(count) })
  }

  return (
    <section aria-label={t('search.results')} className="grid min-w-0 gap-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p role="status" className="text-sm font-medium">
          {countText}
        </p>
        {rows.length > 0 && (
          <Button variant="outline" size="sm" disabled={selectAll.isPending} onClick={() => selectAll.mutate()}>
            {selectAll.isPending ? t('search.selectingAll') : t('search.selectAll')}
          </Button>
        )}
      </div>
      {selectAll.isError && <ErrorBanner error={selectAll.error} onDismiss={() => selectAll.reset()} />}

      {(picked.size > 0 || selection !== null) && (
        <section aria-label={t('search.bulk')} className="grid gap-3 rounded-lg border bg-card p-3 text-sm">
          <div className="flex flex-wrap items-center gap-3">
            <span className="font-medium">
              {selection !== null
                ? t('search.allSelected', {
                    count: selection.count,
                    formatted: fmt.count(selection.count),
                    bytes: fmt.bytes(selection.bytes),
                  })
                : t('search.selected', { count: picked.size, formatted: fmt.count(picked.size) })}
            </span>
            <Button variant="ghost" size="sm" onClick={clearSelection}>
              {t('search.clearSelection')}
            </Button>
          </div>
          {targets === null && <p role="alert">{t('search.tooMany', { formatted: fmt.count(maxBulkIds) })}</p>}
          <div role="group" aria-label={t('search.bulkDecision')} className="flex flex-wrap items-center gap-1">
            <span className="mr-2">{t('search.bulkDecision')}</span>
            {decisionChoices.map((choice) => (
              <Button
                key={choice}
                size="sm"
                variant="outline"
                disabled={busy || targets === null}
                onClick={() => targets !== null && decide.mutate({ to: targets, choice })}
              >
                {t(`entry.decisionChoice.${choice}`)}
              </Button>
            ))}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Label htmlFor={tagSelectId}>{t('search.bulkTag')}</Label>
            <select
              id={tagSelectId}
              value={tagChoice}
              onChange={(event) => setTagChoice(event.target.value)}
              className="h-8 rounded-md border border-input bg-card px-2"
            >
              <option value="">{t('detail.noTagChoice')}</option>
              {tags.data?.tags.map((option) => (
                <option key={option.id} value={option.id}>
                  {option.name}
                </option>
              ))}
            </select>
            <Button
              size="sm"
              variant="outline"
              disabled={busy || targets === null || tagChoice === ''}
              onClick={() => targets !== null && tag.mutate({ to: targets, change: { add: [Number(tagChoice)] } })}
            >
              {t('search.addTag')}
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={busy || targets === null || tagChoice === ''}
              onClick={() =>
                targets !== null && tag.mutate({ to: targets, change: { remove: [Number(tagChoice)] } })
              }
            >
              {t('search.removeTag')}
            </Button>
          </div>
        </section>
      )}
      {/* Outside the bulk section: an expired selection is dropped with it. */}
      {decide.isError && <ErrorBanner error={decide.error} onDismiss={() => decide.reset()} />}
      {tag.isError && <ErrorBanner error={tag.error} onDismiss={() => tag.reset()} />}

      {report !== null && <BulkReport report={report} onClose={() => setReport(null)} />}

      {results.isPending && (
        <p role="status" className="text-sm text-muted-foreground">
          {t('app.loading')}
        </p>
      )}
      {results.isError && <ErrorBanner error={results.error} onRetry={() => void results.refetch()} />}
      {results.data !== undefined && (
        <EntryTable
          label={t('search.results')}
          rows={rows}
          rowCount={typeof count === 'number' ? count : undefined}
          emptyText={t('search.noResults')}
          sort={{ sort, order, onSort: sortBy }}
          folderLink={folderLink}
          selectedId={params.get('entry')}
          selection={tableSelection}
          hasMore={hasNextPage}
          loadingMore={isFetchingNextPage}
          onLoadMore={loadMore}
        />
      )}

      {proposal !== null && (
        <SelectAllDialog
          title={t('search.confirmTitle')}
          selection={proposal}
          onConfirm={() => {
            setPicked(new Set())
            setSelection(proposal)
            setProposal(null)
          }}
          onCancel={() => setProposal(null)}
        />
      )}
    </section>
  )
}
