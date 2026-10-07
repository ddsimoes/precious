import { mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { devices, expect, test, type BrowserContext, type Page } from '@playwright/test'

import { awaitDuplicates } from './duplicates'
import { adminPassword, corpusPath, origin } from './env'

// Screens fit the window (design D23): at 1366×768 and 1920×1080, on the Map
// (with and without the detail panel), on Search, on the panel itself, and
// on Opportunities, a review list, and Compare, nothing extends past
// its card or the window and no two controls overlap. Each screen is also
// saved under test-results/layout/ for review.
//
// Files run in alphabetical order, so this one runs before precious.spec.ts,
// which starts from an index without sources and adds the corpus itself.
// This file adds the corpus as a source of its own and removes it at the
// end, leaving the server as it found it.
test.describe.configure({ mode: 'serial' })

const programs = 'Backup_PC_2004/C/Arquivos de programas'
const shots = fileURLToPath(new URL('../test-results/layout/', import.meta.url))
const viewports = [
  { width: 1366, height: 768 },
  { width: 1920, height: 1080 },
]

let context: BrowserContext
let page: Page
let problems: string[] = []
let sourceId = ''
let rootId = ''

interface Item {
  id: string
  name: string
  kind: string
}

test.beforeAll(async ({ browser }) => {
  mkdirSync(shots, { recursive: true })
  context = await browser.newContext({
    ...devices['Desktop Chrome'],
    baseURL: origin(),
    locale: 'en-US',
    timezoneId: 'UTC',
  })
  context.setDefaultTimeout(15_000)
  page = await context.newPage()
  page.on('console', (message) => {
    if (message.type() === 'error') {
      problems.push(`console error: ${message.text()}`)
    }
  })
  page.on('pageerror', (error) => problems.push(`page error: ${error.message}`))

  await page.goto('/login')
  await page.getByLabel('Password').fill(adminPassword)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('heading', { name: 'Home', level: 1 })).toBeVisible()

  // Add the corpus through the picker's handles and scan it.
  const roots: { roots: { handle: string; path: string }[] } = await (await page.request.get('/api/picker')).json()
  let handle: string | undefined
  for (const root of roots.roots) {
    const listing: { children: { handle: string; path: string }[] } = await (
      await page.request.get(`/api/picker?handle=${encodeURIComponent(root.handle)}`)
    ).json()
    handle = listing.children.find((c) => c.path === corpusPath())?.handle ?? handle
  }
  if (handle === undefined) {
    throw new Error('the picker does not list the corpus')
  }
  const added = await command<{ source: { id: string } }>('add-source', { handle, label: 'Layout corpus' })
  expect(added.status).toBe(201)
  sourceId = added.body.source.id
  expect((await command('start-scan', { source_id: sourceId })).status).toBe(202)
  await expect
    .poll(
      async () => {
        const body: { sources: { id: string; last_scan_at: string | null; active_job: unknown; root_entry_id: string | null }[] } =
          await (await page.request.get('/api/sources')).json()
        const source = body.sources.find((s) => s.id === sourceId)
        rootId = source?.root_entry_id ?? ''
        return source !== undefined && source.last_scan_at !== null && source.active_job === null
      },
      { timeout: 60_000 },
    )
    .toBe(true)
  // The duplicates screens need hashing and relations to have run.
  await awaitDuplicates(page.request, async () => {
    const started = await command<{ job_id: string }>('start-hash', { source_id: sourceId })
    expect(started.status).toBe(202)
    return started.body.job_id
  })
})

test.afterEach(async () => {
  const seen = problems
  problems = []
  expect(seen).toEqual([])
})

test.afterAll(async () => {
  if (sourceId !== '') {
    expect((await command('remove-source', { source_id: sourceId })).status).toBe(200)
  }
  await context.close()
})

