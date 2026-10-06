import { useSearchParams } from 'react-router'

import { useSources } from '@/api/sources'

// useSourceParam reads the source a screen is limited to (?source=), or
// null for every source.
export function useSourceParam(): string | null {
  const [params] = useSearchParams()
  const source = params.get('source')
  return source === '' ? null : source
}

// useSourceLabel returns the label of a source by its ID, or the ID while
// the sources are loading.
export function useSourceLabel(): (id: string) => string {
  const sources = useSources()
  return (id) => sources.data?.sources.find((s) => s.id === id)?.label ?? id
}
