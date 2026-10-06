import { devices, expect, test, type BrowserContext, type Locator, type Page } from '@playwright/test'

import { formatBytes, formatCount } from '../src/lib/format'
import { adminPassword, bulkFiles, bulkFolders, corpusPath, groundTruth, origin, type TruthEntry } from './env'

// The suite drives one browser session through the R1 acceptance flows, in
// order, against the server global-setup.ts started over the regression
// corpus. Every test also fails on a Content Security Policy violation, a
// console error, an uncaught page error, or a JavaScript dialog.
test.describe.configure({ mode: 'serial' })

const sourceLabel = 'Old disk'
const programs = 'Backup_PC_2004/C/Arquivos de programas'
const keptThumbs = 'Fotos/2006/Praia/Thumbs.db'
const tagName = 'Fotos de 2006'

const truth = groundTruth()
const bytes = (n: number) => formatBytes(n, 'en')
const count = (n: number) => formatCount(n, 'en')

let context: BrowserContext
let page: Page
let problems: string[] = []

test.beforeAll(async ({ browser }) => {
  // Dates and sizes read the same on every machine.
  context = await browser.newContext({
    ...devices['Desktop Chrome'],
    baseURL: origin(),
    locale: 'en-US',
    timezoneId: 'UTC',
  })
  context.setDefaultTimeout(15_000)
  // Report CSP violations from every document the app loads, frames included.
  await context.addInitScript(() => {
    document.addEventListener('securitypolicyviolation', (event) => {
      console.error(`CSP violation: ${event.violatedDirective} blocked ${event.blockedURI}`)
    })
  })
  page = await context.newPage()
  page.on('console', (message) => {
    if (message.type() === 'error') {
      problems.push(`console error: ${message.text()} (${message.location().url})`)
    }
  })
  page.on('pageerror', (error) => problems.push(`page error: ${error.message}`))
  page.on('dialog', (dialog) => {
    problems.push(`dialog: ${dialog.message()}`)
    void dialog.dismiss()
  })
})

test.afterEach(async () => {
  const seen = problems
  problems = []
  expect(seen).toEqual([])
  await expectNoScriptRan(page)
})

test.afterAll(async () => {
  await context.close()
})

test('signs in with the administrator password', async () => {
  await page.goto('/')
  await expect(page).toHaveURL(/\/login$/)
  await expect(page.getByRole('heading', { name: 'Sign in to Precious' })).toBeVisible()
  await page.getByLabel('Password').fill(adminPassword)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('heading', { name: 'Home', level: 1 })).toBeVisible()
  await expect(page.getByRole('main')).toContainText('No sources yet.')
})

test('R1.18: adds a source only through the picker', async () => {
  // A raw path, or a handle the server did not issue, is refused.
  expect(await command('add-source', { path: '/etc' })).toMatchObject({ status: 400, code: 'invalid_request' })
  expect(await command('add-source', { handle: 'not-a-handle' })).toMatchObject({ status: 400, code: 'invalid_request' })

  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Sources' }).click()
  await page.getByRole('button', { name: 'Add source' }).click()
  const picker = page.getByRole('dialog', { name: 'Add a source' })
  await picker.getByRole('list', { name: 'Folders' }).getByRole('button', { name: /^disk / }).click()
  await picker.getByRole('list', { name: 'Folders' }).getByRole('button', { name: 'corpus', exact: true }).click()
  await expect(picker.getByRole('navigation', { name: 'Current folder' })).toContainText('corpus')
  await picker.getByRole('textbox', { name: 'Name' }).fill(sourceLabel)
  await picker.getByRole('button', { name: 'Add this folder' }).click()

  await expect(picker).toBeHidden()
  await expect(page.getByRole('status').filter({ hasText: `“${sourceLabel}” was added.` })).toBeVisible()
  const card = page.getByRole('article', { name: sourceLabel })
  await expect(card).toContainText('Online')
  // The location is the chosen folder itself, not its disk's mount point.
  await expect(term(card, 'Location')).toHaveText(corpusPath())
  await expect(term(card, 'Last scan')).toHaveText('Not scanned yet')
})

