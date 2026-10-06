import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useId, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { deleteTag, refreshAfterTagChange, renameTag, type Tag } from '@/api/tags'
import { ErrorBanner } from '@/app/ErrorBanner'
import { useCsrfToken } from '@/app/session'
import { Button } from '@/components/ui/button'
import { Dialog } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useFormat } from '@/lib/format'

// ManageTags renames and deletes the owner's tags (rename-tag, delete-tag).
// Deleting asks first, since it removes the tag from every entry.
export function ManageTags({ tags }: { tags: Tag[] }) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  return (
    <>
      <Button type="button" variant="link" size="sm" className="justify-start px-0" onClick={() => setOpen(true)}>
        {t('search.manageTags')}
      </Button>
      {open && (
        <Dialog title={t('search.manageTags')} onClose={() => setOpen(false)}>
          <ul className="grid gap-3">
            {tags.map((tag) => (
              <TagRow key={tag.id} tag={tag} />
            ))}
          </ul>
          <div className="flex justify-end">
            <Button type="button" onClick={() => setOpen(false)}>
              {t('search.close')}
            </Button>
          </div>
        </Dialog>
      )}
    </>
  )
}

function TagRow({ tag }: { tag: Tag }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const queryClient = useQueryClient()
  const csrfToken = useCsrfToken()
  const nameId = useId()
  const [renaming, setRenaming] = useState(false)
  const [confirming, setConfirming] = useState(false)
  const [name, setName] = useState(tag.name)

  const rename = useMutation({
    mutationFn: () => renameTag(tag.id, name.trim(), csrfToken),
    onSuccess: async () => {
      setRenaming(false)
      await refreshAfterTagChange(queryClient)
    },
  })
  const remove = useMutation({
    mutationFn: () => deleteTag(tag.id, csrfToken),
    onSuccess: () => refreshAfterTagChange(queryClient),
  })

  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (name.trim() !== '' && name.trim() !== tag.name) {
      rename.mutate()
    }
  }

  return (
    <li className="grid gap-2 text-sm">
      {renaming ? (
        <form onSubmit={submit} className="flex flex-wrap items-end gap-2">
          <div className="grid gap-1">
            <Label htmlFor={nameId}>{t('search.renameLabel', { name: tag.name })}</Label>
            <Input
              id={nameId}
              value={name}
              maxLength={64}
              onChange={(event) => setName(event.target.value)}
              className="w-48"
            />
          </div>
          <Button type="submit" size="sm" disabled={rename.isPending}>
            {t('search.save')}
          </Button>
          <Button type="button" size="sm" variant="ghost" onClick={() => setRenaming(false)}>
            {t('search.cancel')}
          </Button>
        </form>
      ) : (
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-medium">{tag.name}</span>
          <span className="text-muted-foreground">
            {t('search.tagUses', { count: tag.own_count, formatted: fmt.count(tag.own_count) })}
          </span>
          <Button
            type="button"
            size="sm"
            variant="outline"
            className="ml-auto"
            aria-label={t('search.rename', { name: tag.name })}
            onClick={() => setRenaming(true)}
          >
            {t('search.renameButton')}
          </Button>
          <Button
            type="button"
            size="sm"
            variant="outline"
            aria-label={t('search.delete', { name: tag.name })}
            onClick={() => setConfirming(true)}
          >
            {t('search.deleteButton')}
          </Button>
        </div>
      )}
      {confirming && (
        <div
          role="group"
          aria-label={t('search.deleteConfirm', { name: tag.name })}
          className="flex flex-wrap items-center gap-2 rounded-md border border-destructive/40 p-2"
        >
          <span>{t('search.deleteConfirm', { name: tag.name })}</span>
          <Button
            type="button"
            size="sm"
            variant="destructive"
            disabled={remove.isPending}
            onClick={() => remove.mutate()}
          >
            {t('search.deleteConfirmYes')}
          </Button>
          <Button type="button" size="sm" variant="ghost" onClick={() => setConfirming(false)}>
            {t('search.cancel')}
          </Button>
        </div>
      )}
      {rename.isError && (
        <ErrorBanner
          error={rename.error}
          overrides={{ tag_exists: t('detail.tagExists') }}
          onDismiss={() => rename.reset()}
        />
      )}
      {remove.isError && <ErrorBanner error={remove.error} onDismiss={() => remove.reset()} />}
    </li>
  )
}
