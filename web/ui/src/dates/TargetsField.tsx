import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { maxFolders } from '@/api/dates'
import { Button } from '@/components/ui/button'
import type { TargetChoice } from '@/dates/targets'
import { useFormat } from '@/lib/format'
import { FolderChooser } from '@/organize/FolderChooser'

// TargetsField chooses between the selected photos and videos and a list of
// folders, added with the destination chooser, of one source (R5 design
// D11's targets).
export function TargetsField({
  choice,
  onChange,
  selected,
  sourceId,
}: {
  choice: TargetChoice
  onChange: (choice: TargetChoice) => void
  selected: number
  sourceId: string
}) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const name = useId()
  const [choosing, setChoosing] = useState(false)
  const tooMany = choice.folders.length >= maxFolders

  return (
    <fieldset className="grid gap-2 text-sm">
      <legend className="mb-1 font-medium">{t('dates.targets.label')}</legend>
      <label className="flex items-center gap-2">
        <input
          type="radio"
          name={name}
          checked={choice.mode === 'selected'}
          disabled={selected === 0}
          onChange={() => onChange({ ...choice, mode: 'selected' })}
        />
        {t('dates.targets.selected', { count: selected, formatted: fmt.count(selected) })}
      </label>
      <label className="flex items-center gap-2">
        <input
          type="radio"
          name={name}
          checked={choice.mode === 'folders'}
          onChange={() => onChange({ ...choice, mode: 'folders' })}
        />
        {t('dates.targets.folders')}
      </label>
      {choice.mode === 'folders' && (
        <div className="grid gap-2 pl-6">
          {choice.folders.length === 0 ? (
            <p className="text-muted-foreground">{t('dates.targets.none')}</p>
          ) : (
            <ul aria-label={t('dates.targets.foldersList')} className="grid gap-1">
              {choice.folders.map((folder) => (
                <li key={folder.id} className="flex items-center justify-between gap-2 rounded-md border px-2 py-1">
                  <span className="break-all">{folder.path}</span>
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    aria-label={t('dates.targets.remove', { path: folder.path })}
                    onClick={() =>
                      onChange({ ...choice, folders: choice.folders.filter((f) => f.id !== folder.id) })
                    }
                  >
                    ×
                  </Button>
                </li>
              ))}
            </ul>
          )}
          <div>
            <Button type="button" size="sm" variant="outline" disabled={tooMany} onClick={() => setChoosing(true)}>
              {t('dates.targets.add')}
            </Button>
          </div>
          {tooMany && (
            <p className="text-muted-foreground">{t('dates.targets.tooMany', { formatted: fmt.count(maxFolders) })}</p>
          )}
        </div>
      )}
      {choosing && (
        <FolderChooser
          title={t('dates.targets.chooserTitle')}
          sourceIds={[sourceId]}
          chooseLabel={t('dates.targets.chooserHere')}
          onChoose={(folder, trail) => {
            setChoosing(false)
            if (!choice.folders.some((f) => f.id === folder.id)) {
              onChange({
                mode: 'folders',
                folders: [...choice.folders, { id: folder.id, path: trail.map((f) => f.name).join(' / ') }],
              })
            }
          }}
          onClose={() => setChoosing(false)}
        />
      )}
    </fieldset>
  )
}
