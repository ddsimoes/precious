// check-dist asserts that the built index.html keeps the strict CSP
// (script-src 'self'; style-src 'self'): no inline script or style, no inline
// event handlers, and nothing loaded from another origin.
//
// Usage: node scripts/check-dist.mjs [path/to/index.html]

import { readFileSync } from 'node:fs'
import { fileURLToPath, pathToFileURL } from 'node:url'

// inlineViolations returns one message per CSP problem found in html.
export function inlineViolations(html) {
  const found = []
  for (const match of html.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script\s*>/gi)) {
    const [, attrs, body] = match
    if (!/\ssrc\s*=/i.test(attrs)) {
      found.push(`inline <script> without src: ${match[0].slice(0, 80)}`)
    } else if (body.trim() !== '') {
      found.push(`<script src> with an inline body: ${match[0].slice(0, 80)}`)
    }
  }
  if (/<style\b/i.test(html)) {
    found.push('inline <style> element')
  }
  for (const match of html.matchAll(/<[a-z][^>]*?\s(style|on[a-z]+)\s*=/gi)) {
    found.push(`inline ${match[1]} attribute: ${match[0].slice(0, 80)}`)
  }
  for (const match of html.matchAll(/\s(?:src|href)\s*=\s*["']?((?:[a-z][a-z0-9+.-]*:|\/\/)[^"'\s>]*)/gi)) {
    found.push(`resource from another origin: ${match[1]}`)
  }
  return found
}

if (process.argv[1] !== undefined && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const file = process.argv[2] ?? fileURLToPath(new URL('../../dist/index.html', import.meta.url))
  const found = inlineViolations(readFileSync(file, 'utf8'))
  if (found.length > 0) {
    console.error(`${file} breaks the content security policy:\n  ${found.join('\n  ')}`)
    process.exit(1)
  }
  console.log(`${file}: no inline script or style`)
}
