import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { useId, useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { Link, useSearchParams } from 'react-router'

import {
  camerasQueryKey,
  dateFlags,
  dateSources,
  datesListQueryKey,
  fetchCameras,
  fetchDates,
  type DatesFilter,
  type MediaDate,
} from '@/api/dates'
import { maxBulkIds } from '@/api/decisions'
import { entryQueryKey, fetchEntry } from '@/api/entries'
import type { Source } from '@/api/sources'
import { ErrorBanner } from '@/app/ErrorBanner'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { CorrectionDialog } from '@/dates/CorrectionDialog'
import { OrganizeByDateDialog, SetFileDatesDialog } from '@/dates/DatePlanDialogs'
import type { ChosenFolder } from '@/dates/targets'
import { useDatesText } from '@/dates/text'
import { useEntryLink } from '@/detail/useEntryLink'
import { useFormat } from '@/lib/format'
import { FolderChooser } from '@/organize/FolderChooser'
import { OrganizeOutcome } from '@/organize/OrganizeOutcome'
import { useOrganize } from '@/organize/useOrganize'

// FilterParam is a filter of the list, in the address with the API's name.
type FilterParam = 'flag' | 'date_source' | 'camera' | 'within'

type Dialogs = 'correct' | 'setFileDates' | 'organize' | null

// DatesList is the list of one source's photos and videos (R5 design D10),
// paged by the server: each with its date, where it comes from and how
// sure it is, its metadata state when not read, its flags, its camera, its
// modification time, and the owner's correction, filtered by flag, source
// of date, camera, and folder. Items are selected on the page, then
// corrected, or their file dates set or organized by date after a preview.
export function DatesList({ source }: { source: Source }) {
  const { t } = useTranslation()
  const [params, setParams] = useSearchParams()
  const headingId = useId()
  const filter: DatesFilter = {
    source: source.id,
    flag: dateFlags.find((flag) => flag === params.get('flag')) ?? null,
    dateSource: dateSources.find((s) => s === params.get('date_source')) ?? null,
    camera: params.get('camera'),
    within: params.get('within'),
  }
  const [choosing, setChoosing] = useState(false)

  const setFilter = (name: FilterParam, value: string | null) => {
    const next = new URLSearchParams(params)
    if (value === null || value === '') {
      next.delete(name)
    } else {
      next.set(name, value)
    }
    next.set('source', source.id)
    setParams(next)
  }

  const cameras = useQuery({
    queryKey: camerasQueryKey(source.id),
    queryFn: ({ signal }) => fetchCameras(source.id, signal),
  })
  const within = useQuery({
    queryKey: entryQueryKey(filter.within ?? ''),
    queryFn: ({ signal }) => fetchEntry(filter.within ?? '', signal),
    enabled: filter.within !== null,
  })
  const withinFolder: ChosenFolder | null =
    filter.within === null
      ? null
      : {
          id: filter.within,
          // Named from the source, as the folder chooser's trail.
          path:
            within.data === undefined
              ? filter.within
              : [source.label, ...within.data.entry.path.split('/').filter((part) => part !== '')].join(' / '),
        }

  return (
    <Card className="p-4">
      <section aria-labelledby={headingId} className="grid gap-3 text-sm">
        <h2 id={headingId} className="text-lg font-semibold">
          {t('dates.list.title')}
        </h2>
        <div className="flex flex-wrap items-end gap-3">
          <FilterSelect
            label={t('dates.list.flag')}
            value={filter.flag ?? ''}
            onChange={(value) => setFilter('flag', value)}
            options={[
              ['', t('dates.list.anyFlag')],
              ...dateFlags.map((flag): [string, string] => [flag, t(`dates.flag.${flag}`)]),
            ]}
          />
          <FilterSelect
            label={t('dates.list.dateSource')}
            value={filter.dateSource ?? ''}
            onChange={(value) => setFilter('date_source', value)}
            options={[
              ['', t('dates.list.anySource')],
              ...dateSources.map((s): [string, string] => [s, t(`dates.source.${s}`)]),
            ]}
          />
          <CameraFilter
            value={filter.camera ?? ''}
            cameras={cameras.data?.items ?? []}
            onChange={(value) => setFilter('camera', value)}
          />
          {filter.within === null && (
            <Button size="sm" variant="outline" onClick={() => setChoosing(true)}>
              {t('dates.list.chooseFolder')}
            </Button>
          )}
        </div>
        {withinFolder !== null && (
          <p className="flex flex-wrap items-center gap-2 rounded-md border bg-card px-3 py-2">
            <span className="break-all">
              <Trans i18nKey="dates.list.within" values={{ path: withinFolder.path }} components={{ strong: <strong /> }} />
            </span>
            <Button variant="outline" size="sm" onClick={() => setFilter('within', null)}>
              {t('dates.list.everywhere')}
            </Button>
          </p>
        )}
        {/* Keyed by the filters: other filters start with nothing selected. */}
        <DatesRows key={datesListQueryKey(filter).join('\u0000')} source={source} filter={filter} within={withinFolder} />
      </section>
      {choosing && (
        <FolderChooser
          title={t('dates.list.folderTitle')}
          sourceIds={[source.id]}
          chooseLabel={t('dates.list.folderHere')}
          onChoose={(folder) => {
            setChoosing(false)
            setFilter('within', folder.id)
          }}
          onClose={() => setChoosing(false)}
        />
      )}
    </Card>
  )
}

function FilterSelect({
  label,
  value,
  options,
  onChange,
}: {
  label: string
  value: string
  options: Array<[string, string]>
  onChange: (value: string) => void
}) {
  const id = useId()
  return (
    <div className="grid gap-1">
      <Label htmlFor={id}>{label}</Label>
      <select
        id={id}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        className="h-9 rounded-md border border-input bg-card px-2 text-sm"
      >
        {options.map(([optionValue, optionLabel]) => (
          <option key={optionValue} value={optionValue}>
            {optionLabel}
          </option>
        ))}
      </select>
    </div>
  )
}

function CameraFilter({
  value,
  cameras,
  onChange,
}: {
  value: string
  cameras: Array<{ key: string; make: string | null; model: string | null; serial: string | null }>
  onChange: (value: string) => void
}) {
  const { t } = useTranslation()
  const text = useDatesText()
  const options: Array<[string, string]> = [
    ['', t('dates.list.anyCamera')],
    ...cameras.map((camera): [string, string] => [camera.key, text.camera(camera)]),
  ]
  // A camera in the address that the list no longer has still shows.
  if (value !== '' && !cameras.some((camera) => camera.key === value)) {
    options.push([value, value])
  }
  return <FilterSelect label={t('dates.list.camera')} value={value} options={options} onChange={onChange} />
}

// DatesRows lists the rows of one filter, with the selection and the
// actions on it.
function DatesRows({ source, filter, within }: { source: Source; filter: DatesFilter; within: ChosenFolder | null }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const [selected, setSelected] = useState<ReadonlySet<string>>(() => new Set())
  const [dialog, setDialog] = useState<Dialogs>(null)
  const organize = useOrganize()

  const pages = useInfiniteQuery({
    queryKey: datesListQueryKey(filter),
    queryFn: ({ pageParam, signal }) => fetchDates(filter, pageParam, signal),
    initialPageParam: null as string | null,
    getNextPageParam: (page) => page.next_cursor,
  })
  const rows = pages.data?.pages.flatMap((page) => page.items) ?? []
  // A selected row that left the list is no longer selected.
  const chosen = rows.filter((row) => selected.has(row.entry.id)).map((row) => row.entry.id)
  const writable = source.writes.enabled && source.state === 'online'

  const toggle = (id: string) =>
    setSelected((current) => {
      const next = new Set(current)
      if (next.has(id)) {
        next.delete(id)
      } else {
        next.add(id)
      }
      return next
    })

  const planProps = { organize, selected: chosen, within, sourceId: source.id, onClose: () => setDialog(null) }

  return (
    <div className="grid gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium">
          {t('dates.list.selected', { count: chosen.length, formatted: fmt.count(chosen.length) })}
        </span>
        <Button
          size="sm"
          variant="ghost"
          disabled={rows.length === 0}
          onClick={() => setSelected(new Set(rows.map((row) => row.entry.id)))}
        >
          {t('dates.list.selectShown')}
        </Button>
        {chosen.length > 0 && (
          <Button size="sm" variant="ghost" onClick={() => setSelected(new Set())}>
            {t('dates.list.clear')}
          </Button>
        )}
        <Button size="sm" variant="outline" onClick={() => setDialog('correct')}>
          {t('dates.actions.correct')}
        </Button>
        <Button
          size="sm"
          variant="outline"
          disabled={!writable || organize.pending}
          onClick={() => setDialog('setFileDates')}
        >
          {t('dates.actions.setFileDates')}
        </Button>
        <Button size="sm" variant="outline" disabled={!writable || organize.pending} onClick={() => setDialog('organize')}>
          {t('dates.actions.organize')}
        </Button>
      </div>
      {!source.writes.enabled && (
        <p className="text-muted-foreground">
          <Trans
            i18nKey="dates.actions.writesOff"
            components={{ sourcesLink: <Link to="/sources" className="text-primary underline" /> }}
          />
        </p>
      )}
      {source.writes.enabled && source.state !== 'online' && (
        <p className="text-muted-foreground">{t('dates.actions.offline')}</p>
      )}
      {chosen.length > maxBulkIds && (
        <p className="text-muted-foreground">{t('dates.list.tooMany', { formatted: fmt.count(maxBulkIds) })}</p>
      )}
      {organize.pending && (
        <p role="status" className="text-muted-foreground">
          {t('dates.setFileDates.planning')}
        </p>
      )}
      <OrganizeOutcome organize={organize} />

      {pages.isPending && (
        <p role="status" className="text-muted-foreground">
          {t('app.loading')}
        </p>
      )}
      {pages.isError && <ErrorBanner error={pages.error} onRetry={() => void pages.refetch()} />}
      {pages.data !== undefined && rows.length === 0 && <p className="text-muted-foreground">{t('dates.list.empty')}</p>}
      {rows.length > 0 && (
        <ul aria-label={t('dates.list.label')} className="grid gap-2">
          {rows.map((row) => (
            <DateRow
              key={row.entry.id}
              row={row}
              selected={selected.has(row.entry.id)}
              onToggle={() => toggle(row.entry.id)}
            />
          ))}
        </ul>
      )}
      {pages.hasNextPage && (
        <div className="flex justify-center">
          <Button
            variant="outline"
            size="sm"
            disabled={pages.isFetchingNextPage}
            onClick={() => void pages.fetchNextPage()}
          >
            {pages.isFetchingNextPage ? t('map.loadingMore') : t('map.loadMore')}
          </Button>
        </div>
      )}
      {pages.isFetchNextPageError && <ErrorBanner error={pages.error} />}

      {dialog === 'correct' && (
        <CorrectionDialog
          title={t('dates.correct.title')}
          scope={{ kind: 'bulk', selected: chosen, within, sourceId: source.id }}
          onClose={() => setDialog(null)}
        />
      )}
      {dialog === 'setFileDates' && <SetFileDatesDialog {...planProps} />}
      {dialog === 'organize' && <OrganizeByDateDialog {...planProps} />}
    </div>
  )
}

// DateRow is one photo or video: its name, which opens its details, its
// folder, its date with where it comes from and how sure it is, its
// metadata state when not read, its flags, its camera, its modification
// time, and the owner's correction.
function DateRow({ row, selected, onToggle }: { row: MediaDate; selected: boolean; onToggle: () => void }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const text = useDatesText()
  const entryLink = useEntryLink()
  const { entry, date } = row
  const sourceText = [
    t(`dates.source.${date.source}`),
    ...(date.refined ? [t('dates.refined')] : []),
    ...(date.corrected === null ? [] : [t(`dates.corrected.${date.corrected}`)]),
  ].join(', ')

  return (
    <li className="grid gap-1 rounded-md border bg-card p-2">
      <div className="flex items-start gap-2">
        <input
          type="checkbox"
          className="mt-1"
          aria-label={t('dates.list.select', { name: entry.path })}
          checked={selected}
          onChange={onToggle}
        />
        <div className="grid min-w-0 gap-0.5">
          <Link to={{ search: entryLink(entry.id) }} className="font-medium break-all text-primary hover:underline">
            {entry.path}
          </Link>
          <p>
            <span className="font-medium">{text.date(date)}</span>
            {' · '}
            {sourceText}
            {' · '}
            {t(`dates.confidence.${date.confidence}`)}
          </p>
          <p className="text-muted-foreground">
            {[
              ...(row.metadata === 'read' ? [] : [t(`dates.metadata.${row.metadata}`)]),
              ...(row.camera === null ? [] : [text.camera(row.camera)]),
              entry.mtime === null
                ? t('dates.list.modifiedUnknown')
                : t('dates.list.modified', { time: fmt.instant(entry.mtime) }),
              ...(row.correction === null ? [] : [text.correction(row.correction)]),
            ].join(' · ')}
          </p>
          {row.flags.length > 0 && (
            <ul aria-label={t('dates.panel.flags')} className="flex flex-wrap gap-1 text-xs">
              {row.flags.map((flag) => (
                <li key={flag} className="rounded-full bg-amber-100 px-2 py-0.5 font-medium text-amber-900">
                  {t(`dates.flag.${flag}`)}
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
    </li>
  )
}
