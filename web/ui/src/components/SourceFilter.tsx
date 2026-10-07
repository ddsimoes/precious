import { useId } from 'react'
import { useTranslation } from 'react-i18next'
import { useSearchParams } from 'react-router'

import { useSources } from '@/api/sources'
import { rememberSource } from '@/app/sourceChoice'
import { Label } from '@/components/ui/label'
import { useSourceParam } from '@/lib/sourceParams'

// SourceFilter chooses one source or all of them, kept in the address as
// ?source=, with the screen's other parameters kept, and remembered for the
// screens opened without one (r2b design D9). It shows the source the
// screen uses, from the address or remembered.
export function SourceFilter() {
  const { t } = useTranslation()
  const [params, setParams] = useSearchParams()
  const source = useSourceParam()
  const sources = useSources()
  const id = useId()

  const choose = (value: string) => {
    rememberSource(value === '' ? null : value)
    const next = new URLSearchParams(params)
    if (value === '') {
      next.delete('source')
    } else {
      next.set('source', value)
    }
    setParams(next)
  }

  return (
    <div className="flex items-center gap-2">
      <Label htmlFor={id}>{t('home.filter')}</Label>
      <select
        id={id}
        value={source ?? ''}
        onChange={(event) => choose(event.target.value)}
        className="h-9 rounded-md border border-input bg-card px-2 text-sm"
      >
        <option value="">{t('home.allSources')}</option>
        {sources.data?.sources.map((s) => (
          <option key={s.id} value={s.id}>
            {s.label}
          </option>
        ))}
      </select>
    </div>
  )
}
