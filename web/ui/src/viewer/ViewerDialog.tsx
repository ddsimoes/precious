import { useTranslation } from 'react-i18next'

import { contentUrl, type EntryRow } from '@/api/entries'
import { Button } from '@/components/ui/button'
import { Dialog } from '@/components/ui/dialog'
import { Viewer } from '@/viewer/Viewer'

// ViewerDialog opens a file in the viewer, with a download link for every
// type.
export function ViewerDialog({ entry, onClose }: { entry: EntryRow; onClose: () => void }) {
  const { t } = useTranslation()
  return (
    <Dialog title={<span className="break-all">{entry.name}</span>} onClose={onClose} className="max-w-5xl">
      <Viewer entry={entry} />
      <div className="flex justify-end gap-2">
        <Button asChild variant="outline">
          <a href={contentUrl(entry.id)} download={entry.name}>
            {t('viewer.download')}
          </a>
        </Button>
        <Button onClick={onClose}>{t('viewer.close')}</Button>
      </div>
    </Dialog>
  )
}
