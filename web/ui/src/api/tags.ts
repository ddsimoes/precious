import { useQuery, type QueryClient } from '@tanstack/react-query'

import { entriesQueryRoot } from '@/api/entries'
import { searchQueryRoot } from '@/api/search'
import { apiGet, postCommand } from '@/app/api'

// Owner tags (design Interfaces: GET /api/tags, set-tags, create-tag,
// rename-tag, delete-tag).

export interface Tag {
  id: number
  name: string
  // own_count is the number of entries carrying the tag as their own.
  own_count: number
}

export interface TagsResponse {
  tags: Tag[]
}

export interface TagResult {
  tag: { id: number; name: string }
}

export interface SetTagsResult {
  applied: number
}

// TagTargets names the entries of a set-tags request: explicit IDs or a
// selection.
export type TagTargets = { entry_ids: string[] } | { selection_id: string }

export const tagsQueryKey = ['tags'] as const

// useTags reads the tag list; enabled = false defers it until a screen
// needs it.
export function useTags(enabled = true) {
  return useQuery({
    queryKey: tagsQueryKey,
    queryFn: ({ signal }) => apiGet<TagsResponse>('/api/tags', signal),
    enabled,
  })
}

export function setTags(
  targets: TagTargets,
  change: { add?: number[]; remove?: number[] },
  csrfToken: string,
): Promise<SetTagsResult> {
  return postCommand<SetTagsResult>(
    'set-tags',
    { ...targets, add: change.add ?? [], remove: change.remove ?? [] },
    csrfToken,
  )
}

export function createTag(name: string, csrfToken: string): Promise<TagResult> {
  return postCommand<TagResult>('create-tag', { name }, csrfToken)
}

export function renameTag(id: number, name: string, csrfToken: string): Promise<TagResult> {
  return postCommand<TagResult>('rename-tag', { tag_id: id, name }, csrfToken)
}

export function deleteTag(id: number, csrfToken: string): Promise<TagResult> {
  return postCommand<TagResult>('delete-tag', { tag_id: id }, csrfToken)
}

// refreshAfterTagChange refetches what a change of tags makes stale: the
// tag list and its counts, every entry (own and inherited tags), and search
// results (the tag filter).
export async function refreshAfterTagChange(queryClient: QueryClient) {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: tagsQueryKey }),
    queryClient.invalidateQueries({ queryKey: entriesQueryRoot }),
    queryClient.invalidateQueries({ queryKey: searchQueryRoot }),
  ])
}
