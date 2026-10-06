import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useId } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { Link } from 'react-router'

import { decisionChoices, refreshAfterDecision, setDecision, type DecisionChoice } from '@/api/decisions'
import type { EntryRow, Intent } from '@/api/entries'
import { ErrorBanner } from '@/app/ErrorBanner'
import { useCsrfToken } from '@/app/session'
import { Button } from '@/components/ui/button'
import { useEntryLink } from '@/detail/useEntryLink'

interface DecisionControlsProps {
  entry: EntryRow
  intent: Intent
  // rootLabel names the source's top folder, whose path is empty.
  rootLabel: string
}

// DecisionControls shows an entry's own and effective decision, where the
// effective one comes from, and sets the own decision with an individual
// request, which applies even to a kept entry.
export function DecisionControls({ entry, intent, rootLabel }: DecisionControlsProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const csrfToken = useCsrfToken()
  const entryLink = useEntryLink()
  const groupId = useId()

  const change = useMutation({
    mutationFn: (choice: DecisionChoice) => setDecision({ entry_id: entry.id }, choice, csrfToken),
    onSuccess: () => refreshAfterDecision(queryClient),
  })

  const current: DecisionChoice = intent.decision ?? 'inherit'
  let origin
  if (intent.from === null) {
    origin = t('detail.fromDefault')
  } else if (intent.from.id === entry.id) {
    origin = t('detail.fromSelf')
  } else {
    origin = (
      <Trans
        i18nKey="detail.inheritedFrom"
        values={{ path: intent.from.path === '' ? rootLabel : intent.from.path }}
        components={{
          folderLink: (
            <Link to={{ search: entryLink(intent.from.id) }} className="font-medium text-primary underline" />
          ),
        }}
      />
    )
  }

  return (
    <div className="grid gap-3">
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
        <dt className="text-muted-foreground">{t('detail.own')}</dt>
        <dd>{intent.decision === null ? t('detail.ownNone') : t(`home.decision.${intent.decision}`)}</dd>
        <dt className="text-muted-foreground">{t('detail.effective')}</dt>
        <dd>
          <span className="font-medium">{t(`home.decision.${intent.eff_decision}`)}</span>
          <span className="block text-muted-foreground">{origin}</span>
        </dd>
      </dl>
      <div role="group" aria-labelledby={groupId} className="grid gap-2">
        <span id={groupId} className="text-sm font-medium">
          {t('detail.setDecision')}
        </span>
        <div className="flex flex-wrap gap-1">
          {decisionChoices.map((choice) => (
            <Button
              key={choice}
              size="sm"
              variant={choice === current ? 'default' : 'outline'}
              aria-pressed={choice === current}
              disabled={change.isPending}
              onClick={() => {
                if (choice !== current) {
                  change.mutate(choice)
                }
              }}
            >
              {t(`entry.decisionChoice.${choice}`)}
            </Button>
          ))}
        </div>
      </div>
      {change.isError && <ErrorBanner error={change.error} onDismiss={() => change.reset()} />}
    </div>
  )
}
