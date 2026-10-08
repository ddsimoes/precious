import type { ReactNode } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { Link } from 'react-router'

import { planCleanup } from '@/api/cleanup'
import type { ReviewListName } from '@/api/opportunities'
import type { Source } from '@/api/sources'
import { CleanupOutcome } from '@/cleanup/CleanupOutcome'
import { Button } from '@/components/ui/button'
import { useOrganize } from '@/organize/useOrganize'

// DraftCleanup drafts a cleanup plan of a source's discarded items, or of
// the discarded rows of one review list, and shows its preview (R4 design
// D3). It is off, with the reason, while no source is chosen, the disk is
// not connected, or changes are not allowed; a folder of the owner's own
// that holds the quarantine's name is explained instead.
export function DraftCleanup({ source, list }: { source: Source | undefined; list: ReviewListName | null }) {
  const { t } = useTranslation()
  const organize = useOrganize()
  const label = list === null ? t('cleanup.draft') : t('cleanup.draftFromList')

  if (source?.quarantine.name_taken === true) {
    return (
      <p role="note" className="rounded-md border border-amber-300 bg-amber-50 p-3 text-sm">
        {t('cleanup.nameTaken')}
      </p>
    )
  }

  let hint: ReactNode = null
  if (source === undefined) {
    hint = t('cleanup.chooseSource')
  } else if (source.state !== 'online') {
    hint = t('cleanup.offline')
  } else if (!source.writes.enabled) {
    hint = (
      <Trans
        i18nKey="cleanup.writesOff"
        components={{ sourcesLink: <Link to="/sources" className="font-medium text-primary underline" /> }}
      />
    )
  }

  return (
    <div className="grid gap-2">
      <div className="flex flex-wrap items-center gap-3">
        <Button
          size="sm"
          disabled={source === undefined || hint !== null || organize.pending}
          onClick={() =>
            source !== undefined &&
            organize.start((csrfToken) => planCleanup(source.id, list, csrfToken), { always: true })
          }
        >
          {organize.pending ? t('cleanup.drafting') : label}
        </Button>
        {hint !== null && <span className="text-sm text-muted-foreground">{hint}</span>}
      </div>
      <CleanupOutcome organize={organize} />
    </div>
  )
}
