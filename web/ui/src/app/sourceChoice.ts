import { useSyncExternalStore } from 'react'

// The source the owner last chose on Home, Opportunities, Search, or the
// Map (r2b design D9), used by those screens and by the Map's start until the
// owner chooses another one or all sources (null). It lives in the browser's
// local storage, so it outlasts the tab; a source in the address wins.

const storageKey = 'precious.source'
const listeners = new Set<() => void>()

function load(): string | null {
  try {
    return localStorage.getItem(storageKey)
  } catch {
    return null
  }
}

let current: string | null = load()

// rememberSource remembers the chosen source, or all sources for null.
export function rememberSource(id: string | null) {
  if (id === current) {
    return
  }
  current = id
  try {
    if (id === null) {
      localStorage.removeItem(storageKey)
    } else {
      localStorage.setItem(storageKey, id)
    }
  } catch {
    // Without storage the choice lasts until the page reloads.
  }
  for (const listener of listeners) {
    listener()
  }
}

export function useRememberedSource(): string | null {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener)
      return () => listeners.delete(listener)
    },
    () => current,
  )
}
