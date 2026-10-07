import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { setSourceSchedule, updateSource, type Schedule, type ScheduleSkip, type Source } from '@/api/sources'
import { ErrorBanner } from '@/app/ErrorBanner'
import { useCsrfToken } from '@/app/session'
import { Button } from '@/components/ui/button'
import { FormControl, FormField, FormLabel } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import { useFormat } from '@/lib/format'

// Scheduled rescans (r2b design D6): the card's schedule lines and the form
// that changes the schedule.

const weekdays = [0, 1, 2, 3, 4, 5, 6] as const

// weekdayName names a weekday, 0 being Sunday, in the locale.
function weekdayName(day: number, locale: string): string {
  // 2023-01-01 was a Sunday.
  return new Intl.DateTimeFormat(locale, { weekday: 'long', timeZone: 'UTC' }).format(Date.UTC(2023, 0, 1 + day))
}

// useScheduleText describes a schedule: "Off", "Daily at 03:00", or "Weekly
// on Sunday at 03:00", with its zone when it is not the browser's.
function useScheduleText(): (schedule: Schedule | null) => string {
  const { t, i18n } = useTranslation()
  const locale = i18n.resolvedLanguage ?? i18n.language
  return (schedule) => {
    if (schedule === null) {
      return t('sources.schedule.off')
    }
    const text =
      schedule.every === 'day'
        ? t('sources.schedule.daily', { time: schedule.at })
        : t('sources.schedule.weekly', { weekday: weekdayName(schedule.weekday ?? 0, locale), time: schedule.at })
    return schedule.zone === Intl.DateTimeFormat().resolvedOptions().timeZone
      ? text
      : t('sources.schedule.inZone', { schedule: text, zone: schedule.zone })
  }
}

const skipReasons = ['offline', 'unavailable', 'invalid_schedule'] as const
type SkipReason = (typeof skipReasons)[number]

function isSkipReason(reason: string): reason is SkipReason {
  return (skipReasons as readonly string[]).includes(reason)
}

// ScheduleLines are the card's rows for the schedule, its next scan, and
// the last skipped scan, inside the card's description list.
export function ScheduleLines({ source }: { source: Source }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const scheduleText = useScheduleText()
  return (
    <>
      <dt className="text-muted-foreground">{t('sources.schedule.label')}</dt>
      <dd>{scheduleText(source.schedule)}</dd>
      {source.next_scan_at !== null && (
        <>
          <dt className="text-muted-foreground">{t('sources.schedule.next')}</dt>
          <dd>{fmt.dateTime(source.next_scan_at)}</dd>
        </>
      )}
      {source.schedule_skipped !== null && (
        <>
          <dt className="text-muted-foreground">{t('sources.schedule.lastSkipped')}</dt>
          <dd>
            <SkipText skip={source.schedule_skipped} />
          </dd>
        </>
      )}
    </>
  )
}

function SkipText({ skip }: { skip: ScheduleSkip }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const when = fmt.dateTime(skip.at)
  return isSkipReason(skip.reason)
    ? t(`sources.schedule.skipped.${skip.reason}`, { when })
    : t('sources.schedule.skipped.other', { when })
}

type Every = 'off' | Schedule['every']

const selectClass = 'h-9 rounded-md border border-input bg-card px-2 text-sm'

// ScheduleForm changes the schedule: off, daily, or weekly on a day, at a
// time in the browser's zone.
export function ScheduleForm({ source, onDone }: { source: Source; onDone: () => void }) {
  const { t, i18n } = useTranslation()
  const locale = i18n.resolvedLanguage ?? i18n.language
  const queryClient = useQueryClient()
  const csrfToken = useCsrfToken()
  // A schedule is set in the browser's time zone.
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone
  const [every, setEvery] = useState<Every>(source.schedule?.every ?? 'off')
  const [weekday, setWeekday] = useState(source.schedule?.weekday ?? 0)
  const [time, setTime] = useState(source.schedule?.at ?? '03:00')
  const validTime = /^([01]\d|2[0-3]):[0-5]\d$/u.test(time)

  const save = useMutation({
    mutationFn: () => {
      const schedule: Schedule | null =
        every === 'off'
          ? null
          : every === 'day'
            ? { every, at: time, zone }
            : { every, at: time, weekday, zone }
      return setSourceSchedule(source.id, schedule, csrfToken)
    },
    onSuccess: (result) => {
      updateSource(queryClient, source.id, () => result.source)
      onDone()
    },
  })

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    save.mutate()
  }

  return (
    <form aria-label={t('sources.schedule.formTitle')} className="grid max-w-md gap-3" onSubmit={submit}>
      <FormField>
        <FormLabel>{t('sources.schedule.every')}</FormLabel>
        <FormControl>
          <select
            name="every"
            value={every}
            onChange={(event) => setEvery(event.target.value as Every)}
            className={selectClass}
          >
            <option value="off">{t('sources.schedule.everyOff')}</option>
            <option value="day">{t('sources.schedule.everyDay')}</option>
            <option value="week">{t('sources.schedule.everyWeek')}</option>
          </select>
        </FormControl>
      </FormField>
      {every === 'week' && (
        <FormField>
          <FormLabel>{t('sources.schedule.weekday')}</FormLabel>
          <FormControl>
            <select
              name="weekday"
              value={weekday}
              onChange={(event) => setWeekday(Number(event.target.value))}
              className={selectClass}
            >
              {weekdays.map((day) => (
                <option key={day} value={day}>
                  {weekdayName(day, locale)}
                </option>
              ))}
            </select>
          </FormControl>
        </FormField>
      )}
      {every !== 'off' && (
        <>
          <FormField>
            <FormLabel>{t('sources.schedule.time')}</FormLabel>
            <FormControl>
              <Input
                type="time"
                name="time"
                required
                value={time}
                onChange={(event) => setTime(event.target.value)}
                className="w-36"
              />
            </FormControl>
          </FormField>
          <p className="text-sm text-muted-foreground">{t('sources.schedule.zoneNote', { zone })}</p>
        </>
      )}
      {save.isError && <ErrorBanner error={save.error} />}
      <div className="flex gap-2">
        <Button type="submit" disabled={(every !== 'off' && !validTime) || save.isPending}>
          {save.isPending ? t('sources.saving') : t('sources.save')}
        </Button>
        <Button type="button" variant="outline" onClick={onDone}>
          {t('sources.cancel')}
        </Button>
      </div>
    </form>
  )
}
