import { useTranslation } from 'react-i18next'

import type { Item } from '@/api/organize'
import { useFormat } from '@/lib/format'
import { cn } from '@/lib/utils'

// ItemLine shows one step of an action: an entry's path before and after,
// a file's modification time before and after, the folder made or removed,
// the origin record written or removed, the item deleted for good, or the
// comparison before a purge, with its state when asked, the reason it was
// refused or left as it is, the identical copy that takes its name, the
// files of the same name it leaves behind, the system's error text, and the
// decision it would take from its new place.
export function ItemLine({ item, showState = false }: { item: Item; showState?: boolean }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const from = item.from?.path ?? ''
  const to = item.to?.path ?? ''
  let line: string
  if (item.op === 'rename') {
    line = t('organize.item.rename', { from, to })
  } else if (item.op === 'set_mtime') {
    // A file left out of setting file dates shows by its path and reason:
    // it gets no new time.
    const path = item.from?.path ?? item.entry?.path ?? ''
    const times = item.mtime ?? null
    line =
      times === null || item.state === 'refused'
        ? path
        : t('organize.item.set_mtime', {
            path,
            from: times.from === null ? t('organize.item.unknownTime') : fmt.instant(times.from),
            to: fmt.instant(times.to),
          })
  } else {
    line = t(`organize.item.${item.op}`, { path: item.op === 'mkdir' || item.op === 'record' ? to : from })
  }
  // A file an organize by date plans names in its detail the files of the
  // same name it leaves behind; any other item's detail is the system's
  // error text.
  const siblings = item.state === 'planned' ? item.detail : null
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
      {item.copy_of != null && (
        <span className="break-all text-muted-foreground">{t('organize.item.copyOf', { path: item.copy_of.path })}</span>
      )}
      {siblings !== null && (
        <span className="break-all text-amber-900">{t('organize.item.siblings', { names: siblings })}</span>
      )}
      {item.detail !== null && item.found === null && siblings === null && (
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
