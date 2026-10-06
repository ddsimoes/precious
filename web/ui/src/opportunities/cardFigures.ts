import type { TFunction } from 'i18next'

import type { Card } from '@/api/opportunities'
import type { Formatters } from '@/lib/format'

// cardFigures are what a card's figures read: its bytes as the headline,
// then its open rows. A card whose rows hold no bytes (empty files and
// folders) heads with its item count instead, and its rows line is null.
export function cardFigures(card: Card, t: TFunction, fmt: Formatters): { headline: string; rows: string | null } {
  const formatted = fmt.count(card.rows)
  if (card.bytes === 0 && card.rows > 0) {
    return { headline: t('opportunities.items', { count: card.rows, formatted }), rows: null }
  }
  return { headline: fmt.bytes(card.bytes), rows: t('opportunities.rows', { count: card.rows, formatted }) }
}
