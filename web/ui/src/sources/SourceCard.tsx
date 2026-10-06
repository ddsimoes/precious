import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useId, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'

import { homeQueryRoot } from '@/api/home'
import {
  removeSource,
  renameSource,
  sourcesQueryKey,
  startScan,
  updateSource,
  type Capabilities,
  type Source,
  type SourcesResponse,
} from '@/api/sources'
import { ErrorBanner } from '@/app/ErrorBanner'
import { ScanProgress } from '@/app/ScanProgress'
import { useCsrfToken } from '@/app/session'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Dialog } from '@/components/ui/dialog'
import { FormControl, FormField, FormLabel } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import { useFormat } from '@/lib/format'
import { cn } from '@/lib/utils'

const stateStyles: Record<Source['state'], string> = {
  online: 'bg-emerald-100 text-emerald-900',
  offline: 'bg-muted text-muted-foreground',
  unavailable: 'bg-amber-100 text-amber-900',
}

// SourceCard shows one source with its state, volume, file system, totals,
// and scan, and its commands: Scan now, rename, and remove.
export function SourceCard({ source }: { source: Source }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const queryClient = useQueryClient()
  const csrfToken = useCsrfToken()
  const headingId = useId()
  const [renaming, setRenaming] = useState(false)
  const [confirmingRemove, setConfirmingRemove] = useState(false)

  const scan = useMutation({
    mutationFn: () => startScan(source.id, csrfToken),
    onSuccess: (result) => {
      // The event stream brings the progress; until then the accepted job is
      // the active scan.
      updateSource(queryClient, source.id, (s) =>
        s.active_job === null
          ? { ...s, active_job: { job_id: result.job_id, state: result.state, progress: {} } }
          : s,
      )
    },
  })

  const online = source.state === 'online'
  const disk =
    source.volume.label === null || source.volume.label === ''
      ? t('sources.diskNoLabel', { fsType: source.volume.fs_type })
      : t('sources.diskWithLabel', { label: source.volume.label, fsType: source.volume.fs_type })
  // Where the source's folder is now; without a connected disk, the folder
  // inside the disk, named by its label (or identity).
  const location =
    source.path ??
    t('sources.locationOffline', {
      folder: source.rel_root === '' ? '/' : source.rel_root,
      volume: source.volume.label === null || source.volume.label === '' ? source.volume.id : source.volume.label,
    })

  return (
    <Card className="grid gap-4 p-4">
      <article aria-labelledby={headingId} className="grid gap-4">
        <header className="flex flex-wrap items-center gap-3">
          <h2 id={headingId} className="text-lg font-semibold break-all">
            {source.label}
          </h2>
          <span className={cn('rounded-full px-2 py-0.5 text-xs font-medium', stateStyles[source.state])}>
            {t(`sources.state.${source.state}`)}
          </span>
        </header>
        {source.state !== 'online' && (
          <p className="text-sm text-muted-foreground">
            {t(`sources.stateHelp.${source.state}`)}
            {source.state_reason !== null && source.state_reason !== '' && (
              <span className="block font-mono text-xs break-all">{source.state_reason}</span>
            )}
          </p>
        )}

        <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-[max-content_1fr]">
          <dt className="text-muted-foreground">{t('sources.location')}</dt>
          <dd className="break-all">{location}</dd>
          <dt className="text-muted-foreground">{t('sources.disk')}</dt>
          <dd>{disk}</dd>
          <dt className="text-muted-foreground">{t('sources.recognition')}</dt>
          <dd>{source.volume.strong ? t('sources.strong') : t('sources.weak')}</dd>
          <dt className="text-muted-foreground">{t('sources.size')}</dt>
          <dd>{fmt.bytes(source.totals.bytes)}</dd>
          <dt className="text-muted-foreground">{t('sources.files')}</dt>
          <dd>{fmt.count(source.totals.files)}</dd>
          <dt className="text-muted-foreground">{t('sources.folders')}</dt>
          <dd>{fmt.count(source.totals.dirs)}</dd>
          <dt className="text-muted-foreground">{t('sources.lastScan')}</dt>
          <dd>
            {source.last_scan_at === null ? t('sources.neverScanned') : fmt.dateTime(source.last_scan_at)}
          </dd>
        </dl>

        <CapabilityList capabilities={source.capabilities} />

        {source.active_job !== null && (
          <section className="grid gap-1 rounded-md border p-3">
            <h3 className="text-sm font-semibold">{t('sources.activeScan')}</h3>
            <ScanProgress
              label={t('sources.activeScan')}
              state={source.active_job.state}
              progress={source.active_job.progress}
            />
          </section>
        )}

        {scan.isError && <ErrorBanner error={scan.error} onDismiss={() => scan.reset()} />}

        <div className="flex flex-wrap items-center gap-2">
          <Button
            disabled={!online || source.active_job !== null || scan.isPending}
            onClick={() => scan.mutate()}
          >
            {scan.isPending ? t('sources.scanStarting') : t('sources.scanNow')}
          </Button>
          <Button variant="outline" asChild>
            <Link to={`/map/${source.root_entry_id}`}>{t('sources.openInMap')}</Link>
          </Button>
          <Button variant="outline" onClick={() => setRenaming(true)} disabled={renaming}>
            {t('sources.rename')}
          </Button>
          <Button variant="outline" onClick={() => setConfirmingRemove(true)}>
            {t('sources.remove')}
          </Button>
          {!online && <p className="text-sm text-muted-foreground">{t('sources.scanNeedsDisk')}</p>}
        </div>

        {renaming && <RenameForm source={source} onDone={() => setRenaming(false)} />}
      </article>
      {confirmingRemove && (
        <RemoveDialog source={source} onClose={() => setConfirmingRemove(false)} />
      )}
    </Card>
  )
}

