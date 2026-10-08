import type { BulkTargets } from '@/api/dates'

// ChosenFolder is a folder a date change applies to, with the path shown
// for it from the source's name.
export interface ChosenFolder {
  id: string
  path: string
}

// TargetChoice is what a date change applies to: the photos and videos
// selected on the page, or everything in some folders of the source.
export interface TargetChoice {
  mode: 'selected' | 'folders'
  folders: ChosenFolder[]
}

// initialChoice applies to the selection when there is one, else to the
// folder the list is limited to, if any.
export function initialChoice(selected: number, within: ChosenFolder | null): TargetChoice {
  return { mode: selected > 0 ? 'selected' : 'folders', folders: within === null ? [] : [within] }
}

// choiceTargets is the request's targets, or null while it names nothing.
export function choiceTargets(choice: TargetChoice, selected: string[]): BulkTargets | null {
  if (choice.mode === 'selected') {
    return selected.length === 0 ? null : { entry_ids: selected }
  }
  return choice.folders.length === 0 ? null : { folder_ids: choice.folders.map((folder) => folder.id) }
}
