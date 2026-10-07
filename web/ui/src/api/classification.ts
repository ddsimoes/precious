import type { QueryClient } from '@tanstack/react-query'

import type { DecisionTargets } from '@/api/decisions'
import { entriesQueryRoot, type Category } from '@/api/entries'
import { searchQueryRoot } from '@/api/search'
import { postCommand } from '@/app/api'

// The owner's category overrides and group marks (r2b design D5; commands
// set-category and set-group). 'rules' gives the value back to the rules.

export type CategoryChoice = Category | 'rules'

export type GroupChoice = boolean | 'rules'

export interface OverrideResult {
  applied: number
  // scan is the scan of the targets' disk that brings the folder figures up
  // to date; null when the disk is not connected, whose next scan does it.
  scan: { job_id: string; coalesced: boolean } | null
}

export function setCategory(
  targets: DecisionTargets,
  category: CategoryChoice,
  csrfToken: string,
): Promise<OverrideResult> {
  return postCommand<OverrideResult>('set-category', { ...targets, category }, csrfToken)
}

export function setGroup(targets: DecisionTargets, group: GroupChoice, csrfToken: string): Promise<OverrideResult> {
  return postCommand<OverrideResult>('set-group', { ...targets, group }, csrfToken)
}

// refreshAfterOverride refetches what an override changes at once: the
// entries (the target's own classification) and search results. Folder
// figures follow when the scan ends, which refetches them again.
export async function refreshAfterOverride(queryClient: QueryClient) {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: entriesQueryRoot }),
    queryClient.invalidateQueries({ queryKey: searchQueryRoot }),
  ])
}
