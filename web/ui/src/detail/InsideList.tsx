import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'

import type { EntryRow, InsideItem } from '@/api/entries'
import { useEntryLink } from '@/detail/useEntryLink'
import { useFormat } from '@/lib/format'
import { familyColors, neutralColor } from '@/map/colors'

// InsideList is the "Inside this folder" list of the detail panel (design
// D21): the notable entries below a folder, largest first, each with its
// category in plain words, its size and file count, and whether it is a
// group. Each opens its own details, as the panel's other links do.
export function InsideList({
  folder,
  items,
  label,
}: {
  folder: Pick<EntryRow, 'path'>
  items: InsideItem[]
  label: string
}) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const entryLink = useEntryLink()

  if (items.length === 0) {
    return <p className="text-sm text-muted-foreground">{t('detail.insideNone')}</p>
  }
  // Paths read from the folder down; the top folder's path is empty.
  const prefix = folder.path === '' ? '' : `${folder.path}/`
  return (
    <ul aria-label={label} className="grid gap-2">
      {items.map((item) => (
        <li key={item.entry_id} className="grid gap-0.5 text-sm">
          <span className="flex min-w-0 items-center gap-2">
            <span
              aria-hidden="true"
              className="size-2.5 shrink-0 rounded-sm"
              // React sets this through the CSSOM, which the strict CSP allows.
              style={{ backgroundColor: item.family === null ? neutralColor : familyColors[item.family] }}
            />
            <Link to={{ search: entryLink(item.entry_id) }} className="break-all text-primary hover:underline">
              {item.path.startsWith(prefix) ? item.path.slice(prefix.length) : item.path}
            </Link>
          </span>
          <span className="pl-4.5 text-muted-foreground">
            {[
              t(`entry.category.${item.category ?? 'unknown'}`),
              fmt.bytes(item.bytes),
              t('units.files', { count: item.files, formatted: fmt.count(item.files) }),
              ...(item.group ? [t('detail.insideGroup')] : []),
            ].join(' · ')}
          </span>
        </li>
      ))}
    </ul>
  )
}
