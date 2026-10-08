import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  camerasQueryKey,
  datesQueryRoot,
  fetchCameras,
  fetchDates,
  refreshAfterCorrection,
  setDateCorrection,
  shiftLocal,
  type Camera,
  type MediaDate,
  type SetCorrectionResult,
} from '@/api/dates'
import type { Source } from '@/api/sources'
import { ErrorBanner } from '@/app/ErrorBanner'
import { useCsrfToken } from '@/app/session'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Dialog } from '@/components/ui/dialog'
import { useDatesText } from '@/dates/text'
import { useFormat } from '@/lib/format'

// CamerasSection lists a source's cameras (R5 design D8): those whose clock
// looks off first, each with its suggested shift, the folders it was
// compared in, and what shows which clock was right; then those that
// disagree with another camera without anything to tell which is right;
// then the rest. A suggested shift is applied in one confirmation, after a
// preview of the photos it moves.
export function CamerasSection({ source }: { source: Source }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const headingId = useId()
  const [shifting, setShifting] = useState<Camera | null>(null)
  const [shifted, setShifted] = useState<SetCorrectionResult | null>(null)
  const cameras = useQuery({
    queryKey: camerasQueryKey(source.id),
    queryFn: ({ signal }) => fetchCameras(source.id, signal),
  })
  const items = cameras.data?.items ?? []

  return (
    <Card className="p-4">
      <section aria-labelledby={headingId} className="grid gap-3 text-sm">
        <h2 id={headingId} className="text-lg font-semibold">
          {t('dates.cameras.title')}
        </h2>
        <p className="text-muted-foreground">{t('dates.cameras.help')}</p>
        {cameras.isPending && <p className="text-muted-foreground">{t('app.loading')}</p>}
        {cameras.isError && <ErrorBanner error={cameras.error} onRetry={() => void cameras.refetch()} />}
        {shifted !== null && (
          <p role="status" className="font-medium">
            {t('dates.correct.applied', { count: shifted.applied, formatted: fmt.count(shifted.applied) })}
          </p>
        )}
        {cameras.data !== undefined &&
          (items.length === 0 ? (
            <p className="text-muted-foreground">{t('dates.cameras.empty')}</p>
          ) : (
            <ul aria-label={t('dates.cameras.list')} className="grid gap-2">
              {items.map((camera) => (
                <CameraItem
                  key={camera.key}
                  camera={camera}
                  onShift={() => {
                    setShifted(null)
                    setShifting(camera)
                  }}
                />
              ))}
            </ul>
          ))}
      </section>
      {shifting !== null && (
        <ShiftDialog
          camera={shifting}
          onShifted={(result) => {
            setShifting(null)
            setShifted(result)
          }}
          onClose={() => setShifting(null)}
        />
      )}
    </Card>
  )
}

// eventPhotos counts the photos a camera's suggestion shifts: its photos
// directly in the folders of its events.
function eventPhotos(camera: Camera): number {
  return camera.events.reduce((sum, event) => sum + event.photos, 0)
}

function CameraItem({ camera, onShift }: { camera: Camera; onShift: () => void }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const text = useDatesText()
  const headingId = useId()
  const shift = camera.suggested_shift_s
  const photos = eventPhotos(camera)

  return (
    <li>
      <article
        aria-labelledby={headingId}
        className={
          camera.state === 'ok' ? 'grid gap-1 rounded-md border p-2' : 'grid gap-1 rounded-md border border-amber-300 bg-amber-50 p-2'
        }
      >
        <h3 id={headingId} className="font-semibold break-all">
          {text.camera(camera)}
        </h3>
        <p className="text-muted-foreground">
          {t('dates.photos', { count: camera.photos, formatted: fmt.count(camera.photos) })}
        </p>
        <p>
          {camera.state === 'offset' && shift !== null
            ? t('dates.cameras.state.offset', { shift: text.shift(shift) })
            : t(`dates.cameras.state.${camera.state === 'offset' ? 'disagrees' : camera.state}`)}
        </p>
        {camera.events.length > 0 && (
          <ul aria-label={t('dates.cameras.events')} className="grid gap-0.5 pl-4">
            {camera.events.map((event) => (
              <li key={event.folder.id} className="break-all">
                {t('dates.cameras.event', {
                  path: event.folder.path,
                  photos: t('dates.photos', { count: event.photos, formatted: fmt.count(event.photos) }),
                  delta: text.shift(event.delta_s),
                  reference: t(`dates.cameras.reference.${event.reference ?? 'none'}`),
                })}
              </li>
            ))}
          </ul>
        )}
        {camera.state === 'offset' && shift !== null && (
          <div>
            <Button size="sm" onClick={onShift}>
              {t('dates.cameras.shift', {
                shift: text.shift(shift),
                photos: t('dates.photos', { count: photos, formatted: fmt.count(photos) }),
              })}
            </Button>
          </div>
        )}
      </article>
    </li>
  )
}

