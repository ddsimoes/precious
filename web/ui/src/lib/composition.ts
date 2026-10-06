import type { TFunction } from 'i18next'

import { families } from '@/api/entries'
import type { Family, FamilyAmount } from '@/api/home'
import type { Formatters } from '@/lib/format'

// A folder's composition is its bytes and files by family, from its content
// (design D21). These helpers read it for the bar, the share label, and the
// treemap colors.

// mixedThreshold is the fraction of a folder's bytes outside its dominant
// family from which the folder reads as mixed.
const mixedThreshold = 0.01

// compositionParts lists the families of a composition that hold bytes, in
// the order of families.
export function compositionParts(composition: readonly FamilyAmount[] | undefined): FamilyAmount[] {
  return families.flatMap((family) => {
    const amount = composition?.find((a) => a.family === family)
    return amount !== undefined && amount.bytes > 0 ? [amount] : []
  })
}

// dominantFamily is the family holding the most bytes; ties go in the order
// personal, programs, disposable, containers. It is null for a missing or
// empty composition.
export function dominantFamily(composition: readonly FamilyAmount[] | undefined): Family | null {
  let best: FamilyAmount | null = null
  for (const family of families) {
    const amount = composition?.find((a) => a.family === family)
    if (amount !== undefined && (best === null || amount.bytes > best.bytes)) {
      best = amount
    }
  }
  return best?.family ?? null
}

export interface Share {
  family: Family
  fraction: number
}

// mixedShare is the dominant family's share of the bytes when other
// families hold at least 1% of them, and null otherwise.
export function mixedShare(composition: readonly FamilyAmount[] | undefined): Share | null {
  const total = (composition ?? []).reduce((sum, a) => sum + a.bytes, 0)
  const family = dominantFamily(composition)
  if (total === 0 || family === null) {
    return null
  }
  const bytes = composition?.find((a) => a.family === family)?.bytes ?? 0
  if (total - bytes < total * mixedThreshold) {
    return null
  }
  return { family, fraction: bytes / total }
}

// withShare adds the dominant family's share to a category label when the
// composition is mixed: "Personal media · 98% personal".
export function withShare(
  label: string,
  composition: readonly FamilyAmount[] | undefined,
  t: TFunction,
  fmt: Formatters,
): string {
  const share = mixedShare(composition)
  if (share === null) {
    return label
  }
  return t('composition.withShare', {
    label,
    share: t('composition.mostly', {
      percent: fmt.percent(share.fraction),
      family: t(`composition.familyShort.${share.family}`),
    }),
  })
}

// shareLabel names a family with its share of the bytes: "Personal and
// valuable 98%". A share that would round to 0% or 100% reads as less than
// 1% or more than 99%.
export function shareLabel(amount: FamilyAmount, total: number, t: TFunction, fmt: Formatters): string {
  const fraction = total === 0 ? 0 : amount.bytes / total
  let percent = fmt.percent(fraction)
  if (fraction > 0 && fraction < mixedThreshold) {
    percent = t('composition.underOne', { percent: fmt.percent(mixedThreshold) })
  } else if (fraction < 1 && fraction > 1 - mixedThreshold) {
    percent = t('composition.almostAll', { percent: fmt.percent(1 - mixedThreshold) })
  }
  return t('composition.share', { family: t(`home.family.${amount.family}`), percent })
}

// compositionText reads a composition as a list of shares, the text
// alternative of its bar: "By category: Personal and valuable 98%, …".
export function compositionText(parts: readonly FamilyAmount[], t: TFunction, fmt: Formatters): string {
  const total = parts.reduce((sum, a) => sum + a.bytes, 0)
  return t('composition.bar', { shares: fmt.list(parts.map((a) => shareLabel(a, total, t, fmt))) })
}
