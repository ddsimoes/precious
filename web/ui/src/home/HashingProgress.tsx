import { useTranslation } from 'react-i18next'

import type { HashingJob } from '@/api/content'
import { hashNowKind } from '@/api/jobs'
import { useFormat } from '@/lib/format'

// phaseNames names the `phase` values of a hashing job's progress (R2
// design Interfaces): 1 listing archives, 2 large files, 3 small files.
const phaseNames: Record<number, 'listing' | 'large' | 'small'> = { 1: 'listing', 2: 'large', 3: 'small' }

// HashingProgress shows a hashing job, what it is doing, and its checked
// bytes out of the bytes that could have a copy. The job event stream keeps
// it current.
export function HashingProgress({ job, label }: { job: HashingJob; label: string }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const checked = job.progress.checked_bytes ?? 0
  const candidate = job.progress.candidate_bytes ?? 0
  const phase = phaseNames[job.progress.phase ?? 0]
  let stateText: string | null = null
  if (job.state !== 'running') {
    stateText = t(`scan.state.${job.state}`)
  } else if (phase !== undefined) {
    stateText = t(`hashing.phase.${phase}`)
  }

  return (
    <div role="status" aria-label={label} className="grid gap-1 text-sm">
      <p className="font-medium">
        {t(`hashing.kind.${job.kind === hashNowKind ? 'hash_now' : 'hash'}`)}
        {stateText !== null && ` · ${stateText}`}
      </p>
      <p>{t('hashing.progress', { checked: fmt.bytes(checked), candidate: fmt.bytes(candidate) })}</p>
      <div aria-hidden="true" className="h-2 overflow-hidden rounded-full bg-muted">
        <div
          className="h-full rounded-full bg-primary"
          // React sets this through the CSSOM, which the strict CSP allows.
          style={{ width: `${candidate === 0 ? 0 : Math.min(1, checked / candidate) * 100}%` }}
        />
      </div>
    </div>
  )
}
