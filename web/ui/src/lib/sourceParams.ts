import { useSearchParams } from 'react-router'

import { useSources } from '@/api/sources'
import { useRememberedSource } from '@/app/sourceChoice'

// useSourceParam reads the source a screen is limited to, or null for every
// source: ?source= when the address has it (empty for every source), else
// the source the owner last chose (r2b design D9), forgotten once the
// sources are known not to hold it any more.
export function useSourceParam(): string | null {
  const [params] = useSearchParams()
  const remembered = useRememberedSource()
  const sources = useSources()
  const source = params.get('source')
  if (source !== null) {
    return source === '' ? null : source
  }
  if (remembered !== null && sources.data !== undefined && !sources.data.sources.some((s) => s.id === remembered)) {
    return null
  }
  return remembered
}

// useSourceLabel returns the label of a source by its ID, or the ID while
// the sources are loading.
export function useSourceLabel(): (id: string) => string {
  const sources = useSources()
  return (id) => sources.data?.sources.find((s) => s.id === id)?.label ?? id
}
