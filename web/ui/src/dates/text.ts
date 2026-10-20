import { useTranslation } from 'react-i18next'

import { shiftParts, shiftUnits, type CameraRef, type CorrectionJSON, type DateJSON, type ShiftUnit } from '@/api/dates'
import { useFormat } from '@/lib/format'

export interface DatesText {
  // shift names a shift in whole units with its direction: "+1 year 3 hours".
  shift: (shiftS: number) => string
  // date is an effective date to its precision, or "No date".
  date: (date: DateJSON) => string
  // camera names a camera by its model, with its maker unless the model
  // already says it, and its serial when known.
  camera: (camera: CameraRef) => string
  // correction describes the owner's correction of a file.
  correction: (correction: CorrectionJSON) => string
}

// useDatesText returns the wording of media dates, shared by the Dates
// screen and the detail panel.
export function useDatesText(): DatesText {
  const { t } = useTranslation()
  const fmt = useFormat()
  const shift = (shiftS: number) => {
    const parts = shiftParts(shiftS)
    const words = (Object.keys(shiftUnits) as ShiftUnit[])
      .filter((unit) => parts[unit] > 0)
      .map((unit) => t(`dates.shiftUnit.${unit}`, { count: parts[unit] }))
    if (words.length === 0) {
      return t('dates.shiftText.none')
    }
    return t(shiftS > 0 ? 'dates.shiftText.later' : 'dates.shiftText.earlier', { parts: words.join(' ') })
  }
  return {
    shift,
    date: (date) =>
      date.local === null || date.precision === null
        ? t('dates.noDate')
        : fmt.local(date.local, date.precision, date.offset_min),
    camera: (camera) => {
      const make = camera.make?.trim() ?? ''
      const model = camera.model?.trim() ?? ''
      const name = model.toLowerCase().startsWith(make.toLowerCase()) ? model : `${make} ${model}`.trim()
      const serial = camera.serial?.trim() ?? ''
      const named = name === '' ? t('dates.cameras.unnamed') : name
      return serial === '' ? named : `${named} (${t('dates.cameras.serial', { serial })})`
    },
    correction: (correction) => {
      switch (correction.kind) {
        case 'set':
          return t('dates.correction.set', {
            date: fmt.local(
              correction.local ?? '',
              localPrecision(correction.local ?? ''),
              correction.offset_min ?? null,
            ),
          })
        case 'shift':
          return t('dates.correction.shift', { shift: shift(correction.shift_s ?? 0) })
        default:
          return t(`dates.correction.${correction.kind}`)
      }
    },
  }
}

// localPrecision is the precision a local date is written to.
function localPrecision(local: string): 'year' | 'month' | 'day' | 'second' {
  if (local.length <= 4) {
    return 'year'
  }
  if (local.length <= 7) {
    return 'month'
  }
  return local.length <= 10 ? 'day' : 'second'
}