test('scans a source with live progress', async () => {
  await page.getByRole('button', { name: 'Add source' }).click()
  const picker = page.getByRole('dialog', { name: 'Add a source' })
  await picker.getByRole('list', { name: 'Folders' }).getByRole('button', { name: /^disk / }).click()
  await picker.getByRole('list', { name: 'Folders' }).getByRole('button', { name: 'bulk', exact: true }).click()
  await picker.getByRole('button', { name: 'Add this folder' }).click()
  await expect(picker).toBeHidden()

  const card = page.getByRole('article', { name: 'bulk' })
  const total = bulkFolders * bulkFiles
  await card.getByRole('button', { name: 'Scan now' }).click()
  const progress = card.getByRole('status', { name: 'Scan in progress' })
  await expect(progress).toContainText(/Scanning|Finishing/)
  // The figures grow while the scan runs: a file count between none and all
  // shows up before the scan ends.
  await expect
    .poll(
      async () => {
        const [files] = await term(progress, 'Files').allTextContents()
        const n = Number((files ?? '').replaceAll(',', ''))
        return n > 0 && n < total
      },
      { timeout: 30_000, intervals: [50] },
    )
    .toBe(true)
  await expect(progress).toBeHidden({ timeout: 60_000 })
  await expect(term(card, 'Files')).toHaveText(count(total))
  await expect(term(card, 'Folders')).toHaveText(count(bulkFolders + 40))

  // The bulk folder only served to watch a scan; the rest uses the corpus.
  await card.getByRole('button', { name: 'Remove' }).click()
  await page.getByRole('alertdialog', { name: 'Remove “bulk”?' }).getByRole('button', { name: 'Remove' }).click()
  await expect(card).toBeHidden()
})

test('scans the corpus and Home shows its ground-truth totals', async () => {
  const card = page.getByRole('article', { name: sourceLabel })
  await card.getByRole('button', { name: 'Scan now' }).click()
  await expect(term(card, 'Last scan')).not.toHaveText('Not scanned yet', { timeout: 30_000 })
  await expect(card.getByRole('status', { name: 'Scan in progress' })).toBeHidden()

  const files = truth.filter((e) => e.size !== undefined)
  const totalBytes = files.reduce((sum, e) => sum + (e.size ?? 0), 0)
  const folders = truth.filter((e) => e.kind === 'directory').length
  await expect(term(card, 'Size')).toHaveText(bytes(totalBytes))
  await expect(term(card, 'Files')).toHaveText(count(files.length))
  await expect(term(card, 'Folders')).toHaveText(count(folders))

  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Home' }).click()
  const totals = page.getByRole('region', { name: 'Totals' })
  await expect(term(totals, 'Size')).toHaveText(bytes(totalBytes))
  await expect(term(totals, 'Files')).toHaveText(count(files.length))
  await expect(term(totals, 'Folders')).toHaveText(count(folders))
  // privado cannot be read, so the figures are marked incomplete.
  await expect(page.getByRole('status').filter({ hasText: 'Some folders could not be read' })).toBeVisible()
})

test('R1.2: the Map shows the size and file count of every program folder', async () => {
  await openFolder(programs)
  const table = page.getByRole('table', { name: 'Contents of Arquivos de programas' })
  const areas = page.getByRole('list', { name: 'Areas of the treemap' })
  const children = truth.filter((e) => e.kind === 'directory' && parent(e.path) === programs)
  expect(children.length).toBeGreaterThan(5)
  for (const child of children) {
    const name = child.path.slice(programs.length + 1)
    const below = filesBelow(child.path)
    const size = bytes(below.reduce((sum, e) => sum + (e.size ?? 0), 0))
    const row = table.getByRole('row').filter({ has: page.getByRole('link', { name, exact: true }) })
    await expect(row.getByRole('cell').nth(1), name).toHaveText(size)
    await expect(row.getByRole('cell').nth(2), name).toHaveText(count(below.length))
    await expect(areas.getByRole('button', { name: `Open ${name} (${size})`, exact: true })).toBeVisible()
  }
})

