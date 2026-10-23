import { useTranslation } from 'react-i18next'

import type { PlaceSums } from '@/api/entries'
import { useFormat } from '@/lib/format'
import { placeAmountText, placeParts } from '@/lib/places'
import { cn } from '@/lib/utils'
import { placeColors } from '@/map/colors'

// PlaceBar draws a folder's bytes by place as one stacked bar, in the
// treemap's places colors (R5c design D8). Its text alternative says every
// share in words. It draws nothing for a folder with no bytes.
export function PlaceBar({ sums, className }: { sums: PlaceSums | null | undefined; className?: string }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const parts = placeParts(sums)
  if (parts.length === 0) {
    return null
  }
  const total = parts.reduce((sum, part) => sum + part.bytes, 0)
  const text = fmt.list(parts.map((part) => placeAmountText(part.key, part.bytes, t, fmt)))
  return (
    <div
      role="img"
      aria-label={text}
      title={text}
      className={cn('flex h-1.5 w-full overflow-hidden rounded-full bg-muted', className)}
    >
      {parts.map((part) => (
        <div
          key={part.key}
          data-place={part.key}
          className="h-full"
          // React sets these through the CSSOM, which the strict CSP allows.
          style={{ width: `${(part.bytes / total) * 100}%`, backgroundColor: placeColors[part.key] }}
        />
      ))}
    </div>
  )
}
