import { useInfiniteQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import {
  fetchFolders,
  foldersQueryKey,
  historyQueryRoot,
  planCreateFolder,
  runAction,
} from '@/api/organize'
import { useSources } from '@/api/sources'
import { ErrorBanner } from '@/app/ErrorBanner'
import { useCsrfToken } from '@/app/session'
import { Button } from '@/components/ui/button'
import { Dialog } from '@/components/ui/dialog'
import { FormControl, FormField, FormLabel } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'

// Folder is one step of the chooser's trail: a folder of the index, or a
// source's top folder named after the source.
export interface Folder {
  id: string
  name: string
}

interface FolderChooserProps {
  title: string
  // sourceIds are the sources it may browse; with only one, it opens at
  // that source's top folder.
  sourceIds: string[]
  // start is the trail it opens at, from a source's top folder down.
  start?: Folder[]
  // blocked are the entries being moved: neither they nor any folder below
  // them can be the destination.
  blocked?: string[]
  onChoose: (folder: Folder) => void
  onClose: () => void
}

// FolderChooser picks the destination of a move by browsing the folders of
// the index (R3 design D16), never by a typed path. It can make a new
// folder inside the current one.
export function FolderChooser({ title, sourceIds, start, blocked = [], onChoose, onClose }: FolderChooserProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const csrfToken = useCsrfToken()
  const sources = useSources()
  const [opened, setOpened] = useState<Folder[] | null>(start ?? null)
  const [naming, setNaming] = useState(false)
  const [name, setName] = useState('')
  const [created, setCreated] = useState<string | null>(null)

  const candidates = (sources.data?.sources ?? []).filter((s) => sourceIds.includes(s.id))
  const only = candidates.length === 1 ? candidates[0] : undefined
  const trail = opened ?? (only === undefined ? [] : [{ id: only.root_entry_id, name: only.label }])
  const current = trail.at(-1)
  const blockedHere = trail.some((folder) => blocked.includes(folder.id))

  const folders = useInfiniteQuery({
    queryKey: foldersQueryKey(current?.id ?? ''),
    queryFn: ({ pageParam, signal }) => fetchFolders(current?.id ?? '', pageParam, signal),
    initialPageParam: null as string | null,
    getNextPageParam: (page) => page.next_cursor,
    enabled: current !== undefined,
  })
  const rows = folders.data?.pages.flatMap((page) => page.items) ?? []

  // A new folder is made at once: it appears in the list when the change
  // ends, which refetches every folder listing.
  const create = useMutation({
    mutationFn: async (parent: string) => {
      const planned = await planCreateFolder(parent, name.trim(), csrfToken)
      return runAction(planned.action.id, csrfToken)
    },
    onSuccess: () => {
      setCreated(name.trim())
      setNaming(false)
      setName('')
      void queryClient.invalidateQueries({ queryKey: historyQueryRoot })
    },
  })

  const open = (next: Folder[]) => {
    setOpened(next)
    setNaming(false)
    setCreated(null)
    create.reset()
  }

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (current !== undefined) {
      create.mutate(current.id)
    }
  }

  return (
    <Dialog title={title} description={t('organize.chooser.description')} onClose={onClose}>
      <nav aria-label={t('organize.chooser.trail')}>
        <ol className="flex flex-wrap items-center gap-1 text-sm">
          {candidates.length > 1 && (
            <li>
              <Button variant="link" size="sm" className="px-1" onClick={() => open([])}>
                {t('organize.chooser.sources')}
              </Button>
            </li>
          )}
          {trail.map((folder, index) => (
            <li key={folder.id} className="flex items-center gap-1">
              {(index > 0 || candidates.length > 1) && <span aria-hidden="true">/</span>}
              <Button
                variant="link"
                size="sm"
                className="px-1"
                aria-current={index === trail.length - 1 ? 'location' : undefined}
                onClick={() => open(trail.slice(0, index + 1))}
              >
                {folder.name}
              </Button>
            </li>
          ))}
        </ol>
      </nav>

      {sources.data !== undefined && candidates.length === 0 && (
        <p className="text-sm text-muted-foreground">{t('organize.chooser.noSources')}</p>
      )}

      {current === undefined && candidates.length > 1 && (
        <ul aria-label={t('organize.chooser.sources')} className="grid gap-1 rounded-md border p-1">
          {candidates.map((source) => (
            <li key={source.id}>
              <button
                type="button"
                className="w-full rounded-sm px-2 py-1.5 text-left text-sm font-medium break-all hover:bg-accent"
                onClick={() => open([{ id: source.root_entry_id, name: source.label }])}
              >
                {source.label}
              </button>
            </li>
          ))}
        </ul>
      )}

      {current !== undefined && (
        <>
          {folders.isPending && (
            <p role="status" className="text-sm text-muted-foreground">
              {t('app.loading')}
            </p>
          )}
          {folders.isError && <ErrorBanner error={folders.error} onRetry={() => void folders.refetch()} />}
          {folders.data !== undefined &&
            (rows.length === 0 ? (
              <p className="text-sm text-muted-foreground">{t('organize.chooser.noFolders')}</p>
            ) : (
              <ul
                aria-label={t('organize.chooser.folders')}
                className="grid max-h-72 gap-1 overflow-y-auto rounded-md border p-1"
              >
                {rows.map((row) => (
                  <li key={row.id}>
                    <button
                      type="button"
                      className="w-full rounded-sm px-2 py-1.5 text-left text-sm font-medium break-all hover:bg-accent"
                      onClick={() => open([...trail, { id: row.id, name: row.name }])}
                    >
                      {row.name}
                    </button>
                  </li>
                ))}
              </ul>
            ))}
          {folders.hasNextPage && (
            <div className="flex justify-center">
              <Button
                variant="outline"
                size="sm"
                disabled={folders.isFetchingNextPage}
                onClick={() => void folders.fetchNextPage()}
              >
                {folders.isFetchingNextPage ? t('map.loadingMore') : t('map.loadMore')}
              </Button>
            </div>
          )}

          {created !== null && (
            <p role="status" className="text-sm">
              {t('organize.chooser.creating', { name: created })}
            </p>
          )}
          {naming && (
            <form className="grid gap-2" onSubmit={submit}>
              <FormField>
                <FormLabel>{t('organize.newFolderLabel')}</FormLabel>
                <FormControl>
                  <Input name="name" value={name} onChange={(event) => setName(event.target.value)} />
                </FormControl>
              </FormField>
              {create.isError && (
                <ErrorBanner error={create.error} overrides={{ invalid_request: t('organize.invalidName') }} />
              )}
              <div className="flex gap-2">
                <Button type="submit" size="sm" disabled={name.trim() === '' || create.isPending}>
                  {create.isPending ? t('organize.working') : t('organize.create')}
                </Button>
                <Button type="button" size="sm" variant="outline" onClick={() => setNaming(false)}>
                  {t('organize.cancel')}
                </Button>
              </div>
            </form>
          )}

          <div className="grid gap-2 border-t pt-4">
            <p className="text-sm break-all">
              {t('organize.chooser.current', { path: trail.map((folder) => folder.name).join(' / ') })}
            </p>
            {blockedHere && <p className="text-sm text-muted-foreground">{t('organize.insideItself')}</p>}
          </div>
        </>
      )}

      <div className="flex flex-wrap justify-end gap-2">
        <Button type="button" variant="outline" onClick={onClose}>
          {t('organize.cancel')}
        </Button>
        {current !== undefined && !naming && (
          <Button type="button" variant="outline" onClick={() => setNaming(true)}>
            {t('organize.chooser.newFolder')}
          </Button>
        )}
        <Button
          type="button"
          disabled={current === undefined || blockedHere}
          onClick={() => current !== undefined && onChoose(current)}
        >
          {t('organize.chooser.here')}
        </Button>
      </div>
    </Dialog>
  )
}
