import DOMPurify from 'dompurify'
import { Marked } from 'marked'

// Markdown from a disk is rendered by marked, then sanitized by DOMPurify
// (file-viewer spec, design D12): no script, event handler, style, form, or
// embedded frame survives, nothing remote is loaded, and links open outside
// the application.

const marked = new Marked({ gfm: true })

const purifyConfig = {
  USE_PROFILES: { html: true },
  // Anything that embeds or fetches other content, runs, or styles.
  FORBID_TAGS: [
    'style',
    'link',
    'meta',
    'base',
    'form',
    'input',
    'button',
    'textarea',
    'select',
    'option',
    'iframe',
    'frame',
    'frameset',
    'object',
    'embed',
    'video',
    'audio',
    'source',
    'track',
    'picture',
  ],
  // Inline styles would break the strict CSP; the others fetch content.
  FORBID_ATTR: ['style', 'srcset', 'background', 'poster', 'ping', 'action', 'formaction'],
  ALLOW_DATA_ATTR: false,
  // Ids from the file must not shadow the application's globals.
  SANITIZE_NAMED_PROPS: true,
}

// renderMarkdown returns the file's Markdown as sanitized HTML. Images are
// replaced by the text imageText makes from their description: a remote one
// would leave the machine, and a relative one has nothing to point at.
// Links to web pages open in a new tab with no opener; every other link
// loses its target.
export function renderMarkdown(text: string, imageText: (alt: string) => string): string {
  const html = marked.parse(text, { async: false })
  const fragment = DOMPurify.sanitize(html, { ...purifyConfig, RETURN_DOM_FRAGMENT: true })
  for (const image of fragment.querySelectorAll('img')) {
    image.replaceWith(document.createTextNode(imageText(image.getAttribute('alt') ?? '')))
  }
  for (const link of fragment.querySelectorAll('a')) {
    const href = link.getAttribute('href') ?? ''
    if (/^(https?:|mailto:)/i.test(href)) {
      link.setAttribute('target', '_blank')
      link.setAttribute('rel', 'noopener noreferrer nofollow')
    } else {
      link.removeAttribute('href')
      link.removeAttribute('target')
    }
  }
  const container = document.createElement('div')
  container.append(fragment)
  return container.innerHTML
}
