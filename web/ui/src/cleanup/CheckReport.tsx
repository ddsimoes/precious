import { skipToken, useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useId, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useNavigate, useParams } from 'react-router'

import {
  cancelJob,
  checkClasses,
  checkFilesQueryKey,
  checkProgressKey,
  checkPurge,
  checkQueryKey,
  checksQueryRoot,
  confirmPurge,
  fetchCheck,
  fetchCheckFiles,
  needsOwnConfirmation,
  planPurge,
  verdicts,
  type Check,
  type CheckClass,
  type CheckFile,
  type CheckFilesFilter,
  type ConfirmTargets,
  type Verdict,
} from '@/api/cleanup'
import type { Progress } from '@/api/jobs'
import { historyQueryRoot, planMove, runAction, type Action, type PlanResult } from '@/api/organize'
import { ApiError } from '@/app/api'
import { ErrorBanner } from '@/app/ErrorBanner'
import { PageTitle } from '@/app/PageTitle'
import { useCsrfToken } from '@/app/session'
import { CleanupOutcome, CleanupStatus } from '@/cleanup/CleanupOutcome'
import { useRestore, type Restore } from '@/cleanup/useRestore'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Dialog } from '@/components/ui/dialog'
import { Label } from '@/components/ui/label'
import { useFormat } from '@/lib/format'
import { useSourceLabel } from '@/lib/sourceParams'
import { FolderChooser } from '@/organize/FolderChooser'
import { OrganizeOutcome } from '@/organize/OrganizeOutcome'
import { useOrganize, type Organize } from '@/organize/useOrganize'

// CheckReportPage is the report of one check before deleting for good,
// /cleanup/checks/<id>.
export function CheckReportPage() {
  const { t } = useTranslation()
  const { checkId = '' } = useParams()
  const check = useQuery({
    queryKey: checkQueryKey(checkId),
    queryFn: ({ signal }) => fetchCheck(checkId, signal),
  })
  return (
    <div className="grid max-w-4xl gap-4">
      <PageTitle>{t('cleanup.check.title')}</PageTitle>
      {check.isPending && (
        <p role="status" className="text-sm text-muted-foreground">
          {t('app.loading')}
        </p>
      )}
      {check.isError && <ErrorBanner error={check.error} onRetry={() => void check.refetch()} />}
      {check.data !== undefined && <CheckReport key={check.data.id} check={check.data} />}
    </div>
  )
}