test('R1.3: search finds the spreadsheet inside an installed program', async () => {
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Search' }).click()
  const filters = page.getByRole('form', { name: 'Filters' })
  await filters.getByRole('searchbox', { name: 'Name contains' }).fill('orcamento')
  await filters.getByRole('textbox', { name: 'Extensions' }).fill('xls')
  await filters.getByRole('button', { name: 'Search', exact: true }).click()
  const results = page.getByRole('region', { name: 'Results' })
  await expect(results.getByRole('status').first()).toHaveText('1 result')
  await results.getByRole('link', { name: 'Meu orcamento casamento.xls' }).click()
  const details = page.getByRole('complementary', { name: 'Meu orcamento casamento.xls' })
  await expect(details.getByRole('navigation', { name: 'Location' })).toContainText('Microsoft Office')
})

test('R1.7: discarding a folder decides its subtree and changes Home', async () => {
  const backup = 'Backup_PC_2004'
  const below = filesBelow(backup)
  const discarded = `Discard ${bytes(below.reduce((sum, e) => sum + (e.size ?? 0), 0))} · ${count(below.length)} files`
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Home' }).click()
  const decisions = page.getByRole('list', { name: 'Decisions' })
  await expect(decisions.getByRole('listitem').filter({ hasText: /^Discard/ })).toHaveText(
    spaced('Discard 0 B · 0 files'),
  )

  await openFolder('')
  await page.getByRole('link', { name: `Details of ${backup}`, exact: true }).click()
  const details = page.getByRole('complementary', { name: backup })
  await details.getByRole('group', { name: 'Set decision' }).getByRole('button', { name: 'Discard' }).click()
  await expect(details.getByRole('button', { name: 'Discard' })).toHaveAttribute('aria-pressed', 'true')
  await expect(term(details, 'Effective decision')).toContainText('Discard')

  await page.getByRole('table', { name: /^Contents of / }).getByRole('link', { name: backup, exact: true }).click()
  const inside = page.getByRole('table', { name: `Contents of ${backup}` })
  const drive = inside.getByRole('row').filter({ has: page.getByRole('link', { name: 'C', exact: true }) })
  await expect(drive.getByRole('cell').last()).toHaveText('Discard (inherited)')

  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Home' }).click()
  await expect(decisions.getByRole('listitem').filter({ hasText: /^Discard/ })).toHaveText(spaced(discarded))
})

test('R1.11: a bulk discard skips the kept item and lists it', async () => {
  await openFolder(parent(keptThumbs))
  // A file's name in the folder table opens its details.
  await page.getByRole('table', { name: 'Contents of Praia' }).getByRole('link', { name: 'Thumbs.db', exact: true }).click()
  const details = page.getByRole('complementary', { name: 'Thumbs.db' })
  await details.getByRole('group', { name: 'Set decision' }).getByRole('button', { name: 'Keep' }).click()
  await expect(details.getByRole('button', { name: 'Keep' })).toHaveAttribute('aria-pressed', 'true')

  const thumbs = truth.filter((e) => e.path.split('/').at(-1) === 'Thumbs.db')
  await search({ name: 'Thumbs.db' })
  const results = page.getByRole('region', { name: 'Results' })
  await expect(results.getByRole('status').first()).toHaveText(`${count(thumbs.length)} results`)
  await results.getByRole('button', { name: 'Select all results' }).click()
  const confirm = page.getByRole('alertdialog', { name: 'Select all results?' })
  await expect(confirm).toContainText(`${count(thumbs.length)} items`)
  const kept = truth.find((e) => e.path === keptThumbs)?.size ?? 0
  await expect(confirm).toContainText(`1 of them (${bytes(kept)}) is kept.`)
  await confirm.getByRole('button', { name: 'Select all', exact: true }).click()
  await expect(results).toContainText(`All ${count(thumbs.length)} results are selected`)

  await page.getByRole('group', { name: 'Set decision' }).getByRole('button', { name: 'Discard' }).click()
  const report = page.getByRole('region', { name: 'Result of the last change' })
  await expect(report).toContainText(`Decision set on ${count(thumbs.length - 1)} items.`)
  await expect(report.getByRole('list', { name: '1 kept item was skipped:' }).getByRole('listitem')).toHaveText([keptThumbs])
})

