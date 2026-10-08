import { useState } from 'react'

import { planRestore } from '@/api/cleanup'
import { useOrganize, type Organize } from '@/organize/useOrganize'

export interface Restore {
  organize: Organize
  // restore plans quarantined items back to their original places.
  restore: (entryIds: string[]) => void
  // chooseDestination plans the same items again, the ones whose place is
  // taken or gone going into the chosen folder.
  chooseDestination: (destinationId: string) => void
}

// useRestore plans a restore and keeps it for the preview (R4 design D6),
// remembering its items so that a conflict can be planned again with a
// destination.
export function useRestore(): Restore {
  const organize = useOrganize()
  const [entryIds, setEntryIds] = useState<string[]>([])
  return {
    organize,
    restore: (ids) => {
      setEntryIds(ids)
      organize.start((csrfToken) => planRestore({ entry_ids: ids }, null, csrfToken), { always: true })
    },
    chooseDestination: (destinationId) =>
      organize.start((csrfToken) => planRestore({ entry_ids: entryIds }, destinationId, csrfToken), { always: true }),
  }
}
