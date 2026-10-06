import { useTranslation } from 'react-i18next'

import { decisionChoices, useDecide, type DecisionChoice } from '@/api/decisions'
import type { Decision } from '@/api/home'
import { ErrorBanner } from '@/app/ErrorBanner'
import { Button } from '@/components/ui/button'

interface DecisionButtonsProps {
  entryId: string
  // name names the entry in the group's label.
  name: string
  // own is the entry's own decision (null: it follows its folder), when
  // known; a copy reports only its effective decision.
  own?: Decision | null
}

// DecisionButtons are the decision controls of a row in Opportunities,
// Compare, and Gems: the detail panel's choices, smaller.
export function DecisionButtons({ entryId, name, own }: DecisionButtonsProps) {
  const { t } = useTranslation()
  const decide = useDecide()
  const current: DecisionChoice | undefined = own === undefined ? undefined : (own ?? 'inherit')

  return (
    <div className="grid gap-1">
      <div role="group" aria-label={t('review.decisionFor', { name })} className="flex flex-wrap gap-1">
        {decisionChoices.map((choice) => (
          <Button
            key={choice}
            size="sm"
            variant={choice === current ? 'default' : 'outline'}
            aria-pressed={current === undefined ? undefined : choice === current}
            disabled={decide.isPending}
            className="h-7 px-2 text-xs"
            onClick={(event) => {
              event.stopPropagation()
              if (choice !== current) {
                decide.mutate({ id: entryId, choice })
              }
            }}
          >
            {t(`entry.decisionChoice.${choice}`)}
          </Button>
        ))}
      </div>
      {decide.isError && <ErrorBanner error={decide.error} onDismiss={() => decide.reset()} />}
    </div>
  )
}