test('R1.12: a folder tag is inherited and found by search', async () => {
  const folder = 'Fotos/2006'
  await openFolder(parent(folder))
  await page.getByRole('link', { name: 'Details of 2006', exact: true }).click()
  const details = page.getByRole('complementary', { name: '2006' })
  await details.getByRole('textbox', { name: 'New tag' }).fill(tagName)
  await details.getByRole('button', { name: 'Create and add' }).click()
  await expect(details.getByRole('list', { name: 'Own tags' })).toContainText(tagName)

  await openFolder(folder)
  await page.getByRole('link', { name: 'Details of Praia', exact: true }).click()
  const praia = page.getByRole('complementary', { name: 'Praia' })
  await expect(praia.getByRole('list', { name: 'Inherited tags' }).getByRole('listitem')).toHaveText(
    `${tagName} from ${folder}`,
  )

  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Search' }).click()
  const filters = page.getByRole('form', { name: 'Filters' })
  await filters.getByRole('button', { name: 'Clear filters' }).click()
  // The tag choices are folded under their summary.
  await filters.locator('summary').filter({ hasText: /^Tags$/ }).click()
  await filters.getByRole('checkbox', { name: tagName }).check()
  await filters.getByRole('button', { name: 'Search', exact: true }).click()
  const tagged = truth.filter((e) => e.path === folder || e.path.startsWith(`${folder}/`))
  await expect(page.getByRole('region', { name: 'Results' }).getByRole('status').first()).toHaveText(
    `${count(tagged.length)} results`,
  )
})

test('R1.13: the viewer shows each kind of file', async () => {
  const image = await openViewer('foto.jpg', 'image/jpeg')
  await expect
    .poll(() => image.getByRole('img', { name: 'foto.jpg' }).evaluate((img: HTMLImageElement) => img.naturalWidth))
    .toBe(320)
  await closeViewer(image)

  const audio = await openViewer('musica.mp3', 'audio/mpeg')
  await expect(audio.locator('audio')).toHaveAttribute('aria-label', 'musica.mp3')
  // HAVE_METADATA: the browser read the file's duration.
  await expect.poll(() => audio.locator('audio').evaluate((m: HTMLMediaElement) => m.readyState)).toBeGreaterThanOrEqual(1)
  await closeViewer(audio)

  // The corpus video is H.264 and AAC. Playwright's Chromium has no H.264
  // decoder (Chrome, Edge, Firefox, and Safari have one): there the file
  // must still arrive intact, and the player must fail only on the codec.
  const video = await openViewer('video.mp4', 'video/mp4')
  const player = video.locator('video')
  await expect(player).toHaveAttribute('aria-label', 'video.mp4')
  const h264 = await player.evaluate((v: HTMLVideoElement) => v.canPlayType('video/mp4; codecs="avc1.42E01E"') !== '')
  if (h264) {
    await expect.poll(() => player.evaluate((v: HTMLVideoElement) => v.readyState)).toBeGreaterThanOrEqual(1)
  } else {
    await expect
      .poll(() => player.evaluate((v: HTMLVideoElement) => v.error?.code))
      .toBe(4 /* MEDIA_ERR_SRC_NOT_SUPPORTED, not 2, a network error */)
  }
  await closeViewer(video)

  const pdf = await openViewer('documento.pdf', 'application/pdf')
  await expect(pdf.locator('iframe[title="PDF document documento.pdf"]')).toBeVisible()
  await closeViewer(pdf)

  const markdown = await openViewer('notas.md')
  const article = markdown.getByRole('article', { name: 'Contents of notas.md' })
  await expect(article.getByRole('heading', { name: 'Notas da mudança' })).toBeVisible()
  await expect(article.locator('script, img, [onerror]')).toHaveCount(0)
  await closeViewer(markdown)

  const python = await openViewer('script.py')
  await expect(python.locator('pre[aria-label="Contents of script.py"]')).toContainText('def renomear(pasta):')
  await closeViewer(python)

  const letter = await openViewer('carta_1252.txt')
  await expect(letter.locator('pre[aria-label="Contents of carta_1252.txt"]')).toContainText(
    'A viagem para São Paulo foi “inesquecível”. Gastei só € 50 no café da manhã – que preço!',
  )
  await closeViewer(letter)
})

