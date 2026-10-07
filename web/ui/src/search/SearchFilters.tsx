import { useId, useState, type FormEvent, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { categories, decisions, fileKinds, triages } from '@/api/entries'
import { dupFilters, searchListParams } from '@/api/search'
import { useTags } from '@/api/tags'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useFormat } from '@/lib/format'

// Extensions are typed as text; the other lists are chosen from options.
type ListFilter = Exclude<(typeof searchListParams)[number], 'ext'>

const units = [
  { unit: 'B', bytes: 1 },
  { unit: 'KiB', bytes: 1024 },
  { unit: 'MiB', bytes: 1024 ** 2 },
  { unit: 'GiB', bytes: 1024 ** 3 },
] as const

type Unit = (typeof units)[number]['unit']

interface SizeDraft {
  value: string
  unit: Unit
}

// sizeDraft shows a byte count in the largest unit that holds it with at
// most two decimals, as it was likely typed: 1572864 is 1.5 MiB.
function sizeDraft(bytes: string | null): SizeDraft {
  if (bytes === null) {
    return { value: '', unit: 'MiB' }
  }
  const n = Number(bytes)
  const fit =
    [...units].reverse().find((u) => n >= u.bytes && Number.isInteger((n / u.bytes) * 100)) ?? units[0]
  return { value: String(n / fit.bytes), unit: fit.unit }
}

function sizeBytes(draft: SizeDraft): string | null {
  if (draft.value.trim() === '') {
    return null
  }
  const factor = units.find((u) => u.unit === draft.unit)?.bytes ?? 1
  return String(Math.round(Number(draft.value) * factor))
}

interface SearchFiltersProps {
  params: URLSearchParams
  onSearch: (next: URLSearchParams) => void
}

