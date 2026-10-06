import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  decisionChoices,
  refreshAfterDecision,
  setDecision,
  type DecisionChoice,
  type Selection,
} from '@/api/decisions'
import { selectList, type SelectableList } from '@/api/opportunities'
import { ApiError } from '@/app/api'
import { ErrorBanner } from '@/app/ErrorBanner'
import { useCsrfToken } from '@/app/session'
import { BulkReport, SelectAllDialog, type Report } from '@/components/BulkSelection'
import { Button } from '@/components/ui/button'
import { useFormat } from '@/lib/format'

// ListSelectAll selects every open row of a review list or Gems section
// through select-list (R2 design D13), and then works as Search's "select
// all results": the owner confirms the count, bytes, and kept entries, sets
// a decision on the selection, which skips kept entries, and reads the
// report.
export function ListSelectAll({ list, source }: { list: SelectableList; source: string | null }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const queryClient = useQueryClient()
  const csrfToken = useCsrfToken()
  const [proposal, setProposal] = useState<Selection | null>(null)
  const [selection, setSelection] = useState<Selection | null>(null)
  const [report, setReport] = useState<Report | null>(null)

  const selectAll = useMutation({
    mutationFn: () => selectList(list, source, csrfToken),
    onSuccess: (created) => setProposal(created),
  })
  const decide = useMutation({
    mutationFn: ({ to, choice }: { to: Selection; choice: DecisionChoice }) =>
      setDecision({ selection_id: to.selection_id }, choice, csrfToken),
    onSuccess: async (result) => {
      setReport({ kind: 'decision', result })
      setSelection(null)
      await refreshAfterDecision(queryClient)
    },
    onError: (error) => {
      if (error instanceof ApiError && error.code === 'selection_expired') {
        setSelection(null)
      }
    },
  })

  return (
    <div className="grid gap-3">
      <div>
        <Button variant="outline" size="sm" disabled={selectAll.isPending} onClick={() => selectAll.mutate()}>
          {selectAll.isPending ? t('review.selectingAll') : t('review.selectAll')}
        </Button>
      </div>
      {selectAll.isError && <ErrorBanner error={selectAll.error} onDismiss={() => selectAll.reset()} />}

      {selection !== null && (
        <section aria-label={t('search.bulk')} className="grid gap-3 rounded-lg border bg-card p-3 text-sm">
          <div className="flex flex-wrap items-center gap-3">
            <span className="font-medium">
              {t('review.allSelected', {
                count: selection.count,
                formatted: fmt.count(selection.count),
                bytes: fmt.bytes(selection.bytes),
              })}
            </span>
            <Button variant="ghost" size="sm" onClick={() => setSelection(null)}>
              {t('search.clearSelection')}
            </Button>
          </div>
          <div role="group" aria-label={t('search.bulkDecision')} className="flex flex-wrap items-center gap-1">
            <span className="mr-2">{t('search.bulkDecision')}</span>
            {decisionChoices.map((choice) => (
              <Button
                key={choice}
                size="sm"
                variant="outline"
                disabled={decide.isPending}
                onClick={() => decide.mutate({ to: selection, choice })}
              >
                {t(`entry.decisionChoice.${choice}`)}
              </Button>
            ))}
          </div>
        </section>
      )}
      {decide.isError && <ErrorBanner error={decide.error} onDismiss={() => decide.reset()} />}
      {report !== null && <BulkReport report={report} onClose={() => setReport(null)} />}

      {proposal !== null && (
        <SelectAllDialog
          title={t('review.confirmTitle')}
          selection={proposal}
          onConfirm={() => {
            setSelection(proposal)
            setProposal(null)
          }}
          onCancel={() => setProposal(null)}
        />
      )}
    </div>
  )
}