test('R1.13: HTML and SVG from the source never run script in the app', async () => {
  const html = await openViewer('pagina.html')
  // Shown as text: the markup is visible, not rendered.
  await expect(html.locator('pre[aria-label="Contents of pagina.html"]')).toContainText('<script>window.__precious_pwned')
  await closeViewer(html)

  const svg = await openViewer('desenho.svg', 'image/svg+xml')
  await expect
    .poll(() => svg.getByRole('img', { name: 'desenho.svg' }).evaluate((img: HTMLImageElement) => img.naturalWidth))
    .toBe(120)
  await closeViewer(svg)
  await expectNoScriptRan(page)

  // Opened directly, in a tab of the same session, the SVG is sandboxed and
  // the HTML page is only downloaded.
  const tab = await context.newPage()
  try {
    const svgResponse = await tab.goto(await contentPath('desenho.svg'))
    expect(svgResponse?.headers()['content-security-policy']).toMatch(/^sandbox/)
    await expectNoScriptRan(tab)
    const download = tab.waitForEvent('download')
    await tab.goto(await contentPath('pagina.html')).catch(() => {})
    expect((await download).suggestedFilename()).toBe('pagina.html')
    await expectNoScriptRan(tab)
  } finally {
    await tab.close()
  }
})

test('D22: the detail panel shows a photo without Open', async () => {
  await openFolder('Midia')
  await page.getByRole('table', { name: 'Contents of Midia' }).getByRole('link', { name: 'foto.jpg', exact: true }).click()
  const preview = page.getByRole('complementary', { name: 'foto.jpg' }).getByRole('region', { name: 'Preview' })
  const image = preview.getByAltText('foto.jpg')
  await expect.poll(() => image.evaluate((img: HTMLImageElement) => img.naturalWidth)).toBe(320)
  await expect(image).toBeVisible()
  await expect(page.getByRole('dialog')).toHaveCount(0)
})

