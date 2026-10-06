import { useQuery } from '@tanstack/react-query'
import { useEffect, useId, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useNavigate, useSearchParams } from 'react-router'

import { entryQueryKey, fetchEntry, type EntryDetail } from '@/api/entries'
import { useSources } from '@/api/sources'
import { ErrorBanner } from '@/app/ErrorBanner'
import { Button } from '@/components/ui/button'
import { DecisionControls } from '@/detail/DecisionControls'
import { InsideList } from '@/detail/InsideList'
import { Preview } from '@/detail/Preview'
import { TagEditor } from '@/detail/TagEditor'
import { useEntryLink } from '@/detail/useEntryLink'
import { BarList } from '@/home/BarList'
import { en } from '@/i18n/en'
import { compositionParts, dominantFamily, shareLabel, withShare } from '@/lib/composition'
import { useFormat } from '@/lib/format'
import { familyColors } from '@/map/colors'
import { ViewerDialog } from '@/viewer/ViewerDialog'

type KnownSignal = keyof typeof en.entry.signal

// sideBySideQuery matches the windows wide enough for the panel to open
// beside the Map or Search (the 3xl breakpoint of index.css); narrower ones
// open it over them, as a drawer, so the table keeps its width (design D23).
const sideBySideQuery = '(min-width: 100rem)'

// DetailPanel shows the entry named by ?entry=<id> on the Map and Search
// screens: where it is, its sizes and dates, its breakdowns, its
// classification, its decision and tags with their controls, and its
// technical details. Without ?entry= it renders nothing.
export function DetailPanel() {
  const { t } = useTranslation()
  const [params] = useSearchParams()
  const navigate = useNavigate()
  const entryLink = useEntryLink()
  const entryId = params.get('entry')
  const headingId = useId()
  const panelRef = useRef<HTMLElement>(null)

  const detail = useQuery({
    queryKey: entryQueryKey(entryId ?? ''),
    queryFn: ({ signal }) => fetchEntry(entryId ?? '', signal),
    enabled: entryId !== null,
  })

  // Opened as a drawer, the panel takes the focus, and gives it back to
  // where it was when it closes.
  useEffect(() => {
    if (entryId === null || typeof window.matchMedia !== 'function' || window.matchMedia(sideBySideQuery).matches) {
      return
    }
    const previous = document.activeElement
    panelRef.current?.focus()
    return () => {
      if (previous instanceof HTMLElement && previous.isConnected) {
        previous.focus()
      }
    }
  }, [entryId])

  if (entryId === null) {
    return null
  }

  const close = () => void navigate({ search: entryLink(null) })
  // Escape closes the panel, but not from a dialog opened in it (the
  // viewer), which Escape closes alone.
  const closeOnEscape = (event: KeyboardEvent) => {
    if (event.key === 'Escape' && !(event.target instanceof Element && event.target.closest('dialog') !== null)) {
      close()
    }
  }

  return (
    <aside
      ref={panelRef}
      aria-labelledby={headingId}
      tabIndex={-1}
      onKeyDown={closeOnEscape}
      className="fixed inset-y-0 right-0 z-40 grid w-[26rem] max-w-full content-start gap-4 overflow-y-auto border-l bg-card p-4 shadow-2xl focus:outline-none 3xl:sticky 3xl:top-4 3xl:right-auto 3xl:bottom-auto 3xl:z-auto 3xl:max-h-[calc(100vh-2rem)] 3xl:w-auto 3xl:rounded-lg 3xl:border 3xl:shadow-none"
    >
      <div className="flex items-start justify-between gap-2">
        <h2 id={headingId} className="text-lg font-semibold break-all">
          {detail.data?.entry.name ?? t('detail.label')}
        </h2>
        <Button variant="ghost" size="sm" aria-label={t('detail.close')} onClick={close}>
          ×
        </Button>
      </div>
      {detail.isPending && (
        <p role="status" className="text-sm text-muted-foreground">
          {t('app.loading')}
        </p>
      )}
      {detail.isError && <ErrorBanner error={detail.error} onRetry={() => void detail.refetch()} />}
      {detail.data !== undefined && <DetailBody detail={detail.data} />}
    </aside>
  )
}