for (const viewport of viewports) {
  const size = `${viewport.width}x${viewport.height}`

  test(`the Map fits at ${size}`, async () => {
    await page.setViewportSize(viewport)
    const folder = await entryAt(programs)
    const backup = await entryAt('Backup_PC_2004')
    const winamp = await entryAt(`${programs}/Winamp`)

    await openMap(rootId)
    await checkLayout(`map-root-${size}`)
    await openMap(rootId, backup.id)
    await checkLayout(`map-root-panel-${size}`)
    await openMap(folder.id)
    await checkLayout(`map-programs-${size}`)
    await openMap(folder.id, winamp.id)
    await checkLayout(`map-programs-panel-${size}`)

    // The table keeps its name, size, and category columns in view with the
    // panel open, beside them or over something else (spec: Map with the
    // detail panel at 1366×768).
    const panel = await page.getByRole('complementary').boundingBox()
    const headers = page.getByRole('table', { name: /^Contents of / }).getByRole('columnheader')
    for (const name of ['Name', 'Size', 'Type or category']) {
      const header = await headers.filter({ hasText: name }).boundingBox()
      expect(header, name).not.toBeNull()
      expect((header?.x ?? 0) + (header?.width ?? 0), name).toBeLessThanOrEqual(panel?.x ?? 0)
    }
  })

  test(`Search and the detail panel fit at ${size}`, async () => {
    await page.setViewportSize(viewport)
    await page.goto('/search?ext=jpg')
    const results = page.getByRole('region', { name: 'Results' })
    await expect(results.getByRole('status').first()).toHaveText(/results?$/)
    await expect(results.getByRole('row').nth(1)).toBeVisible()
    await checkLayout(`search-${size}`)

    const photo = await entryAt('Fotos/2004')
    await page.goto(`/search?ext=jpg&entry=${photo.id}`)
    await expect(page.getByRole('complementary', { name: '2004' })).toBeVisible()
    await expect(page.getByRole('complementary').getByRole('region', { name: 'Classification' })).toBeVisible()
    await checkLayout(`search-panel-folder-${size}`)

    const found: { items: Item[] } = await (await page.request.get('/api/search?name=foto.jpg')).json()
    const item = found.items.find((i) => i.name === 'foto.jpg')
    if (item === undefined) {
      throw new Error('foto.jpg not found')
    }
    await page.goto(`/search?ext=jpg&entry=${item.id}`)
    await expect(page.getByRole('complementary', { name: 'foto.jpg' })).toBeVisible()
    await expect(page.getByRole('complementary').getByRole('region', { name: 'Classification' })).toBeVisible()
    await checkLayout(`search-panel-file-${size}`)
  })

  test(`Opportunities, a review list, and Compare fit at ${size}`, async () => {
    await page.setViewportSize(viewport)
    await page.goto('/opportunities')
    await expect(page.getByRole('list', { name: 'Opportunity cards' }).getByRole('listitem')).toHaveCount(8)
    await checkLayout(`opportunities-${size}`)

    // A duplicates list with a group's copies and a relation's sides shown.
    await page.goto('/opportunities/duplicates')
    const rows = page.getByRole('list', { name: 'Rows of Duplicate folders and files' }).locator(':scope > li')
    for (const row of [rows.filter({ hasText: /copies of/ }).first(), rows.filter({ hasNotText: /copies of/ }).first()]) {
      await row.getByRole('button', { name: 'Show copies' }).click()
      await expect(row.getByRole('list', { name: 'Copies' })).toBeVisible()
    }
    await checkLayout(`review-duplicates-${size}`)
    await page.goto('/opportunities/programs')
    await expect(page.getByRole('list', { name: 'Rows of Installed programs and system copies' }).locator(':scope > li').first()).toBeVisible()
    await checkLayout(`review-programs-${size}`)

    const fotos = await entryAt('Fotos')
    const copy = await entryAt('Fotos - Copia')
    await page.goto(`/compare?left=${fotos.id}&right=${copy.id}&bucket=identical`)
    await expect(page.getByRole('list', { name: 'Files: Identical' }).locator(':scope > li').first()).toBeVisible()
    await checkLayout(`compare-${size}`)
  })
}