// CapabilityList explains the file system's behavior in plain words.
function CapabilityList({ capabilities: c }: { capabilities: Capabilities }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const lines = [
    !c.known && t('sources.capabilities.unknown'),
    c.read_only ? t('sources.capabilities.readOnly') : t('sources.capabilities.writable'),
    c.case_sensitive ? t('sources.capabilities.caseSensitive') : t('sources.capabilities.caseInsensitive'),
    !c.normalization_sensitive && t('sources.capabilities.normalizationInsensitive'),
    c.stable_identity ? t('sources.capabilities.stableIdentity') : t('sources.capabilities.unstableIdentity'),
    c.local_time && t('sources.capabilities.localTime'),
    c.hard_links && t('sources.capabilities.hardLinks'),
    t('sources.capabilities.precision', { precision: fmt.timePrecision(c.time_resolution_ns) }),
  ].filter((line) => line !== false)

  return (
    <ul aria-label={t('sources.fileSystem')} className="grid list-disc gap-1 pl-5 text-sm">
      {lines.map((line) => (
        <li key={line}>{line}</li>
      ))}
    </ul>
  )
}

function RenameForm({ source, onDone }: { source: Source; onDone: () => void }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const csrfToken = useCsrfToken()
  const [label, setLabel] = useState(source.label)
  const trimmed = label.trim()

  const rename = useMutation({
    mutationFn: () => renameSource(source.id, trimmed, csrfToken),
    onSuccess: (result) => {
      updateSource(queryClient, source.id, () => result.source)
      onDone()
    },
  })

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    rename.mutate()
  }

  return (
    <form className="grid max-w-md gap-3" onSubmit={submit}>
      <FormField>
        <FormLabel>{t('sources.renameLabel')}</FormLabel>
        <FormControl>
          <Input name="label" value={label} onChange={(event) => setLabel(event.target.value)} />
        </FormControl>
      </FormField>
      {rename.isError && <ErrorBanner error={rename.error} />}
      <div className="flex gap-2">
        <Button type="submit" disabled={trimmed === '' || rename.isPending}>
          {rename.isPending ? t('sources.saving') : t('sources.save')}
        </Button>
        <Button type="button" variant="outline" onClick={onDone}>
          {t('sources.cancel')}
        </Button>
      </div>
    </form>
  )
}

function RemoveDialog({ source, onClose }: { source: Source; onClose: () => void }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const csrfToken = useCsrfToken()

  const remove = useMutation({
    mutationFn: () => removeSource(source.id, csrfToken),
    onSuccess: () => {
      queryClient.setQueryData<SourcesResponse>(sourcesQueryKey, (data) =>
        data && { sources: data.sources.filter((s) => s.id !== source.id) },
      )
      // Home's figures included this source.
      void queryClient.invalidateQueries({ queryKey: homeQueryRoot })
    },
  })

  return (
    <Dialog
      alert
      title={t('sources.removeTitle', { label: source.label })}
      description={t('sources.removeBody')}
      onClose={onClose}
    >
      {source.active_job !== null && <p className="text-sm">{t('sources.removeStopsScan')}</p>}
      {remove.isError && <ErrorBanner error={remove.error} />}
      <div className="flex justify-end gap-2">
        <Button variant="outline" onClick={onClose}>
          {t('sources.cancel')}
        </Button>
        <Button variant="destructive" disabled={remove.isPending} onClick={() => remove.mutate()}>
          {remove.isPending ? t('sources.removing') : t('sources.remove')}
        </Button>
      </div>
    </Dialog>
  )
}
