import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'

import type { Card } from '@/api/opportunities'
import { useFormat } from '@/lib/format'
import { cardFigures } from '@/opportunities/cardFigures'

// CardList ranks the opportunity cards as the server does (R2 design D12,
// r2c D3): the rescue card first while it has open rows, then largest
// first. Each shows its bytes, its open rows and those decided (r2b D13),
// and what it rests on, and opens its review list for the same source.
export function CardList({ cards, source }: { cards: Card[]; source: string | null }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  if (cards.length === 0) {
    return <p className="text-sm text-muted-foreground">{t('opportunities.none')}</p>
  }
  const ranked = [...cards].sort(
    (a, b) =>
      Number(b.list === 'rescue' && b.rows > 0) - Number(a.list === 'rescue' && a.rows > 0) || b.bytes - a.bytes,
  )
  const search = source === null ? '' : `?${new URLSearchParams({ source })}`
  return (
    <ul aria-label={t('opportunities.cards')} className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
      {ranked.map((card) => {
        const figures = cardFigures(card, t, fmt)
        return (
          <li key={card.list} className="grid content-start gap-1 rounded-lg border bg-card p-3 text-sm">
            <Link
              to={{ pathname: `/opportunities/${card.list}`, search }}
              className="font-semibold text-primary hover:underline"
            >
              {t(`opportunities.list.${card.list}`)}
            </Link>
            <span className="text-2xl font-semibold">{figures.headline}</span>
            {(figures.rows !== null || figures.decided !== null) && (
              <span>{[figures.rows, figures.decided].filter((s) => s !== null).join(' · ')}</span>
            )}
            <span className="text-muted-foreground">{t(`opportunities.help.${card.list}`)}</span>
            <span className="text-xs text-muted-foreground">{t(`opportunities.basis.${card.basis}`)}</span>
          </li>
        )
      })}
    </ul>
  )
}