// spaced matches text, ignoring the whitespace between its words, so a list
// item whose parts are separate elements matches what it reads as.
function spaced(text: string): RegExp {
  const words = text.split(/\s+/).map((word) => word.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'))
  return new RegExp(`^\\s*${words.join('\\s*')}\\s*$`)
}

// term returns the definition of the description-list term named name
// inside scope.
function term(scope: Locator, name: string): Locator {
  return scope.locator(`xpath=.//dt[normalize-space()="${name}"]/following-sibling::dd[1]`)
}

function parent(path: string): string {
  const slash = path.lastIndexOf('/')
  return slash < 0 ? '' : path.slice(0, slash)
}

// filesBelow lists the ground-truth files below the folder at path.
function filesBelow(path: string): TruthEntry[] {
  return truth.filter((e) => e.size !== undefined && e.path.startsWith(`${path}/`))
}

// openFolder opens the Map at the folder at path ("" is the source's top
// folder), following the folder links from the top.
async function openFolder(path: string) {
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Map' }).click()
  await expect(page.getByRole('table', { name: /^Contents of / })).toBeVisible()
  const trail = page.getByRole('navigation', { name: 'Folder path' })
  const top = trail.getByRole('link').first()
  if ((await top.count()) > 0) {
    await top.click()
  }
  for (const name of path === '' ? [] : path.split('/')) {
    await page.getByRole('table', { name: /^Contents of / }).getByRole('link', { name, exact: true }).click()
    await expect(page.getByRole('table', { name: `Contents of ${name}` })).toBeVisible()
  }
}

// search runs a search from the Search screen's filters.
async function search({ name }: { name: string }) {
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Search' }).click()
  const filters = page.getByRole('form', { name: 'Filters' })
  await filters.getByRole('button', { name: 'Clear filters' }).click()
  await filters.getByRole('searchbox', { name: 'Name contains' }).fill(name)
  await filters.getByRole('button', { name: 'Search', exact: true }).click()
}

// openViewer finds the file by name, opens it from its details, and returns
// the viewer dialog. With contentType, it also waits for the file's first
// content response (whole, or a range for media and for the panel's check
// of a PDF), which must have that type: the panel's preview may make it
// before the viewer does.
async function openViewer(name: string, contentType?: string): Promise<Locator> {
  await search({ name })
  const results = page.getByRole('region', { name: 'Results' })
  await expect(results.getByRole('status').first()).toHaveText('1 result')
  const content =
    contentType === undefined
      ? undefined
      : page.waitForResponse((r) => /\/api\/entries\/\d+\/content$/.test(new URL(r.url()).pathname))
  await results.getByRole('link', { name, exact: true }).click()
  const details = page.getByRole('complementary', { name })
  await details.getByRole('button', { name: 'Open' }).click()
  const dialog = page.getByRole('dialog', { name })
  await expect(dialog).toBeVisible()
  if (content !== undefined) {
    const response = await content
    expect([200, 206]).toContain(response.status())
    expect(response.headers()['content-type']).toBe(contentType)
  }
  return dialog
}

async function closeViewer(dialog: Locator) {
  await dialog.getByRole('button', { name: 'Close', exact: true }).click()
  await expect(dialog).toBeHidden()
}

// contentPath returns the content URL of the file named name, from the
// search API.
async function contentPath(name: string): Promise<string> {
  const resp = await page.request.get(`/api/search?name=${encodeURIComponent(name)}`)
  const body: { items: { id: string; name: string }[] } = await resp.json()
  const item = body.items.find((i) => i.name === name)
  if (item === undefined) {
    throw new Error(`${name} not found`)
  }
  return `/api/entries/${item.id}/content`
}

// command sends a command as the app does, with the session's CSRF token,
// and returns the response status and error code.
async function command(name: string, body: unknown): Promise<{ status: number; code?: string }> {
  const session: { csrf_token: string } = await (await page.request.get('/api/session')).json()
  const resp = await page.request.post(`/api/commands/${name}`, {
    data: body,
    headers: {
      Origin: origin(),
      'X-CSRF-Token': session.csrf_token,
      'Idempotency-Key': `e2e-${name}-${Date.now()}-${Math.random()}`,
    },
  })
  const out: { error?: { code: string } } = await resp.json()
  return { status: resp.status(), code: out.error?.code }
}

// expectNoScriptRan checks that no fixture script set its flag in any frame
// of p.
async function expectNoScriptRan(p: Page) {
  for (const frame of p.frames()) {
    const ran = await frame.evaluate(() => '__precious_pwned' in window).catch(() => false)
    expect(ran, `a fixture script ran in ${frame.url()}`).toBe(false)
  }
}
