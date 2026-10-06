import { fireEvent, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import type { EntryRow, TextContent } from '@/api/entries'
import { entryDetail, entryRow, fotosSource } from '@/test/fixtures'
import { errorResponse, jsonResponse, renderApp, signedIn, stubApi } from '@/test/renderApp'

type Answer = () => Response

// showPreview opens the detail panel of file and returns its preview. text
// answers /text and content answers /content; without them a request fails
// the test.
async function showPreview(file: EntryRow, { text, content }: { text?: Answer; content?: Answer } = {}) {
  const routes: Record<string, () => Response> = {
    'GET /api/session': () => jsonResponse(200, signedIn),
    'GET /api/sources': () => jsonResponse(200, { sources: [fotosSource()] }),
    'GET /api/tags': () => jsonResponse(200, { tags: [] }),
    [`GET /api/entries/${file.id}`]: () => jsonResponse(200, entryDetail(file)),
  }
  if (text !== undefined) {
    routes[`GET /api/entries/${file.id}/text`] = text
  }
  if (content !== undefined) {
    routes[`GET /api/entries/${file.id}/content`] = content
  }
  const requests = stubApi(routes)
  renderApp(`/search?entry=${file.id}`)
  const panel = await screen.findByRole('complementary', { name: file.name })
  return { panel, requests }
}

function file(name: string, overrides: Partial<EntryRow> = {}): EntryRow {
  return entryRow({ id: '40', name, name_b64: btoa(name), path: `Docs/${name}`, file_kind: 'document', ...overrides })
}

function textOf(text: string, overrides: Partial<TextContent> = {}): Answer {
  return () => jsonResponse(200, { encoding: 'UTF-8', text, truncated: false, language: null, markdown: false, ...overrides })
}

const partial: Answer = () => new Response('x', { status: 206, headers: { 'Content-Range': 'bytes 0-0/10' } })

describe('Preview in the detail panel', () => {
  it('shows an image from the content endpoint and opens the viewer on click', async () => {
    const { panel, requests } = await showPreview(file('NATAL.JPG', { file_kind: 'image' }))
    const preview = within(panel).getByRole('region', { name: 'Preview' })
    const image = within(preview).getByAltText('NATAL.JPG')
    expect(image).toHaveAttribute('src', '/api/entries/40/content')
    expect(image).toHaveAttribute('loading', 'lazy')
    expect(image).toHaveAttribute('decoding', 'async')
    // The browser loads the image itself: no API request for it.
    expect(requests.some((r) => new URL(r.url).pathname.endsWith('/content'))).toBe(false)
    expect(within(panel).getByRole('button', { name: 'Open' })).toBeInTheDocument()

    await userEvent.click(within(preview).getByRole('button', { name: 'View NATAL.JPG full size' }))
    expect(screen.getByRole('dialog', { name: 'NATAL.JPG' })).toBeInTheDocument()
  })

  it('says a file that changed on disk must be rescanned when its image cannot load', async () => {
    const { panel, requests } = await showPreview(file('NATAL.JPG', { file_kind: 'image' }), {
      content: () => errorResponse(409, 'invalid_entry_state', 'file changed'),
    })
    fireEvent.error(within(panel).getByAltText('NATAL.JPG'))
    expect(await within(panel).findByRole('alert')).toHaveTextContent(
      'This file changed on disk after the last scan. Scan its source again to preview it.',
    )
    const probe = requests.find((r) => new URL(r.url).pathname.endsWith('/content'))
    expect(probe?.headers.get('Range')).toBe('bytes=0-0')
    expect(within(panel).queryByAltText('NATAL.JPG')).toBeNull()
    // The rest of the panel stays.
    expect(within(panel).getByRole('button', { name: 'Open' })).toBeInTheDocument()
    expect(within(panel).getByText('Classification')).toBeInTheDocument()
  })

  it('says the disk is not connected when a video cannot load', async () => {
    const { panel } = await showPreview(file('festa.mp4', { file_kind: 'video' }), {
      content: () => errorResponse(409, 'source_offline', 'volume not mounted'),
    })
    const player = within(panel).getByLabelText('festa.mp4')
    expect(player.tagName).toBe('VIDEO')
    expect(player).toHaveAttribute('controls')
    expect(player).toHaveAttribute('preload', 'metadata')
    expect(player).toHaveAttribute('src', '/api/entries/40/content')
    fireEvent.error(player)
    expect(await within(panel).findByRole('alert')).toHaveTextContent(
      'The disk of this source is not connected. Connect it to preview this file.',
    )
  })

  it('offers the download when the browser cannot decode served content', async () => {
    const { panel } = await showPreview(file('NATAL.JPG', { file_kind: 'image' }), { content: partial })
    fireEvent.error(within(panel).getByAltText('NATAL.JPG'))
    expect(await within(panel).findByText(/cannot be shown here/)).toBeInTheDocument()
    expect(within(panel).getByRole('link', { name: 'Download' })).toHaveAttribute('download', 'NATAL.JPG')
  })

  it('plays audio with controls', async () => {
    const { panel } = await showPreview(file('musica.mp3', { file_kind: 'audio' }))
    const player = within(panel).getByLabelText('musica.mp3')
    expect(player.tagName).toBe('AUDIO')
    expect(player).toHaveAttribute('controls')
    expect(player).toHaveAttribute('preload', 'metadata')
  })

  it('frames a PDF once its content is available', async () => {
    const { panel } = await showPreview(file('manual.pdf'), { content: partial })
    const frame = await within(panel).findByTitle('PDF document manual.pdf')
    expect(frame.tagName).toBe('IFRAME')
    expect(frame).toHaveAttribute('src', '/api/entries/40/content')
    expect(frame).not.toHaveAttribute('sandbox')
  })

  it('does not frame a PDF that changed on disk', async () => {
    const { panel } = await showPreview(file('manual.pdf'), {
      content: () => errorResponse(409, 'invalid_entry_state', 'file changed'),
    })
    expect(await within(panel).findByRole('alert')).toHaveTextContent(/Scan its source again to preview it/)
    expect(within(panel).queryByTitle('PDF document manual.pdf')).toBeNull()
  })

  it('shows the first 40 lines of text', async () => {
    const lines = Array.from({ length: 50 }, (_, i) => `linha ${i + 1}`)
    const { panel } = await showPreview(file('notas.txt'), { text: textOf(`${lines.join('\n')}\n`) })
    const body = await within(panel).findByLabelText('Contents of notas.txt')
    expect(body).toHaveTextContent('linha 40')
    expect(body).not.toHaveTextContent('linha 41')
    expect(within(panel).getByText(/The first 40 lines are shown/)).toBeInTheDocument()
  })

  it('shows a short text whole, without the first-lines note', async () => {
    const lines = Array.from({ length: 40 }, (_, i) => `linha ${i + 1}`)
    const { panel } = await showPreview(file('notas.txt'), { text: textOf(`${lines.join('\n')}\n`) })
    expect(await within(panel).findByLabelText('Contents of notas.txt')).toHaveTextContent('linha 40')
    expect(within(panel).queryByText(/The first 40 lines are shown/)).toBeNull()
  })

  it('highlights source code', async () => {
    const { panel } = await showPreview(file('main.go', { file_kind: 'source' }), {
      text: textOf('package main\n\nfunc main() { println("<b>hi</b>") }\n', { language: 'go' }),
    })
    const code = await within(panel).findByLabelText('Contents of main.go')
    expect(code.querySelector('.hljs-keyword')).toHaveTextContent('package')
    expect(code.querySelector('b')).toBeNull()
  })

  it('renders Markdown sanitized: no script, event handler, remote image, or app navigation', async () => {
    const { panel } = await showPreview(file('README.md'), {
      text: textOf(
        [
          '# Leia-me',
          '',
          '<script>alert(1)</script>',
          '',
          '<img src=x onerror=alert(2)>',
          '',
          '<p onclick="alert(3)">para</p>',
          '',
          '![pixel](https://tracker.test/pixel.png)',
          '',
          '[site](https://example.test/) and [map](/map/1) and [js](javascript:alert(4))',
        ].join('\n'),
        { language: 'markdown', markdown: true },
      ),
    })
    const article = await within(panel).findByRole('article', { name: 'Contents of README.md' })
    expect(within(article).getByRole('heading', { name: 'Leia-me' })).toBeInTheDocument()
    expect(article.querySelector('script, img, [onerror], [onclick]')).toBeNull()
    expect(article.innerHTML).not.toMatch(/alert\(|tracker\.test/)
    expect(article).toHaveTextContent('[image not shown: pixel]')
    const [site, map, js] = Array.from(article.querySelectorAll('a'))
    expect(site).toHaveAttribute('target', '_blank')
    expect(site).toHaveAttribute('rel', 'noopener noreferrer nofollow')
    expect(map).not.toHaveAttribute('href')
    expect(js).not.toHaveAttribute('href')
  })

  it('says a text file must be rescanned when it changed on disk', async () => {
    const { panel } = await showPreview(file('notas.txt'), {
      text: () => errorResponse(409, 'invalid_entry_state', 'file changed'),
    })
    expect(await within(panel).findByRole('alert')).toHaveTextContent(
      'This file changed on disk after the last scan. Scan its source again to preview it.',
    )
  })

  it('says the disk is not connected for a text file on it', async () => {
    const { panel } = await showPreview(file('notas.txt'), {
      text: () => errorResponse(409, 'source_offline', 'volume not mounted'),
    })
    expect(await within(panel).findByRole('alert')).toHaveTextContent(
      'The disk of this source is not connected. Connect it to preview this file.',
    )
  })

  it('offers other types as a download without fetching them', async () => {
    const { panel, requests } = await showPreview(file('Setup.exe', { file_kind: 'installer' }))
    const preview = within(panel).getByRole('region', { name: 'Preview' })
    expect(within(preview).getByText('No preview for this type of file.')).toBeInTheDocument()
    expect(within(preview).getByRole('link', { name: 'Download' })).toHaveAttribute('href', '/api/entries/40/content')
    expect(requests.some((r) => /\/(text|content)$/.test(new URL(r.url).pathname))).toBe(false)
  })

  it('has no preview for a missing file', async () => {
    const { panel, requests } = await showPreview(file('NATAL.JPG', { file_kind: 'image', state: 'missing' }))
    expect(within(panel).getByText('This item was not found in the last scan.')).toBeInTheDocument()
    expect(within(panel).queryByRole('region', { name: 'Preview' })).toBeNull()
    expect(within(panel).queryByAltText('NATAL.JPG')).toBeNull()
    expect(requests.some((r) => /\/(text|content)$/.test(new URL(r.url).pathname))).toBe(false)
  })
})