test('below 1,600 px the panel opens over the Map, takes the focus, and gives it back', async () => {
  await page.setViewportSize(viewports[0] ?? { width: 1366, height: 768 })
  await openMap(rootId)
  const table = page.getByRole('table', { name: /^Contents of / })
  const before = await table.boundingBox()
  const link = page.getByRole('link', { name: 'Details of Backup_PC_2004', exact: true })
  await link.focus()
  await page.keyboard.press('Enter')
  const panel = page.getByRole('complementary', { name: 'Backup_PC_2004' })
  await expect(panel).toBeFocused()
  // The table keeps its width: the panel lies over it.
  expect(await table.boundingBox()).toEqual(before)
  expect(await panel.evaluate((el) => getComputedStyle(el).position)).toBe('fixed')

  await page.keyboard.press('Escape')
  await expect(panel).toBeHidden()
  await expect(link).toBeFocused()

  // From 1,600 px the panel opens beside the Map, which narrows instead.
  await page.setViewportSize({ width: 1920, height: 1080 })
  await link.click()
  await expect(panel).toBeVisible()
  expect(await panel.evaluate((el) => getComputedStyle(el).position)).toBe('sticky')
  await panel.getByRole('button', { name: 'Close details' }).click()
  await expect(panel).toBeHidden()
})

// openMap opens the Map at folder, with the detail panel of entry, and waits
// for its table and treemap.
async function openMap(folder: string, entry?: string) {
  await page.goto(`/map/${folder}${entry === undefined ? '' : `?entry=${entry}`}`)
  const table = page.getByRole('table', { name: /^Contents of / })
  await expect(table.getByRole('row').nth(1)).toBeVisible()
  await expect(page.getByRole('img', { name: /^Treemap of / })).toBeVisible()
  if (entry !== undefined) {
    await expect(page.getByRole('complementary').getByRole('region', { name: 'Classification' })).toBeVisible()
  }
}

// entryAt finds the entry at path inside the corpus source, folder by folder.
async function entryAt(path: string): Promise<Item> {
  let current: Item = { id: rootId, name: '', kind: 'directory' }
  for (const name of path.split('/')) {
    const resp = await page.request.get(`/api/entries/${current.id}/children?sort=name&order=asc&limit=1000`)
    const body: { items: Item[] } = await resp.json()
    const next = body.items.find((i) => i.name === name)
    if (next === undefined) {
      throw new Error(`${path}: ${name} not found`)
    }
    current = next
  }
  return current
}

interface Box {
  left: number
  right: number
  top: number
  bottom: number
}

interface Finding {
  what: string
  detail: string
}

