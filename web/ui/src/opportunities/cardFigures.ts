import type { TFunction } from 'i18next'

import type { Card } from '@/api/opportunities'
import type { Formatters } from '@/lib/format'

// cardFigures are what a card's figures read: its bytes as the headline,
// then its open rows. A card whose rows hold no bytes (empty files and
// folders) heads with its item count instead, and its rows line is null; a
// card with no open row left heads with "Nothing left to review". decided
// counts the rows no longer open, with their bytes when they hold any (r2b
// design D13), and is null before any.
export function cardFigures(
  card: Card,
  t: TFunction,
  fmt: Formatters,
): { headline: string; rows: string | null; decided: string | null } {
  const formatted = fmt.count(card.rows)
  const decided =
    card.decided_rows === 0
      ? null
      : t(card.decided_bytes === 0 ? 'opportunities.decidedRows' : 'opportunities.decided', {
          count: card.decided_rows,
          formatted: fmt.count(card.decided_rows),
          bytes: fmt.bytes(card.decided_bytes),
        })
  if (card.rows === 0) {
    return { headline: t('opportunities.nothingLeft'), rows: null, decided }
  }
  if (card.bytes === 0) {
    return { headline: t('opportunities.items', { count: card.rows, formatted }), rows: null, decided }
  }
  return { headline: fmt.bytes(card.bytes), rows: t('opportunities.rows', { count: card.rows, formatted }), decided }
}