// CheckReport shows what a check found (R4 design D7, D8, D10): its
// progress while it runs, the files and bytes of each verdict and, among
// the files with no copy, of each class, the files filterable by both, the
// confirmations the purge waits for, and the purge itself once allowed. A
// check that went out of date offers to check the set again.
function CheckReport({ check }: { check: Check }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const csrfToken = useCsrfToken()
  const sourceLabel = useSourceLabel()
  const restore = useRestore()
  const moveOut = useOrganize()
  const [purgePlan, setPurgePlan] = useState<PlanResult | null>(null)
  const [purged, setPurged] = useState<Action | null>(null)
  // A set none of whose items is left in quarantine (restored, moved out,
  // or deleted for good) has nothing to confirm or delete; checking again
  // finds that too.
  const [setGone, setSetGone] = useState(false)
  const gone = check.items === 0 || setGone
  const open = check.state === 'ready' && !gone

  const confirm = useMutation({
    mutationFn: (targets: ConfirmTargets) => confirmPurge(check.id, targets, csrfToken),
    onSuccess: ({ check: next }) => {
      queryClient.setQueryData(checkQueryKey(check.id), next)
      void queryClient.invalidateQueries({ queryKey: [...checksQueryRoot, check.id, 'files'] })
    },
  })
  // Checking again checks what is left of the set in quarantine; nothing
  // left says so.
  const again = useMutation({
    mutationFn: async () => {
      try {
        return await checkPurge({ check_id: check.id }, csrfToken)
      } catch (error) {
        if (error instanceof ApiError && error.code === 'not_found') {
          return null
        }
        throw error
      }
    },
    onSuccess: (started) => {
      if (started === null) {
        setSetGone(true)
      } else {
        void navigate(`/cleanup/checks/${encodeURIComponent(started.check_id)}`)
      }
    },
  })
  const cancel = useMutation({
    mutationFn: () => cancelJob(check.job_id ?? '', csrfToken),
  })
  const plan = useMutation({
    mutationFn: () => planPurge(check.id, csrfToken),
    onSuccess: setPurgePlan,
  })

  const junk = check.counts.class.likely_junk
  const back = `/cleanup?${new URLSearchParams({ source: check.source_id })}`

  return (
    <>
      <Link to={back} className="text-sm font-medium text-primary hover:underline">
        {t('cleanup.check.back')}
      </Link>
      <p className="text-sm">
        {t('cleanup.check.of', {
          source: sourceLabel(check.source_id),
          items: t('cleanup.check.items', { count: check.items, formatted: fmt.count(check.items) }),
          time: fmt.dateTime(check.created_at),
        })}
      </p>

      {check.state === 'running' && (
        <Card role="status" className="grid gap-2 p-4 text-sm">
          <CheckProgress jobId={check.job_id} />
          {check.job_id !== null && (
            <div>
              <Button size="sm" variant="outline" disabled={cancel.isPending} onClick={() => cancel.mutate()}>
                {t('cleanup.check.cancel')}
              </Button>
            </div>
          )}
          {cancel.isError && <ErrorBanner error={cancel.error} onDismiss={() => cancel.reset()} />}
        </Card>
      )}
      {(check.state === 'stale' || check.state === 'failed') && (
        <div role="alert" className="grid gap-2 rounded-md border border-amber-300 bg-amber-50 p-3 text-sm">
          <p className="font-medium">{t(`cleanup.check.${check.state}`)}</p>
          {!gone && (
            <div>
              <Button size="sm" disabled={again.isPending} onClick={() => again.mutate()}>
                {again.isPending ? t('cleanup.quarantine.checking') : t('cleanup.check.again')}
              </Button>
            </div>
          )}
          {gone && <p>{t('cleanup.check.setGone')}</p>}
          {again.isError && <ErrorBanner error={again.error} onDismiss={() => again.reset()} />}
        </div>
      )}
      {check.state === 'ready' && gone && (
        <p role="status" className="rounded-md border bg-card p-3 text-sm">
          {t('cleanup.check.setGone')}
        </p>
      )}

      <Card className="grid gap-3 p-4">
        <Section title={t('cleanup.check.verdicts')}>
          <ul aria-label={t('cleanup.check.verdicts')} className="grid gap-1 text-sm">
            {verdicts.map((verdict) => (
              <AmountLine
                key={verdict}
                label={t(`cleanup.verdict.${verdict}`)}
                files={check.counts.verdict[verdict].files}
                bytes={check.counts.verdict[verdict].bytes}
              />
            ))}
          </ul>
        </Section>
        <Section title={t('cleanup.check.classes')}>
          <ul aria-label={t('cleanup.check.classes')} className="grid gap-1 text-sm">
            {checkClasses.map((name) => (
              <AmountLine
                key={name}
                label={t(`cleanup.class.${name}`)}
                files={check.counts.class[name].files}
                bytes={check.counts.class[name].bytes}
              />
            ))}
          </ul>
        </Section>
      </Card>

      {check.state !== 'running' && (
        <Card className="grid gap-3 p-4 text-sm">
          <Section title={t('cleanup.check.confirmations')}>
            <p>{t('cleanup.check.gateHelp')}</p>
            <p className="font-medium">
              {t('cleanup.check.unconfirmed', {
                files: t('units.files', { count: check.unconfirmed.files, formatted: fmt.count(check.unconfirmed.files) }),
                bytes: fmt.bytes(check.unconfirmed.bytes),
              })}
            </p>
            {junk.files > 0 &&
              (check.junk_confirmed || !gone) &&
              (check.junk_confirmed ? (
                <p>{t('cleanup.check.junkConfirmed')}</p>
              ) : (
                <div>
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={!open || confirm.isPending}
                    onClick={() => confirm.mutate({ group: 'likely_junk' })}
                  >
                    {t('cleanup.check.confirmJunk', {
                      files: t('units.files', { count: junk.files, formatted: fmt.count(junk.files) }),
                      bytes: fmt.bytes(junk.bytes),
                    })}
                  </Button>
                </div>
              ))}
            {confirm.isError && <ErrorBanner error={confirm.error} onDismiss={() => confirm.reset()} />}
          </Section>
        </Card>
      )}

      <CleanupOutcome organize={restore.organize} onChooseDestination={restore.chooseDestination} />
      <OrganizeOutcome organize={moveOut} />
      {check.state !== 'running' && (
        <CheckFiles
          check={check}
          open={open}
          confirming={confirm.isPending}
          onConfirm={(file) => confirm.mutate({ file_ids: [file.id] })}
          restore={restore}
          moveOut={moveOut}
        />
      )}

      {check.state !== 'running' && purged === null && !gone && (
        <Card className="grid gap-2 p-4 text-sm">
          <Section title={t('cleanup.purge.title')}>
            <p>{t('cleanup.purge.help')}</p>
            {!check.allowed && open && <p className="text-muted-foreground">{t('cleanup.purge.notAllowed')}</p>}
            <div>
              <Button
                variant="destructive"
                disabled={!open || !check.allowed || plan.isPending}
                onClick={() => plan.mutate()}
              >
                {t('cleanup.purge.start')}
              </Button>
            </div>
            {plan.isError && <ErrorBanner error={plan.error} onDismiss={() => plan.reset()} />}
          </Section>
        </Card>
      )}
      {purged !== null && <CleanupStatus ran={purged} onClose={() => setPurged(null)} />}
      {purgePlan !== null && (
        <PurgeConfirm
          plan={purgePlan}
          onRan={(action) => {
            setPurgePlan(null)
            setPurged(action)
            void queryClient.invalidateQueries({ queryKey: historyQueryRoot })
          }}
          onClose={() => setPurgePlan(null)}
        />
      )}
    </>
  )
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  const headingId = useId()
  return (
    <section aria-labelledby={headingId} className="grid gap-2">
      <h2 id={headingId} className="text-base font-semibold">
        {title}
      </h2>
      {children}
    </section>
  )
}

