import { useState, type FormEvent } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { Link } from 'react-router'

import type { Ancestor, EntryRow } from '@/api/entries'
import { planCreateFolder, planMove, planRename, planRescue } from '@/api/organize'
import { useSources } from '@/api/sources'
import { Button } from '@/components/ui/button'
import { FormControl, FormField, FormLabel } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import { FolderChooser, type Folder } from '@/organize/FolderChooser'
import { OrganizeOutcome } from '@/organize/OrganizeOutcome'
import { useOrganize } from '@/organize/useOrganize'

// OrganizeSection is the detail panel's Organize section (R3 design D16):
// Rename and Move to… for anything but a source's top folder, New folder
// and Rescue kept items… for a folder. A single change without a conflict
// runs at once and offers Undo; anything else opens the preview. On a
// source Precious may not change, it points to the Sources screen.
export function OrganizeSection({
  entry,
  ancestors,
  kept,
  rootLabel,
}: {
  entry: EntryRow
  ancestors: Ancestor[]
  // kept is whether the entry's effective decision is keep.
  kept: boolean
  rootLabel: string
}) {
  const { t } = useTranslation()
  const sources = useSources()
  const organize = useOrganize()
  const [form, setForm] = useState<'rename' | 'folder' | null>(null)
  const [choosing, setChoosing] = useState<'move' | 'rescue' | null>(null)
  const [name, setName] = useState('')
  const source = sources.data?.sources.find((s) => s.id === entry.source_id)
  if (source === undefined) {
    return null
  }
  if (!source.writes.enabled) {
    return (
      <p className="text-sm text-muted-foreground">
        <Trans
          i18nKey="organize.readOnly"
          components={{ sourcesLink: <Link to="/sources" className="text-primary underline" /> }}
        />
      </p>
    )
  }
  if (source.state !== 'online') {
    return <p className="text-sm text-muted-foreground">{t('organize.offline')}</p>
  }

  const folder = entry.kind === 'directory'
  const top = ancestors.length === 0
  // The chooser opens where the entry is, its trail named from the source.
  const here: Folder[] = ancestors.map((a, index) => ({ id: a.id, name: index === 0 ? rootLabel : a.name }))
  const invalidName = { invalid_request: t('organize.invalidName') }

  const openForm = (next: 'rename' | 'folder') => {
    setForm(next)
    setName(next === 'rename' ? entry.name : '')
    organize.dismissError()
  }
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const trimmed = name.trim()
    organize.start(
      (csrfToken) =>
        form === 'rename' ? planRename(entry.id, trimmed, csrfToken) : planCreateFolder(entry.id, trimmed, csrfToken),
      { overrides: invalidName, onPlanned: () => setForm(null) },
    )
  }
  const choose = (destination: Folder) => {
    setChoosing(null)
    setForm(null)
    if (choosing === 'rescue') {
      organize.start((csrfToken) => planRescue(entry.id, destination.id, csrfToken), {
        always: true,
        overrides: { invalid_entry_state: t('organize.nothingToRescue'), invalid_request: t('organize.insideItself') },
      })
    } else {
      organize.start((csrfToken) => planMove({ entry_id: entry.id }, destination.id, csrfToken))
    }
  }

  return (
    <div className="grid gap-2">
      <div className="flex flex-wrap gap-2">
        {!top && (
          <Button size="sm" variant="outline" disabled={organize.pending} onClick={() => openForm('rename')}>
            {t('organize.rename')}
          </Button>
        )}
        {!top && (
          <Button size="sm" variant="outline" disabled={organize.pending} onClick={() => setChoosing('move')}>
            {t('organize.moveTo')}
          </Button>
        )}
        {folder && (
          <Button size="sm" variant="outline" disabled={organize.pending} onClick={() => openForm('folder')}>
            {t('organize.newFolder')}
          </Button>
        )}
        {folder && !kept && (
          <Button size="sm" variant="outline" disabled={organize.pending} onClick={() => setChoosing('rescue')}>
            {t('organize.rescue')}
          </Button>
        )}
      </div>

      {form !== null && (
        <form className="grid gap-2" onSubmit={submit}>
          <FormField>
            <FormLabel>{form === 'rename' ? t('organize.renameLabel') : t('organize.newFolderLabel')}</FormLabel>
            <FormControl>
              <Input name="name" value={name} onChange={(event) => setName(event.target.value)} />
            </FormControl>
          </FormField>
          <div className="flex gap-2">
            <Button
              type="submit"
              size="sm"
              disabled={name.trim() === '' || (form === 'rename' && name.trim() === entry.name) || organize.pending}
            >
              {organize.pending ? t('organize.working') : form === 'rename' ? t('organize.save') : t('organize.create')}
            </Button>
            <Button type="button" size="sm" variant="outline" onClick={() => setForm(null)}>
              {t('organize.cancel')}
            </Button>
          </div>
        </form>
      )}

      <OrganizeOutcome organize={organize} />

      {choosing !== null && (
        <FolderChooser
          title={
            choosing === 'move'
              ? t('organize.chooser.moveTitle', { name: entry.name })
              : t('organize.chooser.rescueTitle', { name: top ? rootLabel : entry.name })
          }
          sourceIds={[source.id]}
          start={here.length > 0 ? here : undefined}
          blocked={[entry.id]}
          onChoose={choose}
          onClose={() => setChoosing(null)}
        />
      )}
    </div>
  )
}
