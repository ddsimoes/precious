import { useSyncExternalStore } from 'react'

// The first side the owner chose with "Compare with…" (R2 design D11),
// remembered while they find the second folder on the Map or in Search. It
// lives in the tab's session storage, so a reload keeps it.

export interface CompareChoice {
  id: string
  name: string
}

const storageKey = 'precious.compareFirst'
const listeners = new Set<() => void>()

function load(): CompareChoice | null {
  try {
    const stored = sessionStorage.getItem(storageKey)
    return stored === null ? null : (JSON.parse(stored) as CompareChoice)
  } catch {
    return null
  }
}

let current: CompareChoice | null = load()

// chooseCompareFirst remembers the first side, or forgets it for null.
export function chooseCompareFirst(choice: CompareChoice | null) {
  current = choice
  try {
    if (choice === null) {
      sessionStorage.removeItem(storageKey)
    } else {
      sessionStorage.setItem(storageKey, JSON.stringify(choice))
    }
  } catch {
    // Without storage the choice lasts until the page reloads.
  }
  for (const listener of listeners) {
    listener()
  }
}

export function useCompareChoice(): CompareChoice | null {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener)
      return () => listeners.delete(listener)
    },
    () => current,
  )
}
