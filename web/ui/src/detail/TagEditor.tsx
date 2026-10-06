import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useId, useState, type FormEvent } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { Link } from 'react-router'

import type { EffectiveTag } from '@/api/entries'
import { createTag, refreshAfterTagChange, setTags, useTags } from '@/api/tags'
import { ErrorBanner } from '@/app/ErrorBanner'
import { useCsrfToken } from '@/app/session'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useEntryLink } from '@/detail/useEntryLink'

interface TagEditorProps {
  entryId: string
  tags: EffectiveTag[]
  rootLabel: string
}

// TagEditor lists an entry's own tags, which it can remove, and its
// inherited tags with the folder each comes from, and adds own tags: an
// existing one, or a new one created on the spot.
export function TagEditor({ entryId, tags, rootLabel }: TagEditorProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const csrfToken = useCsrfToken()
  const entryLink = useEntryLink()
  const allTags = useTags()
  const chooseId = useId()
  const newId = useId()
  const [chosen, setChosen] = useState('')
  const [newName, setNewName] = useState('')

  const own = tags.filter((tag) => tag.own)
  const inherited = tags.filter((tag) => !tag.own)
  const choices = (allTags.data?.tags ?? []).filter((tag) => !own.some((o) => o.id === tag.id))

  const change = useMutation({
    mutationFn: (edit: { add?: number[]; remove?: number[] }) =>
      setTags({ entry_ids: [entryId] }, edit, csrfToken),
    onSuccess: async () => {
      setChosen('')
      await refreshAfterTagChange(queryClient)
    },
  })

  const create = useMutation({
    mutationFn: async (name: string) => {
      const { tag } = await createTag(name, csrfToken)
      await setTags({ entry_ids: [entryId] }, { add: [tag.id] }, csrfToken)
    },
    onSuccess: async () => {
      setNewName('')
      await refreshAfterTagChange(queryClient)
    },
    // The tag may exist even when adding it failed.
    onError: () => refreshAfterTagChange(queryClient),
  })

  const busy = change.isPending || create.isPending

  const add = (event: FormEvent) => {
    event.preventDefault()
    if (chosen !== '') {
      change.mutate({ add: [Number(chosen)] })
    }
  }

  const addNew = (event: FormEvent) => {
    event.preventDefault()
    const name = newName.trim()
    if (name !== '') {
      create.mutate(name)
    }
  }

  return (
    <div className="grid gap-3 text-sm">
      {tags.length === 0 && <p className="text-muted-foreground">{t('detail.noTags')}</p>}
      {own.length > 0 && (
        <ul aria-label={t('detail.ownTags')} className="flex flex-wrap gap-2">
          {own.map((tag) => (
            <li key={tag.id} className="flex items-center gap-1 rounded-full bg-secondary py-0.5 pr-1 pl-3">
              <span>{tag.name}</span>
              <Button
                variant="ghost"
                size="sm"
                className="h-6 px-2"
                aria-label={t('detail.removeTag', { name: tag.name })}
                disabled={busy}
                onClick={() => change.mutate({ remove: [tag.id] })}
              >
                ×
              </Button>
            </li>
          ))}
        </ul>
      )}
      {inherited.length > 0 && (
        <div className="grid gap-1">
          <ul aria-label={t('detail.inheritedTags')} className="grid gap-1">
            {inherited.map((tag) => (
              <li key={tag.id}>
                <span className="rounded-full border px-3 py-0.5">{tag.name}</span>{' '}
                {tag.from !== null && (
                  <span className="text-muted-foreground">
                    <Trans
                      i18nKey="detail.tagFrom"
                      values={{ path: tag.from.path === '' ? rootLabel : tag.from.path }}
                      components={{
                        folderLink: <Link to={{ search: entryLink(tag.from.id) }} className="text-primary underline" />,
                      }}
                    />
                  </span>
                )}
              </li>
            ))}
          </ul>
          <p className="text-xs text-muted-foreground">{t('detail.inheritedTagHelp')}</p>
        </div>
      )}

      <form onSubmit={add} className="flex flex-wrap items-end gap-2">
        <div className="grid gap-1">
          <Label htmlFor={chooseId}>{t('detail.addTag')}</Label>
          <select
            id={chooseId}
            value={chosen}
            onChange={(event) => setChosen(event.target.value)}
            className="h-8 rounded-md border border-input bg-card px-2"
          >
            <option value="">{t('detail.noTagChoice')}</option>
            {choices.map((tag) => (
              <option key={tag.id} value={tag.id}>
                {tag.name}
              </option>
            ))}
          </select>
        </div>
        <Button type="submit" size="sm" variant="outline" disabled={busy || chosen === ''}>
          {t('detail.add')}
        </Button>
      </form>

      <form onSubmit={addNew} className="flex flex-wrap items-end gap-2">
        <div className="grid gap-1">
          <Label htmlFor={newId}>{t('detail.newTag')}</Label>
          <Input
            id={newId}
            value={newName}
            maxLength={64}
            onChange={(event) => setNewName(event.target.value)}
            className="h-8 w-48"
          />
        </div>
        <Button type="submit" size="sm" variant="outline" disabled={busy || newName.trim() === ''}>
          {t('detail.createTag')}
        </Button>
      </form>

      {change.isError && <ErrorBanner error={change.error} onDismiss={() => change.reset()} />}
      {create.isError && (
        <ErrorBanner
          error={create.error}
          overrides={{ tag_exists: t('detail.tagExists') }}
          onDismiss={() => create.reset()}
        />
      )}
    </div>
  )
}
