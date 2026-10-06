import type { ReactNode } from 'react'

// PageTitle is the heading of a signed-in screen.
export function PageTitle({ children }: { children: ReactNode }) {
  return <h1 className="mb-4 text-2xl font-semibold">{children}</h1>
}