function AmountLine({ label, files, bytes }: { label: string; files: number; bytes: number }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  return (
    <li className="flex flex-wrap justify-between gap-x-3">
      <span className="font-medium">{label}</span>
      <span className="text-muted-foreground">
        {t('units.files', { count: files, formatted: fmt.count(files) })} · {fmt.bytes(bytes)}
      </span>
    </li>
  )
}

// CheckProgress shows how far a running check has read, from its job's
// events.
function CheckProgress({ jobId }: { jobId: string | null }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const progress = useQuery<Progress>({
    queryKey: checkProgressKey(jobId ?? ''),
    queryFn: skipToken,
  })
  const p = progress.data
  if (p === undefined || p.of_files === undefined) {
    return <p className="font-medium">{t('cleanup.check.running')}</p>
  }
  return (
    <>
      <p className="font-medium">{t('cleanup.check.running')}</p>
      <p>
        {t('cleanup.check.progress', {
          files: fmt.count(p.files ?? 0),
          ofFiles: fmt.count(p.of_files),
          bytes: fmt.bytes(p.bytes ?? 0),
          ofBytes: fmt.bytes(p.of_bytes ?? 0),
        })}
      </p>
      <progress className="w-full" max={p.of_bytes ?? 0} value={p.bytes ?? 0} />
    </>
  )
}

const allValue = ''

const confirmedChoices = { all: null, yes: true, no: false } as const

