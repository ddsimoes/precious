import type { EntryRow } from '@/api/entries'

// ViewerKind is how the viewer shows a file. The media kinds follow the
// server's content type table (design D12), so the browser gets a type it
// can show; text goes through GET /api/entries/{id}/text.
export type ViewerKind = 'image' | 'video' | 'audio' | 'pdf' | 'text' | 'download'

const byExtension: Record<string, ViewerKind> = {
  jpg: 'image',
  jpeg: 'image',
  png: 'image',
  gif: 'image',
  webp: 'image',
  avif: 'image',
  bmp: 'image',
  svg: 'image',
  mp4: 'video',
  m4v: 'video',
  webm: 'video',
  mov: 'video',
  mp3: 'audio',
  m4a: 'audio',
  aac: 'audio',
  ogg: 'audio',
  opus: 'audio',
  wav: 'audio',
  flac: 'audio',
  pdf: 'pdf',
  // Plain text, data, and markup files that are not source code; they are
  // shown as text, never rendered.
  txt: 'text',
  text: 'text',
  log: 'text',
  md: 'text',
  markdown: 'text',
  csv: 'text',
  tsv: 'text',
  json: 'text',
  xml: 'text',
  html: 'text',
  htm: 'text',
  yaml: 'text',
  yml: 'text',
  toml: 'text',
  ini: 'text',
  cfg: 'text',
  conf: 'text',
  nfo: 'text',
  srt: 'text',
  diz: 'text',
}

export function viewerKind(entry: Pick<EntryRow, 'name' | 'file_kind'>): ViewerKind {
  const dot = entry.name.lastIndexOf('.')
  const ext = dot > 0 ? entry.name.slice(dot + 1).toLowerCase() : ''
  return byExtension[ext] ?? (entry.file_kind === 'source' ? 'text' : 'download')
}
