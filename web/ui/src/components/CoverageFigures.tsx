import { useTranslation } from 'react-i18next'

import { checkedShare, type Coverage } from '@/api/content'
import { useFormat } from '@/lib/format'

// CoverageFigures shows how much of what could have a copy was checked
// (R2 design D8): the checked bytes out of the candidate bytes, and the
// files not checked yet or not readable.
export function CoverageFigures({ coverage }: { coverage: Coverage }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  if (coverage.candidate.files === 0) {
    return <p className="text-sm text-muted-foreground">{t('coverage.nothing')}</p>
  }
  const share = checkedShare(coverage)
  return (
    <div className="grid gap-2 text-sm">
      <p className="font-medium">
        {t('coverage.share', {
          checked: fmt.bytes(coverage.checked.bytes),
          candidate: fmt.bytes(coverage.candidate.bytes),
          percent: fmt.percent(share),
        })}
      </p>
      <div aria-hidden="true" className="h-2 overflow-hidden rounded-full bg-muted">
        {/* React sets this through the CSSOM, which the strict CSP allows. */}
        <div className="h-full rounded-full bg-primary" style={{ width: `${share * 100}%` }} />
      </div>
      <ul className="grid gap-0.5 text-muted-foreground">
        <li>
          {t('coverage.unchecked', {
            count: coverage.unchecked.files,
            formatted: fmt.count(coverage.unchecked.files),
            bytes: fmt.bytes(coverage.unchecked.bytes),
          })}
        </li>
        <li>
          {t('coverage.unreadable', {
            count: coverage.unreadable.files,
            formatted: fmt.count(coverage.unreadable.files),
            bytes: fmt.bytes(coverage.unreadable.bytes),
          })}
        </li>
      </ul>
      <p className="text-xs text-muted-foreground">{t('coverage.help')}</p>
    </div>
  )
}

// CoverageClaim is the checked share every "no other copy" claim carries
// (I7): a copy can be on any disk, so the share is that of all of them.
export function CoverageClaim({ coverage }: { coverage: Coverage }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  return (
    <p className="text-xs text-muted-foreground">
      {t('coverage.claim', { percent: fmt.percent(checkedShare(coverage)) })}
    </p>
  )
}
