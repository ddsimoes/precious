import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useId, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import {
  clearDateCorrection,
  maxShiftS,
  refreshAfterCorrection,
  setDateCorrection,
  shiftSeconds,
  shiftUnits,
  type Correction,
  type DateTargets,
  type Precision,
  type SetCorrectionResult,
  type ShiftUnit,
} from '@/api/dates'
import { maxBulkIds } from '@/api/decisions'
import { ErrorBanner } from '@/app/ErrorBanner'
import { useCsrfToken } from '@/app/session'
import { Button } from '@/components/ui/button'
import { Dialog } from '@/components/ui/dialog'
import { FormControl, FormField, FormLabel, FormMessage } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import { choiceTargets, initialChoice, type ChosenFolder } from '@/dates/targets'
import { TargetsField } from '@/dates/TargetsField'
import { useFormat } from '@/lib/format'

type Kind = Correction['kind'] | 'clear'

const precisions: Precision[] = ['year', 'month', 'day', 'second']

// localPattern is the form of a date set to each precision.
const localPattern: Record<Precision, RegExp> = {
  year: /^\d{4}$/,
  month: /^\d{4}-\d{2}$/,
  day: /^\d{4}-\d{2}-\d{2}$/,
  second: /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}$/,
}

// The control a date of each precision is entered in, and its label.
const valueControl = {
  year: { type: 'number', label: 'dates.correct.year' },
  month: { type: 'month', label: 'dates.correct.month' },
  day: { type: 'date', label: 'dates.correct.day' },
  second: { type: 'datetime-local', label: 'dates.correct.time' },
} as const

// Scope is what a correction applies to: one entry, from the detail panel,
// or the selection or folders of the Dates screen.
export type Scope =
  | { kind: 'single'; entryId: string; corrected: boolean }
  | { kind: 'bulk'; selected: string[]; within: ChosenFolder | null; sourceId: string }

