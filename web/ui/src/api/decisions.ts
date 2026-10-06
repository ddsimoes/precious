import { useMutation, useQueryClient, type QueryClient } from '@tanstack/react-query'

import { duplicatesQueryRoots } from '@/api/content'
import type { Decision } from '@/api/home'
import type { SelectionQuery } from '@/api/search'
import { postCommand } from '@/app/api'
import { useCsrfToken } from '@/app/session'

// Owner decisions and selections (design D10; commands set-decision and
// create-selection).

// DecisionChoice is a value set-decision accepts; inherit clears the own
// decision.
export type DecisionChoice = Decision | 'inherit'

export const decisionChoices: DecisionChoice[] = ['inherit', 'undecided', 'keep', 'discard', 'later']

export interface SkippedEntry {
  entry_id: string
  path: string
  path_b64: string
}

export interface SetDecisionResult {
  applied: number
  // skipped_count counts the kept entries a bulk request left alone;
  // skipped lists up to 100 of them.
  skipped_count: number
  skipped: SkippedEntry[]
}

// DecisionTargets names the entries of a set-decision request: one entry
// (an individual request, which may change a keep) or explicit IDs or a
// selection (a bulk request, which skips kept entries unless it sets keep).
export type DecisionTargets = { entry_id: string } | { entry_ids: string[] } | { selection_id: string }

// maxBulkIds is the most entry_ids one request may carry.
export const maxBulkIds = 1000

export interface Selection {
  selection_id: string
  count: number
  bytes: number
  // kept counts the selected entries whose effective decision is keep.
  kept: { count: number; bytes: number }
  expires_at: string
}

export function setDecision(
  targets: DecisionTargets,
  decision: DecisionChoice,
  csrfToken: string,
): Promise<SetDecisionResult> {
  return postCommand<SetDecisionResult>('set-decision', { ...targets, decision }, csrfToken)
}

export function createSelection(query: SelectionQuery, csrfToken: string): Promise<Selection> {
  return postCommand<Selection>('create-selection', { query }, csrfToken)
}

// refreshAfterDecision refetches what a decision makes stale: every entry
// (effective decisions change across the subtree), search results (the
// decision filter), Home's decision totals and cards, and the opportunity
// cards, review lists, Gems, and Compare, whose open rows and decision
// controls follow the decisions live.
export async function refreshAfterDecision(queryClient: QueryClient) {
  await Promise.all(duplicatesQueryRoots.map((queryKey) => queryClient.invalidateQueries({ queryKey })))
}

// useDecide sets one entry's own decision with an individual request, which
// applies even to a kept entry, as the detail panel does, and refreshes
// every screen the decision changes.
export function useDecide() {
  const queryClient = useQueryClient()
  const csrfToken = useCsrfToken()
  return useMutation({
    mutationFn: ({ id, choice }: { id: string; choice: DecisionChoice }) =>
      setDecision({ entry_id: id }, choice, csrfToken),
    onSuccess: () => refreshAfterDecision(queryClient),
  })
}
