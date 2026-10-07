import type { Ancestor } from '@/api/entries'

export interface PathStep {
  // id is the deepest folder of the step, which its link opens.
  id: string
  name: string
  current: boolean
}

// pathSteps turns a folder's ancestors and the folder into the steps of its
// path (r2b design D9). The source's top is a step of its own, labelled with
// the source; below it, a folder that holds nothing but the next one is
// joined with it, so a chain of single folders is one step named by their
// names joined with '/', which opens the deepest of them.
export function pathSteps(ancestors: Ancestor[], folder: { id: string; name: string }, rootLabel: string): PathStep[] {
  const items = [
    ...ancestors.map((a) => ({ id: a.id, name: a.name, onlyChild: a.only_child })),
    { ...folder, onlyChild: false },
  ]
  const steps: PathStep[] = []
  items.forEach((item, index) => {
    const current = index === items.length - 1
    const last = steps.at(-1)
    if (index >= 2 && items[index - 1]?.onlyChild === true && last !== undefined) {
      steps[steps.length - 1] = { id: item.id, name: `${last.name}/${item.name}`, current }
    } else {
      steps.push({ id: item.id, name: index === 0 ? rootLabel : item.name, current })
    }
  })
  return steps
}