// CorrectionDialog records the owner's correction of dates (R5 design D11):
// set to a year, month, day, or time; shift in years, days, hours, and
// minutes; take the name's or the folder's date; or remove the correction.
// A bulk correction reports what it skipped; a single one fails instead.
export function CorrectionDialog({ title, scope, onClose }: { title: string; scope: Scope; onClose: () => void }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const queryClient = useQueryClient()
  const csrfToken = useCsrfToken()
  const kindName = useId()
  const [kind, setKind] = useState<Kind>('set')
  const [precision, setPrecision] = useState<Precision>('day')
  const [value, setValue] = useState('')
  const [offset, setOffset] = useState('')
  const [later, setLater] = useState(true)
  const [shift, setShift] = useState<Record<ShiftUnit, string>>({ years: '', days: '', hours: '', minutes: '' })
  const [choice, setChoice] = useState(() =>
    scope.kind === 'bulk' ? initialChoice(scope.selected.length, scope.within) : null,
  )
  const [touched, setTouched] = useState(false)

  const targets: DateTargets | null =
    scope.kind === 'single'
      ? { entry_id: scope.entryId }
      : choice === null
        ? null
        : choiceTargets(choice, scope.selected)

  // The value of each control in the form the server takes: a time from a
  // datetime-local control may come without its seconds.
  const local = precision === 'second' && /T\d{2}:\d{2}$/.test(value) ? `${value}:00` : value
  const dateValid = localPattern[precision].test(local) && Number(local.slice(0, 4)) >= 1700
  const offsetMatch = /^([+-])(\d{2}):(\d{2})$/.exec(offset.trim())
  const offsetMin =
    offsetMatch === null
      ? undefined
      : (offsetMatch[1] === '-' ? -1 : 1) * (Number(offsetMatch[2]) * 60 + Number(offsetMatch[3]))
  const offsetValid =
    offset.trim() === '' || (precision === 'second' && offsetMin !== undefined && Math.abs(offsetMin) <= 840)
  const shiftParts = Object.fromEntries(
    (Object.keys(shiftUnits) as ShiftUnit[]).map((unit) => [unit, shift[unit] === '' ? 0 : Number(shift[unit])]),
  ) as Record<ShiftUnit, number>
  const shiftS = shiftSeconds(shiftParts, later)
  const shiftValid =
    Object.values(shiftParts).every((n) => Number.isInteger(n) && n >= 0) &&
    Math.abs(shiftS) >= 60 &&
    Math.abs(shiftS) <= maxShiftS

  let correction: Correction | null = null
  if (kind === 'set' && dateValid && offsetValid) {
    correction = offsetMin === undefined || precision !== 'second' ? { kind, local } : { kind, local, offset_min: offsetMin }
  } else if (kind === 'shift' && shiftValid) {
    correction = { kind, shift_s: shiftS }
  } else if (kind === 'use_name' || kind === 'use_folder') {
    correction = { kind }
  }

  const apply = useMutation({
    mutationFn: async (): Promise<SetCorrectionResult | { cleared: number }> => {
      if (targets === null) {
        throw new Error('no targets')
      }
      if (kind === 'clear') {
        return clearDateCorrection(targets, csrfToken)
      }
      if (correction === null) {
        throw new Error('no correction')
      }
      return setDateCorrection(targets, correction, csrfToken)
    },
    onSuccess: async () => {
      await refreshAfterCorrection(queryClient)
      if (scope.kind === 'single') {
        onClose()
      }
    },
  })

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    setTouched(true)
    if (targets !== null && (kind === 'clear' || correction !== null)) {
      apply.mutate()
    }
  }

  const kinds: Kind[] = ['set', 'shift', 'use_name', 'use_folder']
  if (scope.kind === 'bulk' || scope.corrected) {
    kinds.push('clear')
  }
  const tooMany = scope.kind === 'bulk' && choice?.mode === 'selected' && scope.selected.length > maxBulkIds
  const refused = kind === 'clear' ? undefined : t(`dates.correct.refused.${kind}`)
  const result = apply.data

  return (
    <Dialog title={title} description={t('dates.correct.help')} onClose={onClose}>
      {result !== undefined && scope.kind === 'bulk' ? (
        <div role="status" className="grid gap-2 text-sm">
          <CorrectionReport result={result} />
          <div className="flex justify-end">
            <Button onClick={onClose}>{t('dates.correct.close')}</Button>
          </div>
        </div>
      ) : (
        <form className="grid gap-4" onSubmit={submit} noValidate>
          {scope.kind === 'bulk' && choice !== null && (
            <TargetsField
              choice={choice}
              onChange={setChoice}
              selected={scope.selected.length}
              sourceId={scope.sourceId}
            />
          )}
          {tooMany && (
            <p className="text-sm text-muted-foreground">
              {t('dates.list.tooMany', { formatted: fmt.count(maxBulkIds) })}
            </p>
          )}
          <fieldset className="grid gap-2 text-sm">
            <legend className="mb-1 font-medium">{t('dates.correct.kind')}</legend>
            {kinds.map((k) => (
              <label key={k} className="flex items-center gap-2">
                <input type="radio" name={kindName} checked={kind === k} onChange={() => setKind(k)} />
                {t(`dates.correct.kinds.${k}`)}
              </label>
            ))}
          </fieldset>

          {kind === 'set' && (
            <div className="grid gap-3">
              <FormField>
                <FormLabel>{t('dates.correct.knownTo')}</FormLabel>
                <FormControl>
                  <select
                    value={precision}
                    onChange={(event) => {
                      setPrecision(event.target.value as Precision)
                      setValue('')
                    }}
                    className="h-9 rounded-md border border-input bg-card px-2 text-sm"
                  >
                    {precisions.map((p) => (
                      <option key={p} value={p}>
                        {t(`dates.correct.known.${p}`)}
                      </option>
                    ))}
                  </select>
                </FormControl>
              </FormField>
              <FormField error={touched && !dateValid ? t('dates.correct.invalidDate') : undefined}>
                <FormLabel>{t(valueControl[precision].label)}</FormLabel>
                <FormControl>
                  <Input
                    type={valueControl[precision].type}
                    step={precision === 'second' ? 1 : undefined}
                    min={precision === 'year' ? 1700 : undefined}
                    value={value}
                    onChange={(event) => setValue(event.target.value)}
                  />
                </FormControl>
                <FormMessage />
              </FormField>
              {precision === 'second' && (
                <FormField error={touched && !offsetValid ? t('dates.correct.invalidOffset') : undefined}>
                  <FormLabel>{t('dates.correct.offset')}</FormLabel>
                  <FormControl>
                    <Input value={offset} onChange={(event) => setOffset(event.target.value)} />
                  </FormControl>
                  <FormMessage />
                </FormField>
              )}
            </div>
          )}

          {kind === 'shift' && (
            <div className="grid gap-3">
              <FormField>
                <FormLabel>{t('dates.correct.direction')}</FormLabel>
                <FormControl>
                  <select
                    value={later ? 'later' : 'earlier'}
                    onChange={(event) => setLater(event.target.value === 'later')}
                    className="h-9 rounded-md border border-input bg-card px-2 text-sm"
                  >
                    <option value="later">{t('dates.correct.later')}</option>
                    <option value="earlier">{t('dates.correct.earlier')}</option>
                  </select>
                </FormControl>
              </FormField>
              <div className="grid grid-cols-4 gap-2">
                {(Object.keys(shiftUnits) as ShiftUnit[]).map((unit) => (
                  <FormField key={unit}>
                    <FormLabel>{t(`dates.correct.units.${unit}`)}</FormLabel>
                    <FormControl>
                      <Input
                        type="number"
                        min={0}
                        value={shift[unit]}
                        onChange={(event) => setShift({ ...shift, [unit]: event.target.value })}
                      />
                    </FormControl>
                  </FormField>
                ))}
              </div>
              <p className="text-sm text-muted-foreground">{t('dates.correct.shiftNote')}</p>
              {touched && !shiftValid && (
                <p role="alert" className="text-sm font-medium text-destructive">
                  {t('dates.correct.invalidShift')}
                </p>
              )}
            </div>
          )}

          {apply.isError && (
            <ErrorBanner
              error={apply.error}
              overrides={{
                invalid_request: t('dates.correct.invalidRequest'),
                ...(refused === undefined ? {} : { invalid_entry_state: refused }),
              }}
              onDismiss={() => apply.reset()}
            />
          )}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="outline" onClick={onClose}>
              {t('dates.correct.cancel')}
            </Button>
            <Button type="submit" disabled={targets === null || tooMany || apply.isPending}>
              {apply.isPending ? t('dates.correct.applying') : t('dates.correct.apply')}
            </Button>
          </div>
        </form>
      )}
    </Dialog>
  )
}

// CorrectionReport tells what a bulk correction did: how many dates it
// corrected or cleared, and the files it skipped with their reasons.
function CorrectionReport({ result }: { result: SetCorrectionResult | { cleared: number } }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  if ('cleared' in result) {
    return <p className="font-medium">{t('dates.correct.cleared', { count: result.cleared, formatted: fmt.count(result.cleared) })}</p>
  }
  const more = result.skipped_count - result.skipped.length
  return (
    <>
      <p className="font-medium">
        {t('dates.correct.applied', { count: result.applied, formatted: fmt.count(result.applied) })}
      </p>
      {result.skipped_count > 0 && (
        <>
          <p>{t('dates.correct.skipped', { count: result.skipped_count, formatted: fmt.count(result.skipped_count) })}</p>
          <ul className="grid gap-1 pl-4">
            {result.skipped.map((skip) => (
              <li key={skip.entry_id} className="break-all">
                {t('dates.correct.skipLine', { path: skip.path, reason: t(`dates.correct.skip.${skip.reason}`) })}
              </li>
            ))}
          </ul>
          {more > 0 && <p>{t('dates.correct.more', { count: more, formatted: fmt.count(more) })}</p>}
        </>
      )}
    </>
  )
}