// SearchFilters edits the filters of design D11, read from the address and
// applied to it on submit. The folder to search within and the order are
// kept as they are.
export function SearchFilters({ params, onSearch }: SearchFiltersProps) {
  const { t } = useTranslation()
  const tags = useTags()
  const nameId = useId()
  const extId = useId()
  const extHelpId = useId()
  const yearFromId = useId()
  const yearToId = useId()

  const [name, setName] = useState(params.get('name') ?? '')
  const [ext, setExt] = useState(params.getAll('ext').join(' '))
  const [minSize, setMinSize] = useState(() => sizeDraft(params.get('min_size')))
  const [maxSize, setMaxSize] = useState(() => sizeDraft(params.get('max_size')))
  const [yearFrom, setYearFrom] = useState(params.get('year_from') ?? '')
  const [yearTo, setYearTo] = useState(params.get('year_to') ?? '')
  const [lists, setLists] = useState<Record<ListFilter, string[]>>(() => ({
    file_kind: params.getAll('file_kind'),
    category: params.getAll('category'),
    tag: params.getAll('tag'),
    decision: params.getAll('decision'),
    triage: params.getAll('triage'),
    dup: params.getAll('dup'),
  }))

  const toggle = (key: ListFilter, value: string) =>
    setLists((current) => ({
      ...current,
      [key]: current[key].includes(value) ? current[key].filter((v) => v !== value) : [...current[key], value],
    }))

  const submit = (event: FormEvent) => {
    event.preventDefault()
    const next = new URLSearchParams()
    for (const key of ['within', 'source', 'sort', 'order']) {
      const value = params.get(key)
      if (value !== null) {
        next.set(key, value)
      }
    }
    const singles: Record<string, string | null> = {
      name: name.trim() === '' ? null : name.trim(),
      min_size: sizeBytes(minSize),
      max_size: sizeBytes(maxSize),
      year_from: yearFrom.trim() === '' ? null : yearFrom.trim(),
      year_to: yearTo.trim() === '' ? null : yearTo.trim(),
    }
    for (const [key, value] of Object.entries(singles)) {
      if (value !== null) {
        next.set(key, value)
      }
    }
    for (const e of ext.split(/[\s,]+/)) {
      const clean = e.replace(/^\.+/, '').toLowerCase()
      if (clean !== '') {
        next.append('ext', clean)
      }
    }
    for (const key of ['file_kind', 'category', 'tag', 'decision', 'triage', 'dup'] as const) {
      for (const value of lists[key]) {
        next.append(key, value)
      }
    }
    onSearch(next)
  }

  const clear = () => {
    const next = new URLSearchParams()
    const within = params.get('within')
    if (within !== null) {
      next.set('within', within)
    }
    onSearch(next)
  }

  const checkboxes = (key: ListFilter, options: Array<{ value: string; label: string }>) => (
    <div className="grid gap-1">
      {options.map((option) => (
        <label key={option.value} className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={lists[key].includes(option.value)}
            onChange={() => toggle(key, option.value)}
          />
          {option.label}
        </label>
      ))}
    </div>
  )

  return (
    <form onSubmit={submit} aria-label={t('search.filters')} className="grid gap-4 rounded-lg border bg-card p-4">
      {/* Columns never narrower than a size row (label, number, unit), so no
          control spills into the next column (design D23). */}
      <div className="grid grid-cols-[repeat(auto-fit,minmax(min(16rem,100%),1fr))] gap-3">
        <div className="grid content-start gap-1">
          <Label htmlFor={nameId}>{t('search.name')}</Label>
          <Input id={nameId} type="search" value={name} onChange={(event) => setName(event.target.value)} />
        </div>
        <div className="grid content-start gap-1">
          <Label htmlFor={extId}>{t('search.ext')}</Label>
          <Input
            id={extId}
            value={ext}
            aria-describedby={extHelpId}
            onChange={(event) => setExt(event.target.value)}
          />
          <p id={extHelpId} className="text-xs text-muted-foreground">
            {t('search.extHelp')}
          </p>
        </div>
        <fieldset className="grid min-w-0 content-start gap-1">
          <legend className="text-sm font-medium">{t('search.size')}</legend>
          <SizeInput label={t('search.minSize')} draft={minSize} onChange={setMinSize} />
          <SizeInput label={t('search.maxSize')} draft={maxSize} onChange={setMaxSize} />
        </fieldset>
        <fieldset className="grid min-w-0 content-start gap-1">
          <legend className="text-sm font-medium">{t('search.year')}</legend>
          <div className="flex items-center gap-2">
            <Label htmlFor={yearFromId} className="w-14 shrink-0 text-xs">
              {t('search.yearFrom')}
            </Label>
            <Input
              id={yearFromId}
              type="number"
              inputMode="numeric"
              value={yearFrom}
              onChange={(event) => setYearFrom(event.target.value)}
              className="min-w-0 flex-1"
            />
          </div>
          <div className="flex items-center gap-2">
            <Label htmlFor={yearToId} className="w-14 shrink-0 text-xs">
              {t('search.yearTo')}
            </Label>
            <Input
              id={yearToId}
              type="number"
              inputMode="numeric"
              value={yearTo}
              onChange={(event) => setYearTo(event.target.value)}
              className="min-w-0 flex-1"
            />
          </div>
        </fieldset>
      </div>

      <div className="flex flex-wrap gap-3">
        <Choices label={t('search.fileKind')} count={lists.file_kind.length}>
          {checkboxes(
            'file_kind',
            fileKinds.map((k) => ({ value: k, label: t(`entry.fileKind.${k}`) })),
          )}
        </Choices>
        <Choices label={t('search.category')} count={lists.category.length}>
          {checkboxes(
            'category',
            categories.map((c) => ({ value: c, label: t(`entry.category.${c}`) })),
          )}
        </Choices>
        <Choices label={t('search.decision')} count={lists.decision.length}>
          {checkboxes(
            'decision',
            decisions.map((d) => ({ value: d, label: t(`home.decision.${d}`) })),
          )}
        </Choices>
        <Choices label={t('search.triage')} count={lists.triage.length}>
          {checkboxes(
            'triage',
            triages.map((tr) => ({ value: tr, label: t(`entry.triage.${tr}`) })),
          )}
        </Choices>
        <Choices label={t('search.dup')} count={lists.dup.length}>
          {checkboxes(
            'dup',
            // A copy outside the folder needs the folder searched within.
            dupFilters
              .filter((d) => d !== 'elsewhere' || params.has('within'))
              .map((d) => ({ value: d, label: t(`search.dupChoice.${d}`) })),
          )}
        </Choices>
        <Choices label={t('search.tags')} count={lists.tag.length}>
          {tags.data?.tags.length === 0 && <p className="text-sm text-muted-foreground">{t('search.noTags')}</p>}
          {checkboxes(
            'tag',
            (tags.data?.tags ?? []).map((tag) => ({ value: String(tag.id), label: tag.name })),
          )}
        </Choices>
      </div>

      <div className="flex gap-2">
        <Button type="submit">{t('search.submit')}</Button>
        <Button type="button" variant="outline" onClick={clear}>
          {t('search.reset')}
        </Button>
      </div>
    </form>
  )
}

function Choices({ label, count, children }: { label: string; count: number; children: ReactNode }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  return (
    <details className="rounded-md border px-3 py-1.5">
      <summary className="cursor-pointer text-sm font-medium">
        {count === 0 ? label : t('search.chosen', { label, formatted: fmt.count(count) })}
      </summary>
      <fieldset className="mt-2 grid gap-2">
        <legend className="sr-only">{label}</legend>
        {children}
      </fieldset>
    </details>
  )
}

function SizeInput({
  label,
  draft,
  onChange,
}: {
  label: string
  draft: SizeDraft
  onChange: (draft: SizeDraft) => void
}) {
  const { t } = useTranslation()
  const valueId = useId()
  return (
    <div className="flex items-center gap-2">
      <Label htmlFor={valueId} className="w-14 shrink-0 text-xs">
        {label}
      </Label>
      <Input
        id={valueId}
        type="number"
        min={0}
        step="any"
        value={draft.value}
        onChange={(event) => onChange({ ...draft, value: event.target.value })}
        className="min-w-0 flex-1"
      />
      <select
        aria-label={`${label}: ${t('search.unit')}`}
        value={draft.unit}
        onChange={(event) => onChange({ ...draft, unit: event.target.value as Unit })}
        className="h-9 shrink-0 rounded-md border border-input bg-card px-1 text-sm"
      >
        {units.map((u) => (
          <option key={u.unit} value={u.unit}>
            {u.unit}
          </option>
        ))}
      </select>
    </div>
  )
}
