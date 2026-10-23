import type { EntryRow } from '@/api/entries'

// filePlaced is true for an entry that is not a folder and has a place of
// its own, which the panel states above its copies (R5c design D1). The
// place fields come only with places=1; a missing or quarantined entry has
// no place. A complete archive is placed by its members (D5), which the
// panel shows as figures instead.
export function filePlaced(entry: EntryRow): boolean {
  return (
    entry.kind !== 'directory' &&
    entry.archive_state !== 'complete' &&
    entry.place !== undefined &&
    entry.place !== null &&
    entry.place !== 'not_computed'
  )
}
