import { useTranslation } from 'react-i18next'

import type { FamilyAmount } from '@/api/home'
import { compositionParts, compositionText } from '@/lib/composition'
import { useFormat } from '@/lib/format'
import { cn } from '@/lib/utils'
import { familyColors } from '@/map/colors'

// CompositionBar draws a folder's bytes by family as one stacked bar, in the
// treemap's family colors. Its text alternative lists the shares. It draws
// nothing for a missing or empty composition.
export function CompositionBar({
  composition,
  className,
}: {
  composition: readonly FamilyAmount[] | undefined
  className?: string
}) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const parts = compositionParts(composition)
  if (parts.length === 0) {
    return null
  }
  const total = parts.reduce((sum, a) => sum + a.bytes, 0)
  const text = compositionText(parts, t, fmt)
  return (
    <div
      role="img"
      aria-label={text}
      title={text}
      className={cn('flex h-1.5 w-full overflow-hidden rounded-full bg-muted', className)}
    >
      {parts.map((a) => (
        <div
          key={a.family}
          data-family={a.family}
          className="h-full"
          // React sets these through the CSSOM, which the strict CSP allows.
          style={{ width: `${(a.bytes / total) * 100}%`, backgroundColor: familyColors[a.family] }}
        />
      ))}
    </div>
  )
}
