import { useTranslation } from 'react-i18next'

import type { Copy, Coverage } from '@/api/content'
import type { EntryRow } from '@/api/entries'
import { CoverageClaim } from '@/components/CoverageFigures'
import { BarList, type Bar } from '@/home/BarList'
import { useFormat } from '@/lib/format'
import { placeAmountText, placeParts, unreadableText } from '@/lib/places'
import { placeColors } from '@/map/colors'

// The detail panel's places (R5c design D5, D8, D14): where the copies of
// an entry's content are, in words. The fields come only with places=1,
// which the panel asks for in the Map's Only here mode; a missing or
// quarantined entry has no place (null) and shows none.

// sumPlaces are the places a folder's figures list, in the Map legend's
// order; disposable files follow on their own line.
const sumPlaces = ['only_copy', 'this_source', 'other_source', 'unchecked', 'unreadable'] as const

// FolderPlaces shows a folder's or complete archive's (listed) files and
// bytes per place and its disposable files, with the checked share of
// every source. The sums are null while the figures are not computed yet;
// an archive's are its members' unpacked sizes, which it says, followed by
// the split of its size on disk that its row shows in its folder. A folder
// that could not be read says so in its row's words, never that it holds
// nothing (I7).
export function FolderPlaces({
  entry,
  listed,
  updating,
  coverage,
}: {
  entry: EntryRow
  listed: boolean
  updating: boolean
  coverage: Coverage
}) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const sums = (listed ? entry.member_places : entry.places) ?? null
  const bars: Bar[] =
    sums === null
      ? []
      : [...sumPlaces, 'disposable' as const]
          .filter((place) => sums[place].files > 0 || sums[place].bytes > 0)
          .map((place) => ({
            key: place,
            label: t(`detail.places.${place}`),
            bytes: sums[place].bytes,
            files: sums[place].files,
            color: placeColors[place],
          }))
  const whole = bars.reduce((sum, bar) => sum + bar.bytes, 0)
  const unreadable = entry.place === 'unreadable' ? unreadableText(entry, t) : null
  const split = listed ? placeParts(entry.places) : []

  return (
    <>
      {listed && <p className="text-sm">{t('detail.places.unpacked')}</p>}
      {unreadable !== null && <p className="text-sm font-medium">{unreadable}</p>}
      {sums === null ? (
        <p className="text-sm">{t('detail.places.notComputed')}</p>
      ) : bars.length > 0 ? (
        <BarList label={t('detail.places.title')} bars={bars} whole={whole} />
      ) : (
        unreadable === null && <p className="text-sm">{t('detail.places.nothing')}</p>
      )}
      {sums !== null && sums.disposable.files > 0 && (
        <p className="text-xs text-muted-foreground">{t('detail.places.disposableHelp')}</p>
      )}
      {split.length > 0 && (
        <p className="text-sm">
          {t('detail.places.split', {
            size: fmt.bytes(entry.size),
            parts: fmt.list(split.map((part) => placeAmountText(part.key, part.bytes, t, fmt))),
          })}
        </p>
      )}
      {updating && <p className="text-sm font-medium">{t('detail.places.updating')}</p>}
      <CoverageClaim coverage={coverage} />
    </>
  )
}

// FilePlace states a file's or member's place in one sentence, above its
// copies. others are the copies listed below and otherCount all of them, so
// that the sentence agrees with the list: a member placed by its archive's
// copy (D6) has no copy of its own to list, and says it is the archive's.
export function FilePlace({ entry, others, otherCount }: { entry: EntryRow; others: Copy[]; otherCount: number }) {
  const { t } = useTranslation()
  const place = entry.place
  if (place === undefined || place === null || place === 'not_computed') {
    return null
  }
  const offline = entry.place_offline === true
  // A member's own copies decide its place unless none of them can: then
  // its archive's copy does.
  const member = entry.archive_id !== null
  const ownOtherSource = others.some((copy) => copy.source_id !== entry.source_id) || otherCount > others.length
  let text
  switch (place) {
    case 'only_copy':
      text = otherCount > 0 ? t('detail.places.file.only_copy_names') : t('detail.places.file.only_copy')
      break
    case 'this_source':
      text =
        member && otherCount === 0
          ? t('detail.places.file.archive_this_source')
          : t('detail.places.file.this_source', { count: otherCount })
      break
    case 'other_source':
      if (member && !ownOtherSource) {
        text = offline
          ? t('detail.places.file.archive_other_source_offline')
          : t('detail.places.file.archive_other_source')
      } else {
        text = offline ? t('detail.places.file.other_source_offline') : t('detail.places.file.other_source')
      }
      break
    case 'unchecked':
      text = t('detail.places.file.unchecked')
      break
    case 'unreadable':
      text = t('detail.places.file.unreadable')
      break
    case 'none':
      text =
        entry.kind === 'symlink'
          ? t('detail.places.file.link')
          : entry.kind === 'file'
            ? t('detail.places.file.empty')
            : t('detail.places.file.special')
      break
  }
  return <p className="text-sm font-medium">{text}</p>
}
