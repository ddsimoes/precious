import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  refreshAfterOverride,
  setCategory,
  setGroup,
  type CategoryChoice,
  type GroupChoice,
  type OverrideResult,
} from '@/api/classification'
import { categories, type Classification, type EntryRow } from '@/api/entries'
import { ErrorBanner } from '@/app/ErrorBanner'
import { useCsrfToken } from '@/app/session'
import { Button } from '@/components/ui/button'

interface ClassificationControlsProps {
  entry: EntryRow
  classification: Classification
}

type Change = { category: CategoryChoice } | { group: GroupChoice }

// ClassificationControls lets the owner override a file's or folder's
// category and mark a folder as one item for review, or give either back to
// the rules (r2b design D5). The entry reads its new values at once; the
// figures of the folders above it follow when the scan the change starts
// ends, which the note after a change says.
export function ClassificationControls({ entry, classification }: ClassificationControlsProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const csrfToken = useCsrfToken()
  const categoryId = useId()
  const groupId = useId()
  const [done, setDone] = useState<OverrideResult | null>(null)
  const { owner } = classification
  const folder = entry.kind === 'directory'
  const rulesCategory = t(`entry.category.${classification.rules_category ?? 'unknown'}`)

  const change = useMutation({
    mutationFn: (c: Change) =>
      'category' in c
        ? setCategory({ entry_id: entry.id }, c.category, csrfToken)
        : setGroup({ entry_id: entry.id }, c.group, csrfToken),
    onMutate: () => setDone(null),
    onSuccess: async (result) => {
      setDone(result)
      await refreshAfterOverride(queryClient)
    },
  })

  const currentGroup: GroupChoice = owner.group ?? 'rules'
  const groupChoices: { value: GroupChoice; label: string }[] = [
    {
      value: 'rules',
      label: t('detail.override.asRules', {
        value: classification.rules_group ? t('detail.override.yes') : t('detail.override.no'),
      }),
    },
    { value: true, label: t('detail.override.yes') },
    { value: false, label: t('detail.override.no') },
  ]

  return (
    <div className="grid gap-3">
      <div className="grid gap-1 text-sm">
        <label htmlFor={categoryId} className="font-medium">
          {t('detail.override.category')}
        </label>
        <select
          id={categoryId}
          className="h-8 rounded-md border bg-background px-2"
          value={owner.category ?? 'rules'}
          disabled={change.isPending}
          onChange={(event) => change.mutate({ category: event.target.value as CategoryChoice })}
        >
          <option value="rules">
            {t(owner.category === null ? 'detail.override.asRules' : 'detail.override.backToRules', {
              value: rulesCategory,
            })}
          </option>
          {categories.map((category) => (
            <option key={category} value={category}>
              {t(`entry.category.${category}`)}
            </option>
          ))}
        </select>
      </div>
      {folder && (
        <div role="group" aria-labelledby={groupId} className="grid gap-1 text-sm">
          <span id={groupId} className="font-medium">
            {t('detail.override.group')}
          </span>
          <div className="flex flex-wrap gap-1">
            {groupChoices.map((choice) => (
              <Button
                key={String(choice.value)}
                size="sm"
                variant={choice.value === currentGroup ? 'default' : 'outline'}
                aria-pressed={choice.value === currentGroup}
                disabled={change.isPending}
                onClick={() => {
                  if (choice.value !== currentGroup) {
                    change.mutate({ group: choice.value })
                  }
                }}
              >
                {choice.label}
              </Button>
            ))}
          </div>
        </div>
      )}
      {done !== null && (
        <p role="status" className="text-sm text-muted-foreground">
          {done.scan === null ? t('detail.override.offline') : t('detail.override.afterScan')}
        </p>
      )}
      {change.isError && <ErrorBanner error={change.error} onDismiss={() => change.reset()} />}
    </div>
  )
}
