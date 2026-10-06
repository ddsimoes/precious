import { describe, expect, it } from 'vitest'

import { inlineViolations } from './check-dist.mjs'

const clean = `<!doctype html>
<html lang="en">
  <head>
    <link rel="icon" type="image/svg+xml" href="/assets/favicon-abc.svg" />
    <script type="module" crossorigin src="/assets/index-abc.js"></script>
    <link rel="stylesheet" crossorigin href="/assets/index-abc.css">
  </head>
  <body><div id="root"></div></body>
</html>`

describe('inlineViolations', () => {
  it('accepts a page that loads only same-origin files', () => {
    expect(inlineViolations(clean)).toEqual([])
  })

  it.each([
    ['an inline script', '<script>alert(1)</script>'],
    ['an inline module script', '<script type="module">import "/x.js"</script>'],
    ['a script with src and a body', '<script src="/a.js">alert(1)</script>'],
    ['a style element', '<style>body{}</style>'],
    ['a style attribute', '<div style="color:red"></div>'],
    ['an event handler', '<img src="/a.png" onerror="alert(1)">'],
    ['a CDN script', '<script src="https://cdn.example/x.js"></script>'],
    ['a protocol-relative font', '<link rel="stylesheet" href="//fonts.example/css">'],
    ['a data URL', '<link rel="icon" href="data:image/svg+xml,abc">'],
  ])('rejects %s', (_, snippet) => {
    expect(inlineViolations(clean.replace('<body>', `<body>${snippet}`))).not.toEqual([])
  })
})
