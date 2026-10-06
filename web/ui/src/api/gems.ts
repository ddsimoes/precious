import { gemsQueryRoot, type Coverage, type Relation } from '@/api/content'
import type { EntryRow } from '@/api/entries'
import type { SelectableList } from '@/api/opportunities'
import { apiGet } from '@/app/api'

// Gems (R2 design D14; GET /api/gems): what is valuable, in three sections.

// GemSection is one section: personal files with no other copy (unique),
// personal material inside program or disposable groups (rescue), and
// files with no other copy found only on one side of two overlapping
// folders (only_in_copy).
export type GemSection = 'unique' | 'rescue' | 'only_in_copy'

export const gemSections: GemSection[] = ['unique', 'rescue', 'only_in_copy']

// gemLists names the review list select-list resolves for each section.
export const gemLists: Record<GemSection, SelectableList> = {
  unique: 'gems_unique',
  rescue: 'gems_rescue',
  only_in_copy: 'gems_only_in_copy',
}

// Gem is one item: the file, the group it sits in (rescue), or the relation
// it sits on one side of (only_in_copy).
export interface Gem {
  entry: EntryRow
  group: EntryRow | null
  relation: Relation | null
}

export interface GemsPage {
  section: GemSection
  items: Gem[]
  next_cursor: string | null
  coverage: Coverage
}

export function gemsQueryKey(section: GemSection, source: string | null) {
  return [...gemsQueryRoot, section, source] as const
}

export function fetchGems(
  section: GemSection,
  source: string | null,
  cursor: string | null,
  signal?: AbortSignal,
): Promise<GemsPage> {
  const params = new URLSearchParams({ section })
  if (source !== null) {
    params.set('source', source)
  }
  if (cursor !== null) {
    params.set('cursor', cursor)
  }
  return apiGet<GemsPage>(`/api/gems?${params}`, signal)
}
