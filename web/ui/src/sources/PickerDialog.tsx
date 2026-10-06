import { skipToken, useMutation, useQuery } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import {
  addSource,
  fetchPickerListing,
  fetchPickerRoots,
  pickerQueryKey,
  type PickerItem,
  type Source,
} from '@/api/sources'
import { ErrorBanner } from '@/app/ErrorBanner'
import { useCsrfToken } from '@/app/session'
import { Button } from '@/components/ui/button'
import { Dialog } from '@/components/ui/dialog'
import { FormControl, FormField, FormLabel } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import { useFormat } from '@/lib/format'

interface PickerDialogProps {
  onAdded: (source: Source) => void
  onClose: () => void
}

// PickerDialog adds a source by browsing folders, from the allowed locations
// down, through the server's opaque handles. No path is ever typed.
export function PickerDialog({ onAdded, onClose }: PickerDialogProps) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const csrfToken = useCsrfToken()
  // trail is the folders opened from a location down to the current one.
  const [trail, setTrail] = useState<PickerItem[]>([])
  const [label, setLabel] = useState('')
  const current = trail.at(-1)
  // A handle no longer valid (the server restarted) is a 400 here.
  const expired = { invalid_request: t('sources.picker.expired') }

  const roots = useQuery({
    queryKey: pickerQueryKey(null),
    queryFn: ({ signal }) => fetchPickerRoots(signal),
    enabled: current === undefined,
  })
  const listing = useQuery({
    queryKey: pickerQueryKey(current?.handle ?? null),
    queryFn:
      current === undefined ? skipToken : ({ signal }) => fetchPickerListing(current.handle, signal),
  })
  const add = useMutation({
    mutationFn: (handle: string) => addSource(handle, label.trim(), csrfToken),
    onSuccess: (result) => onAdded(result.source),
  })

  const open = (index: number, item?: PickerItem) => {
    const next = trail.slice(0, index)
    setTrail(item === undefined ? next : [...next, item])
    setLabel('')
    add.reset()
  }

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (listing.data !== undefined) {
      add.mutate(listing.data.entry.handle)
    }
  }

  const query = current === undefined ? roots : listing
  const items = current === undefined ? roots.data?.roots : listing.data?.children
  const entry = listing.data?.entry

  return (
    <Dialog
      title={t('sources.picker.title')}
      description={t('sources.picker.description')}
      onClose={onClose}
    >
      <nav aria-label={t('sources.picker.trail')}>
        <ol className="flex flex-wrap items-center gap-1 text-sm">
          <li>
            <Button variant="link" size="sm" className="px-1" onClick={() => open(0)}>
              {t('sources.picker.locations')}
            </Button>
          </li>
          {trail.map((item, index) => (
            <li key={item.handle} className="flex items-center gap-1">
              <span aria-hidden="true">/</span>
              <Button
                variant="link"
                size="sm"
                className="px-1"
                aria-current={index === trail.length - 1 ? 'location' : undefined}
                onClick={() => open(index + 1)}
              >
                {item.name}
              </Button>
            </li>
          ))}
        </ol>
      </nav>

      {query.isPending && (
        <p role="status" className="text-sm text-muted-foreground">
          {t('app.loading')}
        </p>
      )}
      {query.isError && (
        <ErrorBanner
          error={query.error}
          overrides={expired}
          onRetry={current === undefined ? () => void roots.refetch() : () => open(0)}
          retryLabel={current === undefined ? undefined : t('sources.picker.startOver')}
        />
      )}

      {items !== undefined && (
        <>
          {items.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t('sources.picker.noFolders')}</p>
          ) : (
            <ul
              aria-label={t('sources.picker.folders')}
              className="grid max-h-72 gap-1 overflow-y-auto rounded-md border p-1"
            >
              {items.map((item) => (
                <li key={item.handle}>
                  <button
                    type="button"
                    className="flex w-full flex-wrap items-baseline gap-x-2 rounded-sm px-2 py-1.5 text-left text-sm hover:bg-accent"
                    onClick={() => open(trail.length, item)}
                  >
                    {/* The spaces keep the parts apart in the button's accessible name. */}
                    <span className="font-medium break-all">{item.name}</span>{' '}
                    {current === undefined && (
                      <span className="text-muted-foreground break-all">{item.path}</span>
                    )}{' '}
                    {item.is_source && (
                      <span className="text-xs text-muted-foreground">{t('sources.picker.isSource')}</span>
                    )}
                  </button>
                </li>
              ))}
            </ul>
          )}
          {listing.data?.truncated === true && current !== undefined && (
            <p className="text-sm text-muted-foreground">
              {t('sources.picker.truncated', { formatted: fmt.count(listing.data.children.length) })}
            </p>
          )}
        </>
      )}

      {entry !== undefined && (
        <form className="grid gap-3 border-t pt-4" onSubmit={submit}>
          <p className="text-sm break-all">{t('sources.picker.current', { path: entry.path })}</p>
          {entry.is_source ? (
            <p className="text-sm text-muted-foreground">{t('sources.picker.currentIsSource')}</p>
          ) : (
            <FormField>
              <FormLabel>{t('sources.picker.name')}</FormLabel>
              <FormControl>
                <Input
                  name="label"
                  value={label}
                  placeholder={t('sources.picker.namePlaceholder', { name: entry.name })}
                  onChange={(event) => setLabel(event.target.value)}
                />
              </FormControl>
            </FormField>
          )}
          {add.isError && <ErrorBanner error={add.error} overrides={expired} />}
          <div className="flex justify-end gap-2">
            <Button type="button" variant="outline" onClick={onClose}>
              {t('sources.picker.close')}
            </Button>
            <Button type="submit" disabled={entry.is_source || add.isPending}>
              {add.isPending ? t('sources.picker.adding') : t('sources.picker.addFolder')}
            </Button>
          </div>
        </form>
      )}
      {entry === undefined && (
        <div className="flex justify-end">
          <Button type="button" variant="outline" onClick={onClose}>
            {t('sources.picker.close')}
          </Button>
        </div>
      )}
    </Dialog>
  )
}
