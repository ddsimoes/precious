import { useTranslation } from 'react-i18next'

import type { JobState, Progress } from '@/api/jobs'
import { useFormat } from '@/lib/format'
import { cn } from '@/lib/utils'

// finishingPhase is the scan's `phase` progress value once the walk is done.
const finishingPhase = 2

interface ScanProgressProps {
  state: JobState
  progress: Progress
  // label names the scan, such as its source, for assistive technology.
  label: string
  className?: string
}

// ScanProgress shows a scan's state and its folders, files, and bytes so far.
// The job event stream keeps the figures current.
export function ScanProgress({ state, progress, label, className }: ScanProgressProps) {
  const { t } = useTranslation()
  const fmt = useFormat()
  let stateText: string
  if (state === 'running') {
    stateText = progress.phase === finishingPhase ? t('scan.state.finishing') : t('scan.state.walking')
  } else {
    stateText = t(`scan.state.${state}`)
  }
  const unreadable = progress.unreadable ?? 0

  return (
    <div role="status" aria-label={label} className={cn('grid gap-1 text-sm', className)}>
      <p className="font-medium">{stateText}</p>
      <dl className="flex flex-wrap gap-x-4 gap-y-1">
        <div className="flex gap-1">
          <dt className="text-muted-foreground">{t('scan.folders')}</dt>
          <dd>{fmt.count(progress.dirs ?? 0)}</dd>
        </div>
        <div className="flex gap-1">
          <dt className="text-muted-foreground">{t('scan.files')}</dt>
          <dd>{fmt.count(progress.files ?? 0)}</dd>
        </div>
        <div className="flex gap-1">
          <dt className="text-muted-foreground">{t('scan.size')}</dt>
          <dd>{fmt.bytes(progress.bytes ?? 0)}</dd>
        </div>
      </dl>
      {unreadable > 0 && (
        <p className="text-muted-foreground">
          {t('scan.unreadable', { count: unreadable, formatted: fmt.count(unreadable) })}
        </p>
      )}
    </div>
  )
}
