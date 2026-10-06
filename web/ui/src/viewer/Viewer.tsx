import { useQuery } from '@tanstack/react-query'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { contentUrl, fetchText, textQueryKey, type EntryRow, type TextContent } from '@/api/entries'
import { cn } from '@/lib/utils'
import { ContentError } from '@/viewer/ContentError'
import { highlightCode } from '@/viewer/highlight'
import { renderMarkdown } from '@/viewer/markdown'
import { viewerKind } from '@/viewer/viewerKind'

// Viewer shows one file in the interface (spec §11.12): images, video, and
// audio natively from GET /api/entries/{id}/content, PDF in the browser's
// own viewer in a frame, text and source code highlighted, and Markdown
// rendered and sanitized. Anything else is offered as a download.
export function Viewer({ entry }: { entry: EntryRow }) {
  const { t } = useTranslation()
  const src = contentUrl(entry.id)

  switch (viewerKind(entry)) {
    case 'image':
      return <img src={src} alt={entry.name} className="mx-auto max-h-[75vh] object-contain" />
    case 'video':
      return <video src={src} controls preload="metadata" aria-label={entry.name} className="max-h-[75vh] w-full" />
    case 'audio':
      return <audio src={src} controls preload="metadata" aria-label={entry.name} className="w-full" />
    case 'pdf':
      // No sandbox attribute: browsers refuse to show a PDF in a sandboxed
      // frame. The response's own policy (default-src 'none';
      // frame-ancestors 'self') keeps it inert (design D12).
      return <iframe src={src} title={t('viewer.pdf', { name: entry.name })} className="h-[75vh] w-full rounded-md border" />
    case 'text':
      return <TextView entry={entry} />
    case 'download':
      return <p className="text-sm">{t('viewer.noPreview')}</p>
  }
}

function TextView({ entry }: { entry: EntryRow }) {
  const { t } = useTranslation()
  const text = useQuery({
    queryKey: textQueryKey(entry.id),
    queryFn: ({ signal }) => fetchText(entry.id, signal),
    // The file changes only with a rescan.
    staleTime: Infinity,
  })

  if (text.isPending) {
    return (
      <p role="status" className="text-sm text-muted-foreground">
        {t('app.loading')}
      </p>
    )
  }
  if (text.isError) {
    return <ContentError entry={entry} error={text.error} onRetry={() => void text.refetch()} />
  }
  return (
    <div className="grid gap-2">
      <p className="text-xs text-muted-foreground">{t('viewer.encoding', { encoding: text.data.encoding })}</p>
      {text.data.truncated && (
        <p role="status" className="text-sm text-muted-foreground">
          {t('viewer.truncated')}
        </p>
      )}
      <TextBody name={entry.name} content={text.data} />
    </div>
  )
}

// TextBody shows decoded text: Markdown rendered and sanitized, anything else
// highlighted when its language is known. className sizes its frame.
export function TextBody({
  name,
  content,
  className = 'max-h-[70vh]',
}: {
  name: string
  content: TextContent
  className?: string
}) {
  const { t } = useTranslation()
  const html = useMemo(() => {
    if (content.markdown) {
      return renderMarkdown(content.text, (alt) => t('viewer.imageRemoved', { alt }))
    }
    return highlightCode(content.text, content.language)
  }, [content, t])
  const label = t('viewer.text', { name })

  if (content.markdown && html !== null) {
    // Sanitized by renderMarkdown.
    return (
      <article
        aria-label={label}
        className={cn('markdown overflow-auto rounded-md border p-4', className)}
        dangerouslySetInnerHTML={{ __html: html }}
      />
    )
  }
  return (
    <pre aria-label={label} className={cn('overflow-auto rounded-md border bg-muted/40 p-3 text-xs', className)}>
      {html === null ? (
        <code>{content.text}</code>
      ) : (
        // highlight.js escapes the text and adds only class-styled spans.
        <code className="hljs" dangerouslySetInnerHTML={{ __html: html }} />
      )}
    </pre>
  )
}