// checkLayout saves a screenshot of the window as name, then checks, in the
// page:
// - the page does not scroll sideways, and every card (table, filters,
//   detail panel, treemap, and each section and list item of the main area,
//   such as an opportunity card, a review row, a copy, or a Compare file)
//   lies inside the window;
// - every visible element inside a card ends inside that card, as far as
//   it is not clipped inside the card;
// - a table's header cells line up with its rows' cells;
// - no two visible controls or labels inside a card overlap.
async function checkLayout(name: string) {
  // Fonts and the virtualized rows settle before measuring.
  await page.evaluate(() => document.fonts.ready)
  await page.screenshot({ path: `${shots}${name}.png` })
  const findings: Finding[] = await page.evaluate(() => {
    const out: { what: string; detail: string }[] = []
    const slack = 0.5
    const describe = (el: Element) => {
      const text = (el.getAttribute('aria-label') ?? el.textContent ?? '').trim().slice(0, 40)
      return `${el.tagName.toLowerCase()}${el.getAttribute('role') === null ? '' : `[role=${el.getAttribute('role')}]`} "${text}"`
    }
    const box = (el: Element) => el.getBoundingClientRect()
    // shown is an element a reader sees: laid out, not hidden (folded inside
    // a closed <details> included), not a screen-reader-only text.
    const shown = (el: Element) => {
      const rect = box(el)
      if (rect.width <= 1 || rect.height <= 1) {
        return false
      }
      return el.checkVisibility({ visibilityProperty: true, contentVisibilityAuto: true }) && el.closest('.sr-only') === null
    }
    const viewportWidth = document.documentElement.clientWidth
    if (document.documentElement.scrollWidth > viewportWidth) {
      out.push({ what: 'page', detail: `scrolls sideways: ${document.documentElement.scrollWidth} > ${viewportWidth}` })
    }

    const cards = [
      ...document.querySelectorAll(
        '[role="table"], form[aria-label="Filters"], aside, [role="img"][aria-label^="Treemap"], main section, main li',
      ),
    ].filter(shown)
    for (const card of cards) {
      const outer = box(card)
      if (outer.left < -slack || outer.right > viewportWidth + slack) {
        out.push({ what: describe(card), detail: `outside the window: ${outer.left}–${outer.right} of ${viewportWidth}` })
      }
      // Rows outside the scrolled-to area are clipped vertically by design;
      // only their sideways extent counts. That extent is what a reader sees
      // of the element: its box cut by every element between it and the
      // card that clips sideways, such as a path cut from the left.
      for (const el of card.querySelectorAll('*')) {
        if (!shown(el)) {
          continue
        }
        let { left, right } = box(el)
        for (let clip = el.parentElement; clip !== null && clip !== card; clip = clip.parentElement) {
          if (getComputedStyle(clip).overflowX !== 'visible') {
            left = Math.max(left, box(clip).left)
            right = Math.min(right, box(clip).right)
          }
        }
        if (right > outer.right + slack || left < outer.left - slack) {
          out.push({ what: describe(el), detail: `past its card ${describe(card)}: ${left}–${right} of ${outer.left}–${outer.right}` })
        }
      }
      if (card.getAttribute('role') === 'table') {
        const [header, ...rows] = [...card.querySelectorAll('[role="row"]')]
        const first = rows.find(shown)
        if (header !== undefined && first !== undefined) {
          const heads = [...header.querySelectorAll('[role="columnheader"]')]
          const cells = [...first.querySelectorAll('[role="cell"]')]
          if (heads.length !== cells.length) {
            out.push({ what: describe(card), detail: `${heads.length} headers for ${cells.length} cells` })
          }
          heads.forEach((head, i) => {
            const cell = cells[i]
            if (cell !== undefined && Math.abs(box(head).left - box(cell).left) > 1) {
              out.push({ what: describe(head), detail: `not above its cells: ${box(head).left} vs ${box(cell).left}` })
            }
          })
        }
      }
      const controls = [
        ...card.querySelectorAll('input, select, textarea, button, a[href], label, summary, legend, [role="columnheader"]'),
      ].filter(shown)
      for (let i = 0; i < controls.length; i++) {
        for (let j = i + 1; j < controls.length; j++) {
          const a = controls[i]
          const b = controls[j]
          if (a === undefined || b === undefined || a.contains(b) || b.contains(a)) {
            continue
          }
          const ra: Box = box(a)
          const rb: Box = box(b)
          const overlapX = Math.min(ra.right, rb.right) - Math.max(ra.left, rb.left)
          const overlapY = Math.min(ra.bottom, rb.bottom) - Math.max(ra.top, rb.top)
          if (overlapX > 1 && overlapY > 1) {
            out.push({ what: `${describe(a)} and ${describe(b)}`, detail: `overlap by ${overlapX}×${overlapY}` })
          }
        }
      }
    }
    return out
  })
  expect(findings, name).toEqual([])
}

// command sends a command as the app does, with the session's CSRF token,
// and returns the response status and body.
async function command<T = unknown>(name: string, body: unknown): Promise<{ status: number; body: T }> {
  const session: { csrf_token: string } = await (await page.request.get('/api/session')).json()
  const resp = await page.request.post(`/api/commands/${name}`, {
    data: body,
    headers: {
      Origin: origin(),
      'X-CSRF-Token': session.csrf_token,
      'Idempotency-Key': `e2e-layout-${name}-${Date.now()}-${Math.random()}`,
    },
  })
  return { status: resp.status(), body: await resp.json() }
}