function DetailBody({ detail }: { detail: EntryDetail }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const entryLink = useEntryLink()
  const sources = useSources()
  const [viewing, setViewing] = useState(false)
  const { entry, ancestors, classification, intent, stats } = detail
  const folder = entry.kind === 'directory'
  const rootLabel = sources.data?.sources.find((s) => s.id === entry.source_id)?.label ?? t('entry.root')
  const parent = ancestors.at(-1)
  const date = (time: string | null) => (time === null ? t('detail.unknownDate') : fmt.dateTime(time))

  const byKind = [...(stats?.by_kind ?? [])]
    .sort((a, b) => b.bytes - a.bytes)
    .map((a) => ({ key: a.kind, label: t(`home.kind.${a.kind}`), bytes: a.bytes, files: a.files }))
  const byYear = [...(stats?.by_year ?? [])]
    .sort((a, b) => a.year - b.year)
    .map((a) => ({ key: String(a.year), label: String(a.year), bytes: a.bytes, files: a.files }))
  const parts = folder ? compositionParts(entry.composition) : []
  const compositionTotal = parts.reduce((sum, a) => sum + a.bytes, 0)
  const byFamily = parts.map((a) => ({
    key: a.family,
    label: shareLabel(a, compositionTotal, t, fmt),
    bytes: a.bytes,
    files: a.files,
    color: familyColors[a.family],
  }))
  // An unclassified entry has no family of its own: a file counts under the
  // family of its file type, and a folder under its composition (D21).
  const unclassified = classification.category === null || classification.category === 'unknown'
  const typeFamily = !folder && unclassified ? dominantFamily(entry.composition) : null
  const ownFamily = unclassified && entry.composition !== undefined ? null : classification.family

  return (
    <>
      <nav aria-label={t('detail.location')} className="text-sm">
        <ol className="flex flex-wrap items-center gap-1 text-muted-foreground">
          {ancestors.map((a, index) => (
            <li key={a.id} className="flex items-center gap-1">
              <Link to={{ search: entryLink(a.id) }} className="text-primary hover:underline">
                {a.name === '' || index === 0 ? rootLabel : a.name}
              </Link>
              <span aria-hidden="true">/</span>
            </li>
          ))}
          <li aria-current="page" className="font-medium break-all text-foreground">
            {ancestors.length === 0 ? rootLabel : entry.name}
          </li>
        </ol>
      </nav>

      <div className="flex flex-wrap gap-2">
        {entry.kind === 'file' && (
          <Button size="sm" onClick={() => setViewing(true)}>
            {t('detail.open')}
          </Button>
        )}
        <Button asChild size="sm" variant="outline">
          <Link
            to={
              folder || parent === undefined
                ? { pathname: `/map/${entry.id}`, search: `?entry=${entry.id}` }
                : { pathname: `/map/${parent.id}`, search: `?entry=${entry.id}` }
            }
          >
            {t('detail.showInMap')}
          </Link>
        </Button>
        {folder && (
          <Button asChild size="sm" variant="outline">
            <Link to={{ pathname: '/search', search: `?within=${entry.id}` }}>{t('detail.searchHere')}</Link>
          </Button>
        )}
      </div>

      {entry.kind === 'file' && <Preview entry={entry} onOpen={() => setViewing(true)} />}

      {entry.state === 'missing' && <Notice>{t('detail.missing')}</Notice>}
      {entry.state === 'unreadable' && <Notice>{t('detail.unreadableState')}</Notice>}
      {entry.partial && entry.state !== 'unreadable' && <Notice>{t('detail.partial')}</Notice>}

      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
        <Fact label={t('detail.kind')}>
          {t(`entry.kind.${entry.kind}`)}
          {entry.file_kind !== null && ` · ${t(`entry.fileKind.${entry.file_kind}`)}`}
        </Fact>
        <Fact label={t('detail.size')}>{fmt.bytes(entry.total_bytes)}</Fact>
        {folder && <Fact label={t('detail.files')}>{fmt.count(entry.total_files)}</Fact>}
        {stats !== null && <Fact label={t('detail.folders')}>{fmt.count(stats.dirs)}</Fact>}
        <Fact label={t('detail.modified')}>{date(entry.mtime)}</Fact>
        {folder && <Fact label={t('detail.newest')}>{date(entry.newest)}</Fact>}
        {folder && <Fact label={t('detail.oldest')}>{date(entry.oldest)}</Fact>}
        {stats !== null && stats.unreadable > 0 && (
          <Fact label={t('detail.unreadable')}>{fmt.count(stats.unreadable)}</Fact>
        )}
        {stats !== null && stats.mount_boundaries > 0 && (
          <Fact label={t('detail.mountBoundaries')}>{fmt.count(stats.mount_boundaries)}</Fact>
        )}
      </dl>

      {byFamily.length > 0 && (
        <Section title={t('detail.composition')}>
          <BarList label={t('detail.composition')} bars={byFamily} whole={compositionTotal} />
        </Section>
      )}
      {stats?.inside !== undefined && (
        <Section title={t('detail.inside')}>
          <InsideList folder={entry} items={stats.inside} label={t('detail.inside')} />
        </Section>
      )}

      {stats !== null && (
        <>
          <Section title={t('detail.byKind')}>
            <BarList label={t('detail.byKind')} bars={byKind} />
          </Section>
          <Section title={t('detail.byYear')}>
            <BarList label={t('detail.byYear')} bars={byYear} />
          </Section>
        </>
      )}

      <Section title={t('detail.classification')}>
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
          <Fact label={t('detail.category')}>
            {withShare(t(`entry.category.${classification.category ?? 'unknown'}`), entry.composition, t, fmt)}
          </Fact>
          {typeFamily !== null && (
            <Fact label={t('detail.family')}>
              {t('detail.familyByType', { family: t(`home.family.${typeFamily}`) })}
            </Fact>
          )}
          {ownFamily !== null && <Fact label={t('detail.family')}>{t(`home.family.${ownFamily}`)}</Fact>}
          {classification.triage !== null && (
            <Fact label={t('detail.suggestion')}>{t(`entry.triage.${classification.triage}`)}</Fact>
          )}
        </dl>
        {classification.group && <p className="text-sm">{t('detail.group')}</p>}
        {classification.traits.length > 0 && (
          <ul aria-label={t('detail.traits')} className="flex flex-wrap gap-1 text-xs">
            {classification.traits.map((trait) => (
              <li key={trait} className="rounded-full bg-secondary px-2 py-0.5">
                {t(`entry.trait.${trait}`)}
              </li>
            ))}
          </ul>
        )}
        <div className="grid gap-1 text-sm">
          <h4 className="font-medium">{t('detail.rules')}</h4>
          {classification.rules.length === 0 ? (
            <p className="text-muted-foreground">{t('detail.noRules')}</p>
          ) : (
            <ul aria-label={t('detail.rules')} className="list-disc pl-5">
              {classification.rules.map((rule) => (
                <li key={rule.id}>{rule.explain}</li>
              ))}
            </ul>
          )}
        </div>
        {classification.veto && (
          <div className="grid gap-1 rounded-md border border-amber-300 bg-amber-50 p-3 text-sm">
            <p className="font-medium">{t('detail.veto')}</p>
            {classification.indicators.length > 0 && (
              <ul aria-label={t('detail.indicators')} className="grid gap-1">
                {classification.indicators.map((indicator) => (
                  <li key={indicator.entry_id}>
                    <Link to={{ search: entryLink(indicator.entry_id) }} className="break-all text-primary underline">
                      {indicator.path}
                    </Link>{' '}
                    <span className="text-muted-foreground">
                      (
                      {Object.hasOwn(en.entry.signal, indicator.signal)
                        ? t(`entry.signal.${indicator.signal as KnownSignal}`)
                        : t('entry.signal.other')}
                      )
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
      </Section>

      <Section title={t('detail.decision')}>
        <DecisionControls entry={entry} intent={intent} rootLabel={rootLabel} />
      </Section>

      <Section title={t('detail.tags')}>
        <TagEditor entryId={entry.id} tags={intent.tags} rootLabel={rootLabel} />
      </Section>

      <details className="text-sm">
        <summary className="cursor-pointer font-medium">{t('detail.technical')}</summary>
        <dl className="mt-2 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1">
          <Fact label={t('detail.id')}>{entry.id}</Fact>
          <Fact label={t('detail.source')}>{entry.source_id}</Fact>
          <Fact label={t('detail.rawName')}>
            <span className="font-mono text-xs break-all">{hexBytes(entry.name_b64)}</span>
          </Fact>
          <Fact label={t('detail.rawPath')}>
            <span className="font-mono text-xs break-all">{entry.path_b64}</span>
          </Fact>
          <Fact label={t('detail.state')}>{entry.state}</Fact>
          <Fact label={t('detail.mountBoundary')}>{entry.mount_boundary ? t('detail.yes') : t('detail.no')}</Fact>
          {classification.rules.length > 0 && (
            <Fact label={t('detail.ruleIds')}>
              <span className="font-mono text-xs">{classification.rules.map((rule) => rule.id).join(', ')}</span>
            </Fact>
          )}
        </dl>
      </details>

      {viewing && <ViewerDialog entry={entry} onClose={() => setViewing(false)} />}
    </>
  )
}

// hexBytes shows base64 bytes as hexadecimal pairs.
function hexBytes(b64: string): string {
  return Array.from(atob(b64), (c) => c.charCodeAt(0).toString(16).padStart(2, '0')).join(' ')
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  const headingId = useId()
  return (
    <section aria-labelledby={headingId} className="grid gap-2 border-t pt-3">
      <h3 id={headingId} className="text-sm font-semibold">
        {title}
      </h3>
      {children}
    </section>
  )
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd>{children}</dd>
    </>
  )
}

function Notice({ children }: { children: ReactNode }) {
  return (
    <p role="status" className="rounded-md border border-amber-300 bg-amber-50 p-2 text-sm">
      {children}
    </p>
  )
}
