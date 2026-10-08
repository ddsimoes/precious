import { useTranslation } from 'react-i18next'

import type { Item } from '@/api/organize'
import { cn } from '@/lib/utils'

// ItemLine shows one step of an action: an entry's path before and after,
// the folder made or removed, the origin record written or removed, the
// item deleted for good, or the comparison before a purge, with its state
// when asked, the reason it was refused or left as it is, the system's
// error text, and the decision it would take from its new place.
export function ItemLine({ item, showState = false }: { item: Item; showState?: boolean }) {
  const { t } = useTranslation()
  const from = item.from?.path ?? ''
  const to = item.to?.path ?? ''
  const line =
    item.op === 'rename'
      ? t('organize.item.rename', { from, to })
      : t(`organize.item.${item.op}`, { path: item.op === 'mkdir' || item.op === 'record' ? to : from })
  return (
    <li
      className={cn(
        'grid gap-0.5 rounded-md border px-2 py-1.5 text-sm',
        item.state === 'manual_recovery' && 'border-amber-300 bg-amber-50',
      )}
    >
      <span className="break-all">{line}</span>
      {(showState || item.reason !== null) && (
        <span className="text-muted-foreground">
          {[showState && t(`organize.itemState.${item.state}`), item.reason !== null && t(`organize.reason.${item.reason}`)]
            .filter((part) => part !== false)
            .join(' · ')}
        </span>
      )}
      {item.detail !== null && item.found === null && (
        <span className="font-mono text-xs break-all">{item.detail}</span>
      )}
      {item.decision_after !== null && (
        <span className="text-muted-foreground">
          {t('organize.item.decisionAfter', { decision: t(`home.decision.${item.decision_after}`) })}
        </span>
      )}
    </li>
  )
}
