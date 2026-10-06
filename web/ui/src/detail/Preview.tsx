import { useQuery } from '@tanstack/react-query'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  contentProbeQueryKey,
  contentUrl,
  fetchText,
  probeContent,
  textQueryKey,
  type EntryRow,
} from '@/api/entries'
import { ErrorBanner } from '@/app/ErrorBanner'
import { Button } from '@/components/ui/button'
import { TextBody } from '@/viewer/Viewer'
import { viewerKind } from '@/viewer/viewerKind'

// previewLines is how much of a text file the panel shows (design D22).
const previewLines = 40

interface PreviewProps {
  entry: EntryRow
  // onOpen opens the file in the full viewer.
  onOpen: () => void
}

// Preview shows a file in the detail panel without opening the viewer
// (design D22): an image scaled to the panel, video and audio players, a PDF
// in a frame, and the first lines of text, source code, and Markdown, with
// the viewer's safety rules (design D12). Other types offer the download. A
// file that was not present in the last scan has no preview.
export function Preview({ entry, onOpen }: PreviewProps) {
  const { t } = useTranslation()
  if (entry.state !== 'present') {
    return null
  }
  return (
    <section aria-label={t('detail.preview.label')} className="grid gap-2">
      <PreviewBody key={entry.id} entry={entry} onOpen={onOpen} />
    </section>
  )
}

function PreviewBody({ entry, onOpen }: PreviewProps) {
  const { t } = useTranslation()
  const kind = viewerKind(entry)
  switch (kind) {
    case 'image':
    case 'video':
    case 'audio':
      return <MediaPreview entry={entry} kind={kind} onOpen={onOpen} />
    case 'pdf':
      return <PdfPreview entry={entry} />
    case 'text':
      return <TextPreview entry={entry} />
    case 'download':
      return <DownloadOffer entry={entry} message={t('detail.preview.none')} />
  }
}

// MediaPreview lets the browser load the file itself. When it cannot, a
// one-byte request for the same content tells why: a refusal is shown as
// such, and content the browser cannot decode is offered as a download.
function MediaPreview({
  entry,
  kind,
  onOpen,
}: PreviewProps & { kind: 'image' | 'video' | 'audio' }) {
  const { t } = useTranslation()
  const [attempt, setAttempt] = useState(0)
  const [failed, setFailed] = useState(false)
  const probe = useQuery({
    queryKey: contentProbeQueryKey(entry.id, attempt),
    queryFn: ({ signal }) => probeContent(entry.id, signal),
    enabled: failed,
    staleTime: Infinity,
  })

  if (failed) {
    if (probe.isPending) {
      return <Loading />
    }
    if (probe.isError) {
      return (
        <ContentError
          error={probe.error}
          onRetry={() => {
            setFailed(false)
            setAttempt((n) => n + 1)
          }}
        />
      )
    }
    return <DownloadOffer entry={entry} message={t('detail.preview.failed')} />
  }

  const src = contentUrl(entry.id)
  switch (kind) {
    case 'image':
      return (
        <button
          key={attempt}
          type="button"
          onClick={onOpen}
          aria-label={t('detail.preview.viewImage', { name: entry.name })}
          className="block cursor-zoom-in rounded-md border bg-muted/40 p-1 focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
        >
          <img
            src={src}
            alt={entry.name}
            loading="lazy"
            decoding="async"
            onError={() => setFailed(true)}
            className="mx-auto max-h-80 max-w-full object-contain"
          />
        </button>
      )
    case 'video':
      return (
        <video
          key={attempt}
          src={src}
          controls
          preload="metadata"
          aria-label={entry.name}
          onError={() => setFailed(true)}
          className="max-h-80 w-full rounded-md"
        />
      )
    case 'audio':
      return (
        <audio
          key={attempt}
          src={src}
          controls
          preload="metadata"
          aria-label={entry.name}
          onError={() => setFailed(true)}
          className="w-full"
        />
      )
  }
}

// PdfPreview checks the content before framing it: a frame cannot tell a
// refusal from a document.
function PdfPreview({ entry }: { entry: EntryRow }) {
  const { t } = useTranslation()
  const probe = useQuery({
    queryKey: contentProbeQueryKey(entry.id, 0),
    queryFn: ({ signal }) => probeContent(entry.id, signal),
    staleTime: Infinity,
  })

  if (probe.isPending) {
    return <Loading />
  }
  if (probe.isError) {
    return <ContentError error={probe.error} onRetry={() => void probe.refetch()} />
  }
  // No sandbox attribute: browsers refuse to show a PDF in a sandboxed
  // frame. The response's own policy keeps it inert (design D12).
  return (
    <iframe
      src={contentUrl(entry.id)}
      title={t('viewer.pdf', { name: entry.name })}
      className="h-96 w-full rounded-md border"
    />
  )
}

// TextPreview shows the first lines of the text the viewer shows, from the
// same request.
function TextPreview({ entry }: { entry: EntryRow }) {
  const { t } = useTranslation()
  const text = useQuery({
    queryKey: textQueryKey(entry.id),
    queryFn: ({ signal }) => fetchText(entry.id, signal),
    // The file changes only with a rescan.
    staleTime: Infinity,
  })
  const head = useMemo(() => {
    if (text.data === undefined) {
      return null
    }
    const lines = text.data.text.split('\n')
    if (lines.at(-1) === '') {
      lines.pop()
    }
    return {
      content: { ...text.data, text: lines.slice(0, previewLines).join('\n') },
      more: text.data.truncated || lines.length > previewLines,
    }
  }, [text.data])

  if (text.isError) {
    return <ContentError error={text.error} onRetry={() => void text.refetch()} />
  }
  if (head === null) {
    return <Loading />
  }
  return (
    <>
      <TextBody name={entry.name} content={head.content} className="max-h-80" />
      {head.more && (
        <p className="text-xs text-muted-foreground">{t('detail.preview.firstLines', { count: previewLines })}</p>
      )}
    </>
  )
}

function ContentError({ error, onRetry }: { error: unknown; onRetry: () => void }) {
  const { t } = useTranslation()
  return (
    <ErrorBanner
      error={error}
      overrides={{
        invalid_entry_state: t('detail.preview.changed'),
        source_offline: t('detail.preview.offline'),
      }}
      onRetry={onRetry}
    />
  )
}

function DownloadOffer({ entry, message }: { entry: EntryRow; message: string }) {
  const { t } = useTranslation()
  return (
    <div className="flex flex-wrap items-center gap-2 text-sm">
      <p className="text-muted-foreground">{message}</p>
      <Button asChild size="sm" variant="outline">
        <a href={contentUrl(entry.id)} download={entry.name}>
          {t('viewer.download')}
        </a>
      </Button>
    </div>
  )
}

function Loading() {
  const { t } = useTranslation()
  return (
    <p role="status" className="text-sm text-muted-foreground">
      {t('app.loading')}
    </p>
  )
}
