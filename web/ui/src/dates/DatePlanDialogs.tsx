import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { defaultTemplate, planDateOrganize, planSetMtime } from '@/api/dates'
import { maxBulkIds } from '@/api/decisions'
import { Button } from '@/components/ui/button'
import { Dialog } from '@/components/ui/dialog'
import { FormControl, FormField, FormLabel } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import { choiceTargets, initialChoice, type ChosenFolder } from '@/dates/targets'
import { TargetsField } from '@/dates/TargetsField'
import { useFormat } from '@/lib/format'
import { FolderChooser } from '@/organize/FolderChooser'
import type { Organize } from '@/organize/useOrganize'

interface PlanDialogProps {
  // organize plans the action and shows its preview on the screen.
  organize: Organize
  selected: string[]
  within: ChosenFolder | null
  sourceId: string
  onClose: () => void
}

// SetFileDatesDialog plans setting the modification time of the chosen
// photos and videos to their dates (R5 design D14). The plan always opens
// the preview: nothing changes before the owner confirms it.
export function SetFileDatesDialog({ organize, selected, within, sourceId, onClose }: PlanDialogProps) {
  const { t } = useTranslation()
  const [choice, setChoice] = useState(() => initialChoice(selected.length, within))
  const targets = choiceTargets(choice, selected)
  const tooMany = choice.mode === 'selected' && selected.length > maxBulkIds

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (targets === null) {
      return
    }
    organize.start((csrfToken) => planSetMtime(targets, csrfToken), {
      always: true,
      overrides: { invalid_request: t('dates.setFileDates.tooMany') },
    })
    onClose()
  }

  return (
    <Dialog title={t('dates.setFileDates.title')} description={t('dates.setFileDates.help')} onClose={onClose}>
      <form className="grid gap-4" onSubmit={submit}>
        <TargetsField choice={choice} onChange={setChoice} selected={selected.length} sourceId={sourceId} />
        <TooMany shown={tooMany} />
        <div className="flex justify-end gap-2">
          <Button type="button" variant="outline" onClick={onClose}>
            {t('dates.setFileDates.cancel')}
          </Button>
          <Button type="submit" disabled={targets === null || tooMany}>
            {t('dates.setFileDates.preview')}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

// OrganizeByDateDialog plans moving the chosen photos and videos into
// folders named by a template below a destination, optionally renaming
// them to their date and time (R5 design D16). The preview always opens.
export function OrganizeByDateDialog({ organize, selected, within, sourceId, onClose }: PlanDialogProps) {
  const { t } = useTranslation()
  const [choice, setChoice] = useState(() => initialChoice(selected.length, within))
  const [template, setTemplate] = useState(defaultTemplate)
  const [destination, setDestination] = useState<ChosenFolder | null>(null)
  const [rename, setRename] = useState(false)
  const [choosing, setChoosing] = useState(false)
  const targets = choiceTargets(choice, selected)
  const tooMany = choice.mode === 'selected' && selected.length > maxBulkIds

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (targets === null || destination === null) {
      return
    }
    const options = { destinationId: destination.id, template: template.trim(), rename }
    organize.start((csrfToken) => planDateOrganize(targets, options, csrfToken), {
      always: true,
      overrides: { invalid_request: t('dates.organize.invalid') },
    })
    onClose()
  }

  return (
    <>
      <Dialog title={t('dates.organize.title')} description={t('dates.organize.help')} onClose={onClose}>
        <form className="grid gap-4" onSubmit={submit}>
          <TargetsField choice={choice} onChange={setChoice} selected={selected.length} sourceId={sourceId} />
          <TooMany shown={tooMany} />
          <FormField>
            <FormLabel>{t('dates.organize.template')}</FormLabel>
            <FormControl>
              <Input value={template} onChange={(event) => setTemplate(event.target.value)} />
            </FormControl>
            <p className="text-sm text-muted-foreground">{t('dates.organize.templateHelp')}</p>
          </FormField>
          <div className="grid gap-2 text-sm">
            <p className="font-medium">{t('dates.organize.destination')}</p>
            <p className="break-all">{destination?.path ?? t('dates.organize.noDestination')}</p>
            <div>
              <Button type="button" size="sm" variant="outline" onClick={() => setChoosing(true)}>
                {t('dates.organize.choose')}
              </Button>
            </div>
          </div>
          <label className="flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              className="mt-1"
              checked={rename}
              onChange={(event) => setRename(event.target.checked)}
            />
            {t('dates.organize.rename')}
          </label>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="outline" onClick={onClose}>
              {t('dates.organize.cancel')}
            </Button>
            <Button type="submit" disabled={targets === null || destination === null || tooMany}>
              {t('dates.organize.preview')}
            </Button>
          </div>
        </form>
      </Dialog>
      {choosing && (
        <FolderChooser
          title={t('dates.organize.chooserTitle')}
          sourceIds={[sourceId]}
          chooseLabel={t('dates.organize.chooserHere')}
          onChoose={(folder, trail) => {
            setChoosing(false)
            setDestination({ id: folder.id, path: trail.map((f) => f.name).join(' / ') })
          }}
          onClose={() => setChoosing(false)}
        />
      )}
    </>
  )
}

function TooMany({ shown }: { shown: boolean }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  return shown ? (
    <p className="text-sm text-muted-foreground">{t('dates.list.tooMany', { formatted: fmt.count(maxBulkIds) })}</p>
  ) : null
}
