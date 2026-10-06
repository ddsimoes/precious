import { useTranslation } from 'react-i18next'

import { useFormat } from '@/lib/format'
import { cn } from '@/lib/utils'

export interface Bar {
  key: string
  label: string
  bytes: number
  files: number
  // color paints the bar; the primary color by default.
  color?: string
}

interface BarListProps {
  label: string
  bars: Bar[]
  // whole is the byte count of a full bar: the largest bar by default, or a
  // total the bars are shares of.
  whole?: number
}

// BarList is a horizontal bar chart of bytes as a labeled list: each item
// reads as its label, size, and file count, and its bar is decoration.
export function BarList({ label, bars, whole }: BarListProps) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const full = whole ?? Math.max(0, ...bars.map((bar) => bar.bytes))

  if (bars.length === 0) {
    return <p className="text-sm text-muted-foreground">{t('home.nothing')}</p>
  }
  return (
    <ul aria-label={label} className="grid gap-2">
      {bars.map((bar) => (
        <li key={bar.key} className="grid gap-1 text-sm">
          <div className="flex flex-wrap items-baseline justify-between gap-x-3">
            <span className="font-medium">{bar.label}</span>
            <span className="text-muted-foreground">
              {fmt.bytes(bar.bytes)} · {t('units.files', { count: bar.files, formatted: fmt.count(bar.files) })}
            </span>
          </div>
          <div aria-hidden="true" className="h-2 overflow-hidden rounded-full bg-muted">
            <div
              className={cn('h-full rounded-full', bar.color === undefined && 'bg-primary')}
              // React sets this through the CSSOM, which the strict CSP allows.
              style={{ width: `${full === 0 ? 0 : (bar.bytes / full) * 100}%`, backgroundColor: bar.color }}
            />
          </div>
        </li>
      ))}
    </ul>
  )
}
