import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'

import { isDrillable, type EntryRow } from '@/api/entries'
import { chooseCompareFirst, useCompareChoice } from '@/compare/compareChoice'
import { Button } from '@/components/ui/button'

// CompareWith is the detail panel's way into Compare for a folder or an
// archive (R2 design D11): "Compare with…" remembers this one as the first
// side; on the second one's panel, "Compare with <first>" opens Compare
// with the first on the left.
export function CompareWith({ entry, name }: { entry: EntryRow; name: string }) {
  const { t } = useTranslation()
  const first = useCompareChoice()
  if (!isDrillable(entry)) {
    return null
  }
  if (first === null) {
    return (
      <Button size="sm" variant="outline" onClick={() => chooseCompareFirst({ id: entry.id, name })}>
        {t('compare.compareWith')}
      </Button>
    )
  }
  const cancel = (
    <Button size="sm" variant="ghost" onClick={() => chooseCompareFirst(null)}>
      {t('compare.forget')}
    </Button>
  )
  if (first.id === entry.id) {
    return (
      <div className="grid w-full gap-1">
        <p role="status" className="text-sm">
          {t('compare.chosen', { name: first.name })}
        </p>
        <div>{cancel}</div>
      </div>
    )
  }
  return (
    <>
      <Button asChild size="sm" variant="outline">
        <Link
          to={{ pathname: '/compare', search: `?${new URLSearchParams({ left: first.id, right: entry.id })}` }}
          onClick={() => chooseCompareFirst(null)}
        >
          {t('compare.compareWithName', { name: first.name })}
        </Link>
      </Button>
      {cancel}
    </>
  )
}