// parentPath is the path of the folder that holds a path ('' for the top).
function parentPath(path: string): string {
  const slash = path.lastIndexOf('/')
  return slash < 0 ? '' : path.slice(0, slash)
}

// ShiftDialog previews a camera's suggested shift: the photos it moves,
// that camera's photos directly in the folders of its events, each with its
// date now and after. Confirming records the shift on exactly those
// targets: the events' folders and the camera.
function ShiftDialog({
  camera,
  onShifted,
  onClose,
}: {
  camera: Camera
  onShifted: (result: SetCorrectionResult) => void
  onClose: () => void
}) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const text = useDatesText()
  const queryClient = useQueryClient()
  const csrfToken = useCsrfToken()
  const shiftS = camera.suggested_shift_s ?? 0
  const folderIds = camera.events.map((event) => event.folder.id)
  const photos = eventPhotos(camera)

  const preview = useQuery({
    queryKey: [...datesQueryRoot, 'shift', camera.source_id, camera.key, ...folderIds],
    queryFn: async ({ signal }) => {
      const rows: MediaDate[] = []
      for (const event of camera.events) {
        let cursor: string | null = null
        do {
          const page = await fetchDates(
            { source: camera.source_id, flag: null, dateSource: null, camera: camera.key, within: event.folder.id },
            cursor,
            signal,
          )
          rows.push(...page.items.filter((row) => parentPath(row.entry.path) === event.folder.path))
          cursor = page.next_cursor
        } while (cursor !== null)
      }
      return rows
    },
  })

  const apply = useMutation({
    mutationFn: () =>
      setDateCorrection(
        { folder_ids: folderIds, camera_key: camera.key },
        { kind: 'shift', shift_s: shiftS },
        csrfToken,
      ),
    onSuccess: async (result) => {
      await refreshAfterCorrection(queryClient)
      onShifted(result)
    },
  })

  const title = t('dates.cameras.shiftTitle', { camera: text.camera(camera), shift: text.shift(shiftS) })
  return (
    <Dialog title={title} description={t('dates.cameras.shiftHelp', { shift: text.shift(shiftS) })} onClose={onClose} alert className="max-w-2xl">
      {preview.isPending && (
        <p role="status" className="text-sm text-muted-foreground">
          {t('app.loading')}
        </p>
      )}
      {preview.isError && <ErrorBanner error={preview.error} onRetry={() => void preview.refetch()} />}
      {preview.data !== undefined && (
        <ul aria-label={t('dates.cameras.shiftList')} className="grid max-h-[50vh] gap-1 overflow-y-auto text-sm">
          {preview.data.map((row) => (
            <li key={row.entry.id} className="rounded-md border px-2 py-1.5 break-all">
              {t('dates.cameras.shiftLine', {
                path: row.entry.path,
                from: text.date(row.date),
                to:
                  row.date.local === null || row.date.precision === null
                    ? text.date(row.date)
                    : fmt.local(
                        shiftLocal(row.date.local, row.date.precision, shiftS),
                        row.date.precision,
                        row.date.offset_min,
                      ),
              })}
            </li>
          ))}
        </ul>
      )}
      {apply.isError && <ErrorBanner error={apply.error} onDismiss={() => apply.reset()} />}
      <div className="flex justify-end gap-2">
        <Button variant="outline" onClick={onClose}>
          {t('dates.cameras.cancel')}
        </Button>
        <Button disabled={preview.data === undefined || apply.isPending} onClick={() => apply.mutate()}>
          {apply.isPending
            ? t('dates.cameras.shifting')
            : t('dates.cameras.confirm', { photos: t('dates.photos', { count: photos, formatted: fmt.count(photos) }) })}
        </Button>
      </div>
    </Dialog>
  )
}
