import { useInfiniteQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { Link } from 'react-router'

import {
  copiesQueryKey,
  fetchCopies,
  type ArchiveFormat,
  type ArchiveInfo,
  type Copy,
  type Coverage,
  type EntryContent,
  type Relation,
} from '@/api/content'
import type { EntryRow } from '@/api/entries'
import { CoverageClaim } from '@/components/CoverageFigures'
import { Button } from '@/components/ui/button'
import { useEntryLink } from '@/detail/useEntryLink'
import { useFormat } from '@/lib/format'
import { useSourceLabel } from '@/lib/sourceParams'

// The detail panel's duplicates (spec §11.8, R2 design D8, D16): a file's
// copies, a folder's relations, and an archive's listing. Every claim that
// nothing else holds the same content carries the share checked on every
// disk (I7).

// formatNames are the usual names of the archive formats.
const formatNames: Record<ArchiveFormat, string> = {
  zip: 'ZIP',
  tar: 'TAR',
  tar_gzip: 'TAR.GZ',
  tar_bzip2: 'TAR.BZ2',
  gzip: 'GZ',
  bzip2: 'BZ2',
}

export function CopiesSection({
  entry,
  content,
  coverage,
}: {
  entry: EntryRow
  content: EntryContent
  coverage: Coverage
}) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const [more, setMore] = useState(false)
  const allCopies = useInfiniteQuery({
    queryKey: copiesQueryKey(entry.id),
    queryFn: ({ pageParam, signal }) => fetchCopies(entry.id, pageParam, signal),
    initialPageParam: null as string | null,
    getNextPageParam: (page) => page.next_cursor,
    enabled: more,
  })
  const listed = more && allCopies.data !== undefined ? allCopies.data.pages.flatMap((page) => page.items) : content.copies
  // The copies are the content's other places: the entry itself is not one.
  const others = listed.filter((copy) => copy.ref !== entry.id)
  const otherCount = entry.copies === null ? others.length : Math.max(0, entry.copies - 1)
  const { state } = content

  // A claim that there is no other copy is made only when hashing proved it
  // (R2 design D8); otherwise the panel says why it cannot tell.
  let body
  if (state === 'pending' || state === 'changed' || state === 'unreadable') {
    body = <p className="text-sm">{t(`detail.notClaimed.${state}`)}</p>
  } else if (otherCount === 0) {
    body = <p className="text-sm font-medium">{t(`detail.claim.${state}`)}</p>
  } else {
    body = (
      <>
        <p className="text-sm font-medium">
          {t('detail.otherCopies', { count: otherCount, formatted: fmt.count(otherCount) })}
        </p>
        <CopyList copies={others} />
        {(more ? allCopies.hasNextPage : otherCount > others.length) && (
          <div>
            <Button
              size="sm"
              variant="outline"
              disabled={allCopies.isFetching}
              onClick={() => (more ? void allCopies.fetchNextPage() : setMore(true))}
            >
              {t('detail.moreCopies')}
            </Button>
          </div>
        )}
      </>
    )
  }
  return (
    <>
      {body}
      <CoverageClaim coverage={coverage} />
    </>
  )
}

function CopyList({ copies }: { copies: Copy[] }) {
  const { t } = useTranslation()
  const entryLink = useEntryLink()
  const sourceLabel = useSourceLabel()
  return (
    <ul aria-label={t('detail.copiesList')} className="grid gap-2 text-sm">
      {copies.map((copy) => (
        <li key={copy.ref} className="grid gap-0.5">
          <Link to={{ search: entryLink(copy.ref) }} className="break-all text-primary hover:underline">
            {copy.path}
          </Link>
          <span className="text-xs text-muted-foreground">
            {[
              sourceLabel(copy.source_id),
              t('review.decision', { decision: t(`home.decision.${copy.eff_decision}`) }),
              ...(copy.hard_link ? [t('detail.hardLink')] : []),
              ...(copy.offline ? [t('detail.offline')] : []),
              ...(copy.archive_id !== null ? [t('detail.inArchive')] : []),
            ].join(' · ')}
          </span>
        </li>
      ))}
    </ul>
  )
}

export function RelationsSection({
  entry,
  relations,
  coverage,
}: {
  entry: EntryRow
  relations: Relation[]
  coverage: Coverage
}) {
  const { t } = useTranslation()
  const fmt = useFormat()
  const entryLink = useEntryLink()
  const amount = (a: { files: number; bytes: number }) =>
    `${t('units.files', { count: a.files, formatted: fmt.count(a.files) })} (${fmt.bytes(a.bytes)})`

  if (relations.length === 0) {
    return (
      <>
        <p className="text-sm">{t('detail.noRelations')}</p>
        <CoverageClaim coverage={coverage} />
      </>
    )
  }
  return (
    <>
      <ul aria-label={t('detail.relationsList')} className="grid gap-3 text-sm">
        {relations.map((relation) => (
          <li key={relation.id} className="grid gap-1">
            <span>
              <Trans
                i18nKey={
                  relation.kind === 'same'
                    ? 'detail.relation.same'
                    : `detail.relation.${relation.kind}${relation.self === 'a' ? 'Self' : 'Other'}`
                }
                values={{ path: relation.other.path }}
                components={{
                  otherLink: (
                    <Link to={{ search: entryLink(relation.other.id) }} className="break-all text-primary underline" />
                  ),
                }}
              />
            </span>
            <span className="text-xs text-muted-foreground">
              {t('detail.relationFigures', {
                matched: fmt.bytes(relation.matched_bytes),
                here: amount(relation.only_here),
                there: amount(relation.only_there),
              })}
            </span>
            <div>
              <Button asChild size="sm" variant="outline">
                <Link
                  to={{
                    pathname: '/compare',
                    search: `?${new URLSearchParams({ left: entry.id, right: relation.other.id })}`,
                  }}
                >
                  {t('detail.compare')}
                </Link>
              </Button>
            </div>
          </li>
        ))}
      </ul>
      <CoverageClaim coverage={coverage} />
    </>
  )
}

export function ArchiveSection({ entry, archive }: { entry: EntryRow; archive: ArchiveInfo }) {
  const { t } = useTranslation()
  const fmt = useFormat()
  return (
    <>
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
        <dt className="text-muted-foreground">{t('detail.archiveFormat')}</dt>
        <dd>{formatNames[archive.format]}</dd>
        <dt className="text-muted-foreground">{t('detail.archiveState')}</dt>
        <dd>{t(`detail.archiveStates.${archive.state}`)}</dd>
        {archive.state === 'complete' && (
          <>
            <dt className="text-muted-foreground">{t('detail.archiveMembers')}</dt>
            <dd>{fmt.count(archive.members)}</dd>
            <dt className="text-muted-foreground">{t('detail.archiveUnpacked')}</dt>
            <dd>{fmt.bytes(archive.unpacked_bytes)}</dd>
          </>
        )}
      </dl>
      {archive.state === 'complete' && (
        <div>
          <Button asChild size="sm" variant="outline">
            <Link to={`/map/${entry.id}`}>{t('detail.openArchive')}</Link>
          </Button>
        </div>
      )}
    </>
  )
}
