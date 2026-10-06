import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import type { EntryRow, TextContent } from '@/api/entries'
import { entryDetail, entryRow, fotosSource } from '@/test/fixtures'
import { jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'
import { renderMarkdown } from '@/viewer/markdown'

const imageText = (alt: string) => `[image: ${alt}]`

function sanitized(markdown: string): HTMLElement {
  const container = document.createElement('div')
  container.innerHTML = renderMarkdown(markdown, imageText)
  return container
}

describe('Markdown rendering', () => {
  it('strips scripts, event handlers, styles, and frames', () => {
    const html = sanitized(
      [
        '# Title',
        '',
        '<script>alert(1)</script>',
        '',
        '<p onclick="alert(2)" style="color:red">para</p>',
        '',
        '<img src=x onerror=alert(3)>',
        '',
        '<iframe src="https://evil.test/"></iframe>',
        '',
        '<style>body{display:none}</style>',
        '',
        '<a href="javascript:alert(4)">js link</a>',
      ].join('\n'),
    )
    expect(html.querySelector('h1')).toHaveTextContent('Title')
    expect(html.querySelector('script')).toBeNull()
    expect(html.querySelector('iframe')).toBeNull()
    expect(html.querySelector('style')).toBeNull()
    expect(html.querySelector('[onclick], [onerror], [style]')).toBeNull()
    expect(html.innerHTML).not.toMatch(/onerror|onclick|alert\(/)
    expect(html.querySelector('a')).not.toHaveAttribute('href')
    expect(html.textContent).toContain('para')
  })

  it('replaces images, remote ones included, with their description', () => {
    const html = sanitized(
      '![family photo](https://tracker.test/pixel.png) and ![local](foto.jpg)\n\n<img src="http://evil.test/x.gif" srcset="http://evil.test/y.gif 2x" alt="raw">',
    )
    expect(html.querySelector('img')).toBeNull()
    expect(html.innerHTML).not.toContain('tracker.test')
    expect(html.innerHTML).not.toContain('evil.test')
    expect(html.textContent).toContain('[image: family photo]')
    expect(html.textContent).toContain('[image: local]')
    expect(html.textContent).toContain('[image: raw]')
  })

  it('opens web links outside the application and drops other targets', () => {
    const html = sanitized('[site](https://example.test/) and [page](other.md) and [mail](mailto:a@b.test)')
    const [site, page, mail] = Array.from(html.querySelectorAll('a'))
    expect(site).toHaveAttribute('href', 'https://example.test/')
    expect(site).toHaveAttribute('target', '_blank')
    expect(site).toHaveAttribute('rel', 'noopener noreferrer nofollow')
    expect(page).not.toHaveAttribute('href')
    expect(mail).toHaveAttribute('target', '_blank')
  })
})

// openViewer opens the detail panel of file, then the viewer.
async function openViewer(file: EntryRow, text?: TextContent) {
  const requests = stubApi({
    'GET /api/session': () => jsonResponse(200, signedIn),
    'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource()] }),
    'GET /api/tags': () => jsonResponse(200, { tags: [] }),
    [`GET /api/entries/${file.id}`]: () => jsonResponse(200, entryDetail(file)),
    [`GET /api/entries/${file.id}/text`]: () => jsonResponse(200, text),
  })
  renderApp(`/search?entry=${file.id}`)
  const panel = await screen.findByRole('complementary', { name: file.name })
  await userEvent.click(within(panel).getByRole('button', { name: 'Open' }))
  const dialog = within(screen.getByRole('dialog', { name: file.name }))
  return { dialog, requests }
}

function file(name: string, overrides: Partial<EntryRow> = {}): EntryRow {
  return entryRow({ id: '40', name, name_b64: btoa(name), path: `Docs/${name}`, file_kind: 'document', ...overrides })
}

describe('Viewer', () => {
  it('shows images from the content endpoint', async () => {
    const image = await openViewer(file('NATAL.JPG', { file_kind: 'image' }))
    expect(image.dialog.getByRole('img', { name: 'NATAL.JPG' })).toHaveAttribute('src', '/api/entries/40/content')
    expect(image.dialog.getByRole('link', { name: 'Download' })).toHaveAttribute('href', '/api/entries/40/content')
  })

  it('plays video and audio with controls', async () => {
    const video = await openViewer(file('festa.mp4', { file_kind: 'video' }))
    const player = video.dialog.getByLabelText('festa.mp4')
    expect(player.tagName).toBe('VIDEO')
    expect(player).toHaveAttribute('controls')
    expect(player).toHaveAttribute('src', '/api/entries/40/content')
  })

  it('plays audio with controls', async () => {
    const audio = await openViewer(file('musica.mp3', { file_kind: 'audio' }))
    const player = audio.dialog.getByLabelText('musica.mp3')
    expect(player.tagName).toBe('AUDIO')
    expect(player).toHaveAttribute('controls')
  })

  it('frames a PDF in the browser’s viewer', async () => {
    const pdf = await openViewer(file('manual.pdf'))
    const frame = pdf.dialog.getByTitle('PDF document manual.pdf')
    expect(frame.tagName).toBe('IFRAME')
    expect(frame).toHaveAttribute('src', '/api/entries/40/content')
  })

  it('highlights source code from the text endpoint', async () => {
    const { dialog } = await openViewer(file('main.go', { file_kind: 'source' }), {
      encoding: 'UTF-8',
      text: 'package main\n\nfunc main() { println("<b>hi</b>") }\n',
      truncated: true,
      language: 'go',
      markdown: false,
    })
    const code = await dialog.findByLabelText('Contents of main.go')
    expect(code.querySelector('.hljs-keyword')).toHaveTextContent('package')
    expect(code.querySelector('b')).toBeNull()
    expect(code).toHaveTextContent('println("<b>hi</b>")')
    expect(dialog.getByText('Text encoding: UTF-8')).toBeInTheDocument()
    expect(dialog.getByText('Only the first 1 MiB of this file is shown.')).toBeInTheDocument()
  })

  it('shows decoded text without a known language as plain text', async () => {
    const { dialog } = await openViewer(file('orcamento.txt'), {
      encoding: 'Windows-1252',
      text: 'Meu orçamento',
      truncated: false,
      language: null,
      markdown: false,
    })
    expect(await dialog.findByLabelText('Contents of orcamento.txt')).toHaveTextContent('Meu orçamento')
    expect(dialog.getByText('Text encoding: Windows-1252')).toBeInTheDocument()
  })

  it('renders Markdown sanitized', async () => {
    const { dialog } = await openViewer(file('README.md'), {
      encoding: 'UTF-8',
      text: '# Leia-me\n\n<script>alert(1)</script>\n\n<img src=x onerror=alert(1)>\n\n![](https://evil.test/a.png)',
      truncated: false,
      language: 'markdown',
      markdown: true,
    })
    const article = await dialog.findByRole('article', { name: 'Contents of README.md' })
    expect(within(article).getByRole('heading', { name: 'Leia-me' })).toBeInTheDocument()
    expect(article.querySelector('script, img, [onerror]')).toBeNull()
    expect(article).toHaveTextContent('[image not shown: ]')
  })

  it('offers other files as a download', async () => {
    const { dialog, requests } = await openViewer(file('Setup.exe', { file_kind: 'installer' }))
    expect(dialog.getByText(/cannot be shown here/)).toBeInTheDocument()
    expect(dialog.getByRole('link', { name: 'Download' })).toHaveAttribute('download', 'Setup.exe')
    await userEvent.click(dialog.getByRole('button', { name: 'Close' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(requests.some((r) => new URL(r.url).pathname.endsWith('/text'))).toBe(false)
  })
})