// CheckFiles lists a check's files, filtered by verdict, class, and
// confirmation, paged. A file that must be confirmed on its own offers
// Confirm, Move out… (not for a file inside an archive), and Restore of its
// item, while the check is ready, grouped under the file's name. A file of
// an item that could not be read stays in quarantine with its item, and
// offers no Confirm.
function CheckFiles({
  check,
  open,
  confirming,
  onConfirm,
  restore,
  moveOut,
}: {
  check: Check
  open: boolean
  confirming: boolean
  onConfirm: (file: CheckFile) => void
  restore: Restore
  moveOut: Organize
}) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const verdictId = useId()
  const classId = useId()
  const confirmedId = useId()
  const sourceLabel = useSourceLabel()
  const [filter, setFilter] = useState<CheckFilesFilter>({ verdict: null, class: null, confirmed: null })
  const [moving, setMoving] = useState<CheckFile | null>(null)

  const pages = useInfiniteQuery({
    queryKey: checkFilesQueryKey(check.id, filter),
    queryFn: ({ pageParam, signal }) => fetchCheckFiles(check.id, filter, pageParam, signal),
    initialPageParam: null as string | null,
    getNextPageParam: (page) => page.next_cursor,
  })
  const files = pages.data?.pages.flatMap((page) => page.items) ?? []
  const confirmedKey =
    filter.confirmed === null ? 'all' : filter.confirmed ? 'yes' : 'no'

  const selectClass = 'h-9 rounded-md border border-input bg-card px-2 text-sm'
  return (
    <Card className="grid gap-3 p-4 text-sm">
      <Section title={t('cleanup.files.title')}>
        <div className="flex flex-wrap items-center gap-3">
          <div className="flex items-center gap-2">
            <Label htmlFor={verdictId}>{t('cleanup.files.verdict')}</Label>
            <select
              id={verdictId}
              className={selectClass}
              value={filter.verdict ?? allValue}
              onChange={(event) =>
                setFilter({ ...filter, verdict: event.target.value === allValue ? null : (event.target.value as Verdict) })
              }
            >
              <option value={allValue}>{t('cleanup.files.all')}</option>
              {verdicts.map((verdict) => (
                <option key={verdict} value={verdict}>
                  {t(`cleanup.verdict.${verdict}`)}
                </option>
              ))}
            </select>
          </div>
          <div className="flex items-center gap-2">
            <Label htmlFor={classId}>{t('cleanup.files.class')}</Label>
            <select
              id={classId}
              className={selectClass}
              value={filter.class ?? allValue}
              onChange={(event) =>
                setFilter({ ...filter, class: event.target.value === allValue ? null : (event.target.value as CheckClass) })
              }
            >
              <option value={allValue}>{t('cleanup.files.all')}</option>
              {checkClasses.map((name) => (
                <option key={name} value={name}>
                  {t(`cleanup.class.${name}`)}
                </option>
              ))}
            </select>
          </div>
          <div className="flex items-center gap-2">
            <Label htmlFor={confirmedId}>{t('cleanup.files.confirmed')}</Label>
            <select
              id={confirmedId}
              className={selectClass}
              value={confirmedKey}
              onChange={(event) =>
                setFilter({
                  ...filter,
                  confirmed: confirmedChoices[event.target.value as keyof typeof confirmedChoices],
                })
              }
            >
              {Object.keys(confirmedChoices).map((key) => (
                <option key={key} value={key}>
                  {t(`cleanup.files.confirmedChoice.${key as keyof typeof confirmedChoices}`)}
                </option>
              ))}
            </select>
          </div>
        </div>
        {pages.isPending && (
          <p role="status" className="text-muted-foreground">
            {t('app.loading')}
          </p>
        )}
        {pages.isError && <ErrorBanner error={pages.error} onRetry={() => void pages.refetch()} />}
        {pages.data !== undefined && files.length === 0 && (
          <p className="text-muted-foreground">{t('cleanup.files.none')}</p>
        )}
        {files.length > 0 && (
          <ul aria-label={t('cleanup.files.title')} className="grid gap-2">
            {files.map((file) => {
              const own = needsOwnConfirmation(file)
              const label = file.member === null ? file.path : t('cleanup.files.member', { path: file.path, member: file.member })
              return (
                <li key={file.id} className="grid gap-1 rounded-md border p-2">
                  <div className="flex flex-wrap items-baseline justify-between gap-x-3">
                    <span className="min-w-0 font-medium break-all">{label}</span>
                    <span className="text-muted-foreground">{fmt.bytes(file.size)}</span>
                  </div>
                  <p className="text-muted-foreground">
                    {[
                      t(`cleanup.verdict.${file.verdict}`),
                      ...(file.class === null ? [] : [t(`cleanup.class.${file.class}`)]),
                      ...(file.confirmed ? [t('cleanup.files.isConfirmed')] : []),
                      ...(file.item_readable && !own && file.verdict === 'unique' && file.class === 'likely_junk'
                        ? [check.junk_confirmed ? t('cleanup.files.junkConfirmed') : t('cleanup.files.junkGroup')]
                        : []),
                    ].join(' · ')}
                  </p>
                  {file.copy !== null && (
                    <p className="break-all">
                      {t(file.copy.hard_link ? 'cleanup.files.hardLink' : 'cleanup.files.copy', {
                        path: file.copy.path,
                        source: sourceLabel(file.copy.source_id),
                      })}
                    </p>
                  )}
                  {!file.item_readable && <p>{t('cleanup.files.itemUnreadable')}</p>}
                  {open && ((own && !file.confirmed) || !file.item_readable) && (
                    <div
                      role="group"
                      aria-label={t('cleanup.files.actionsFor', { name: label })}
                      className="flex flex-wrap gap-2"
                    >
                      {own && (
                        <Button size="sm" variant="outline" disabled={confirming} onClick={() => onConfirm(file)}>
                          {t('cleanup.files.confirm')}
                        </Button>
                      )}
                      {file.entry_id !== null && (
                        <Button size="sm" variant="outline" disabled={moveOut.pending} onClick={() => setMoving(file)}>
                          {t('cleanup.files.moveOut')}
                        </Button>
                      )}
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={restore.organize.pending}
                        onClick={() => restore.restore([file.item.id])}
                      >
                        {t('cleanup.files.restoreItem')}
                      </Button>
                    </div>
                  )}
                </li>
              )
            })}
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
      </Section>
      {moving !== null && moving.entry_id !== null && (
        <FolderChooser
          title={t('cleanup.files.moveOutTitle', { name: moving.path.split('/').at(-1) ?? moving.path })}
          sourceIds={[check.source_id]}
          onChoose={(folder) => {
            const entryId = moving.entry_id ?? ''
            setMoving(null)
            moveOut.start((csrfToken) => planMove({ entry_id: entryId }, folder.id, csrfToken))
          }}
          onClose={() => setMoving(null)}
        />
      )}
    </Card>
  )
}

// PurgeConfirm is the last question before deleting for good: what the
// purge plan deletes, in files and bytes, and that it cannot be undone.
function PurgeConfirm({
  plan,
  onRan,
  onClose,
}: {
  plan: PlanResult
  onRan: (action: Action) => void
  onClose: () => void
}) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const csrfToken = useCsrfToken()
  const { action } = plan
  const counts = action.entries ?? action.counts
  const run = useMutation({
    mutationFn: () => runAction(action.id, csrfToken),
    onSuccess: (result) => onRan(result.action),
  })
  return (
    <Dialog title={t('cleanup.purge.confirmTitle')} onClose={onClose} alert>
      <p className="text-sm font-medium">
        {t('cleanup.purge.confirmBody', {
          items: t('cleanup.check.items', { count: counts.planned, formatted: fmt.count(counts.planned) }),
          files: t('units.files', { count: action.files, formatted: fmt.count(action.files) }),
          bytes: fmt.bytes(action.bytes),
        })}
      </p>
      {counts.refused > 0 && (
        <p className="text-sm">
          {t('cleanup.purge.refused', { count: counts.refused, formatted: fmt.count(counts.refused) })}
        </p>
      )}
      <p className="text-sm text-muted-foreground">{t('cleanup.purge.confirmHelp')}</p>
      {run.isError && <ErrorBanner error={run.error} />}
      <div className="flex justify-end gap-2">
        <Button variant="outline" onClick={onClose}>
          {t('cleanup.purge.keep')}
        </Button>
        <Button variant="destructive" disabled={counts.planned === 0 || run.isPending} onClick={() => run.mutate()}>
          {run.isPending ? t('organize.preview.confirming') : t('cleanup.purge.confirm')}
        </Button>
      </div>
    </Dialog>
  )
}
