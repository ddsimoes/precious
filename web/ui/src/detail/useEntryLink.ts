import { useCallback } from 'react'
import { useSearchParams } from 'react-router'

// useEntryLink returns the query string that opens the detail panel on an
// entry (?entry=<id>), or closes it for null, on the current screen with
// its other parameters kept.
export function useEntryLink(): (id: string | null) => string {
  const [params] = useSearchParams()
  return useCallback(
    (id) => {
      const next = new URLSearchParams(params)
      if (id === null) {
        next.delete('entry')
      } else {
        next.set('entry', id)
      }
      const query = next.toString()
      return query === '' ? '' : `?${query}`
    },
    [params],
  )
}
