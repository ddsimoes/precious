import { useTranslation } from 'react-i18next'

import type { Selection, SetDecisionResult } from '@/api/decisions'
import type { SetTagsResult } from '@/api/tags'
import { Button } from '@/components/ui/button'
import { Dialog } from '@/components/ui/dialog'
import { useFormat } from '@/lib/format'

// The confirmation and report of a bulk change, shared by Search and the
// lists of Opportunities (design D10, R2 D13).

export type Report = { kind: 'decision'; result: SetDecisionResult } | { kind: 'tags'; result: SetTagsResult }

// SelectAllDialog asks before a selection is used: it shows the count, the
// bytes, and the kept entries a decision other than keep will skip.
export function SelectAllDialog({
  title,
  selection,
  onConfirm,
  onCancel,
}: {
  title: string
  selection: Selection
  onConfirm: () => void
  onCancel: () => void
}) {
  const { t } = useTranslation()
  const fmt = useFormat()
  return (
    <Dialog title={title} onClose={onCancel} alert>
      <ul className="grid gap-1 text-sm">
        <li className="font-medium">
          {t('search.confirmCount', { count: selection.count, formatted: fmt.count(selection.count) })}
        </li>
        <li>{t('search.confirmBytes', { bytes: fmt.bytes(selection.bytes) })}</li>
        <li>
          {selection.kept.count === 0
            ? t('search.confirmNoneKept')
            : t('search.confirmKept', {
                count: selection.kept.count,
                formatted: fmt.count(selection.kept.count),
                bytes: fmt.bytes(selection.kept.bytes),
              })}
        </li>
        <li className="text-muted-foreground">{t('search.confirmExpiry')}</li>
      </ul>
      <div className="flex justify-end gap-2">
        <Button variant="outline" onClick={onCancel}>
          {t('search.cancel')}
        </Button>
        <Button onClick={onConfirm}>{t('search.confirm')}</Button>
      </div>
    </Dialog>
  )
}

// BulkReport tells what a bulk change did, listing the kept entries a
// decision skipped.
export function BulkReport({ report, onClose }: { report: Report; onClose: () => void }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const { result } = report
  return (
    <section aria-label={t('search.report')} className="grid gap-2 rounded-lg border bg-card p-3 text-sm">
      {report.kind === 'tags' ? (
        <p>{t('search.tagsApplied', { count: result.applied, formatted: fmt.count(result.applied) })}</p>
      ) : (
        <>
          <p>{t('search.applied', { count: result.applied, formatted: fmt.count(result.applied) })}</p>
          {report.result.skipped_count > 0 && (
            <>
              <p className="font-medium">
                {t('search.skipped', {
                  count: report.result.skipped_count,
                  formatted: fmt.count(report.result.skipped_count),
                })}
              </p>
              <ul
                aria-label={t('search.skipped', {
                  count: report.result.skipped_count,
                  formatted: fmt.count(report.result.skipped_count),
                })}
                className="grid gap-0.5 pl-4"
              >
                {report.result.skipped.map((entry) => (
                  <li key={entry.entry_id} className="break-all">
                    {entry.path}
                  </li>
                ))}
              </ul>
              {report.result.skipped_count > report.result.skipped.length && (
                <p className="text-muted-foreground">
                  {t('search.skippedMore', {
                    count: report.result.skipped_count - report.result.skipped.length,
                    formatted: fmt.count(report.result.skipped_count - report.result.skipped.length),
                  })}
                </p>
              )}
            </>
          )}
        </>
      )}
      <div>
        <Button variant="ghost" size="sm" onClick={onClose}>
          {t('search.closeReport')}
        </Button>
      </div>
    </section>
  )
}
