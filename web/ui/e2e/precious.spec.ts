import { devices, expect, test, type BrowserContext, type Locator, type Page } from '@playwright/test'

import { en } from '../src/i18n/en'
import { formatBytes, formatCount, formatPercent } from '../src/lib/format'
import { awaitDuplicates, type Coverage } from './duplicates'
import {
  adminPassword,
  bulkFiles,
  bulkFolders,
  corpusPath,
  groundTruth,
  origin,
  type TruthEntry,
  type TruthMember,
  type TruthPath,
} from './env'

// The suite drives one browser session through the R1 and R2 acceptance
// flows, in order, against the server global-setup.ts started over the
// regression corpus. Every test also fails on a Content Security Policy
// violation, a console error, an uncaught page error, or a JavaScript dialog.
test.describe.configure({ mode: 'serial' })

const sourceLabel = 'Old disk'
const programs = 'Backup_PC_2004/C/Arquivos de programas'
const keptThumbs = 'Fotos/2006/Praia/Thumbs.db'
const tagName = 'Fotos de 2006'
const pendrive = 'Downloads/fotos_2005_do_pendrive'
const pendriveZip = `${pendrive}.zip`
// At the default window the Map's table sits beside the treemap, too
// narrow for its Decision column, which hides before Duplicated (r2b design
// D9); the tests that read Decision widen the window for it.
const defaultWindow = devices['Desktop Chrome'].viewport
const wideWindow = { width: 1920, height: 1080 }

const corpus = groundTruth()
const truth = corpus.entries
const bytes = (n: number) => formatBytes(n, 'en')
const count = (n: number) => formatCount(n, 'en')

type Decision = keyof typeof en.home.decision
type ListName = keyof typeof en.opportunities.list

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

test('R2.1: Home shows the hashing coverage, and every duplicate group is found', async () => {
  // Hashing started on its own after the scan. Home shows how much is
  // checked, and refreshes when hashing ends.
  const coverage = page.getByRole('region', { name: 'Checked for copies' })
  await expect(coverage).toContainText(/ of .+ checked \(\d+%\)/)
  await awaitDuplicates(page.request, startHash)
  const home: { coverage: Coverage } = await (await page.request.get('/api/home')).json()
  const candidate = bytes(home.coverage.candidate.bytes)
  expect(home.coverage.checked).toEqual(home.coverage.candidate)
  await expect(coverage.getByText(/ checked \(/)).toHaveText(`${candidate} of ${candidate} checked (100%)`)
  await expect(coverage).toContainText('Not checked yet: 0 files (0 B)')
  await expect(coverage).toContainText('Could not be read: 0 files (0 B)')

  // Every indexed copy of the ground truth's groups has another copy, and
  // nothing else has (members are not searched).
  const copies = corpus.duplicates.flatMap((d) => d.copies.map((c) => c.path)).filter((p) => !p.includes('!'))
  const found = await allPages<{ path: string }>('/api/search', { dup: 'copies' })
  expect(found.items.map((i) => i.path).toSorted()).toEqual(copies.toSorted())
  await search({ copies: true })
  const results = page.getByRole('region', { name: 'Results' })
  await expect(results.getByRole('status').first()).toHaveText(`${count(copies.length)} results`)
  const setup = corpus.duplicates.find((d) => d.copies.some((c) => c.path === 'Downloads/Setup.exe'))
  expect(setup?.copies.map((c) => c.path)).toEqual(['Downloads/Setup(1).exe', 'Downloads/Setup.exe'])
  await search({ name: 'Setup', copies: true })
  await expect(results.getByRole('status').first()).toHaveText('2 results')
  for (const name of ['Setup.exe', 'Setup(1).exe']) {
    await expect(results.getByRole('link', { name, exact: true })).toBeVisible()
  }
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

  // The table is wide enough for its Decision column only in a wide window.
  await page.setViewportSize(wideWindow)
  await page.getByRole('table', { name: /^Contents of / }).getByRole('link', { name: backup, exact: true }).click()
  const inside = page.getByRole('table', { name: `Contents of ${backup}` })
  const drive = inside.getByRole('row').filter({ has: page.getByRole('link', { name: 'C', exact: true }) })
  await expect(await cellUnder(inside, drive, 'Decision')).toHaveText('Discard ↑')
  await page.setViewportSize(defaultWindow)

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

test('R2.2: Compare of Fotos with Fotos - Copia, chosen from the detail panel', async () => {
  const relation = corpus.relations.find((r) => r.kind === 'overlap' && r.a.path === 'Fotos - Copia' && r.b.path === 'Fotos')
  if (relation === undefined) {
    throw new Error('the ground truth has no overlap of Fotos - Copia with Fotos')
  }
  const onlyLeft = relation.b_only.map((p) => p.path.slice('Fotos/'.length))
  const onlyRight = relation.a_only.map((p) => p.path.slice('Fotos - Copia/'.length))
  expect(onlyRight).toEqual(['2006/Praia/DSC_editada.JPG'])
  expect(onlyLeft).toHaveLength(3)

  await openFolder('')
  await page.getByRole('link', { name: 'Details of Fotos', exact: true }).click()
  const fotos = page.getByRole('complementary', { name: 'Fotos' })
  await fotos.getByRole('button', { name: 'Compare with…' }).click()
  await expect(fotos.getByRole('status')).toContainText('Chosen for Compare.')
  await fotos.getByRole('button', { name: 'Close details' }).click()
  await page.getByRole('link', { name: 'Details of Fotos - Copia', exact: true }).click()
  await page
    .getByRole('complementary', { name: 'Fotos - Copia' })
    .getByRole('link', { name: 'Compare with Fotos', exact: true })
    .click()
  // Fotos holds files Fotos - Copia lacks, so Compare opens on them.
  await expect(page).toHaveURL(/\/compare\?left=\d+&right=\d+&bucket=only_left$/)

  // Reloading shows the same comparison: its sides are in the address.
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
    }
    const sides = page.getByRole('region', { name: 'Folders compared' }).locator(':scope > div')
    await expect(sides.nth(0).getByText('Fotos', { exact: true })).toBeVisible()
    await expect(sides.nth(1).getByText('Fotos - Copia', { exact: true })).toBeVisible()
    expect(await compareGroup('Only on the left')).toEqual(onlyLeft)
    expect(await compareGroup('Only on the right')).toEqual(onlyRight)
    await expectGroupFigures('Not checked yet', 0, 0)
    await expectGroupFigures('Same name, different content', 0, 0)
  }
  // The rest is identical, each file at the same path on both sides: one
  // path, both sides, and no extra copy (r2b design D11).
  const identical = truth
    .filter((e) => e.size !== undefined && e.path.startsWith('Fotos/'))
    .map((e) => e.path.slice('Fotos/'.length))
    .filter((p) => !onlyLeft.includes(p))
  expect(await compareGroup('Identical')).toEqual(identical.toSorted())
  const pairs = page.getByRole('list', { name: 'Files: Identical' }).locator(':scope > li')
  await expect(pairs.locator(':scope > div > div')).toHaveCount(2 * identical.length)
  await expect(pairs.getByText(/^Extra copy, same as/)).toHaveCount(0)
})

test('R2.3: the pendrive zip is the same as its unpacked folder, in the panel and in Compare', async () => {
  expect(corpus.relations).toContainEqual(
    expect.objectContaining({
      kind: 'same',
      a: expect.objectContaining({ path: pendriveZip }),
      b: expect.objectContaining({ path: pendrive }),
    }),
  )
  const listing = archiveListing(pendriveZip)
  const files = listing.filter((m) => m.kind === 'file')

  await openFolder(parent(pendriveZip))
  await page.getByRole('link', { name: 'Details of fotos_2005_do_pendrive.zip', exact: true }).click()
  const panel = page.getByRole('complementary', { name: 'fotos_2005_do_pendrive.zip' })
  const archive = panel.getByRole('region', { name: 'Archive' })
  await expect(term(archive, 'Contents')).toHaveText('Read completely')
  await expect(term(archive, 'Items inside')).toHaveText(count(listing.length))
  const same = panel
    .getByRole('list', { name: 'Folders related to this one' })
    .getByRole('listitem')
    .filter({ hasText: `Same content as ${pendrive}` })
  await expect(same).toContainText('only here: 0 files (0 B) · only there: 0 files (0 B)')
  await same.getByRole('link', { name: 'Compare', exact: true }).click()

  const sides = page.getByRole('region', { name: 'Folders compared' }).locator(':scope > div')
  await expect(sides.nth(0).getByText(pendriveZip, { exact: true })).toBeVisible()
  await expect(sides.nth(1).getByText(pendrive, { exact: true })).toBeVisible()
  expect(await compareGroup('Identical')).toEqual(files.map((m) => m.path).toSorted())
  const pairs = page.getByRole('list', { name: 'Files: Identical' }).locator(':scope > li')
  await expect(pairs.locator(':scope > div > div')).toHaveCount(2 * files.length)
  await expect(pairs.getByText(/^Extra copy, same as/)).toHaveCount(0)
  await expectGroupFigures('Identical', files.length, files.reduce((sum, m) => sum + (m.size ?? 0), 0))
  for (const group of ['Only on the left', 'Only on the right', 'Same name, different content', 'Not checked yet']) {
    await expectGroupFigures(group, 0, 0)
  }
})

test('browsing inside the pendrive zip in the Map shows its members, decided with the archive', async () => {
  const listing = archiveListing(pendriveZip)
  const filesUnder = (folder: string) => listing.filter((m) => m.kind === 'file' && m.path.startsWith(`${folder}/`))
  await openFolder(pendriveZip)
  const table = page.getByRole('table', { name: 'Contents of fotos_2005_do_pendrive.zip' })
  const areas = page.getByRole('list', { name: 'Areas of the treemap' })
  const folders = listing.filter((m) => m.kind === 'directory' && !m.path.includes('/'))
  expect(folders.length).toBeGreaterThan(1)
  await expect(table.getByRole('row')).toHaveCount(folders.length + 1)
  for (const folder of folders) {
    const below = filesUnder(folder.path)
    const size = bytes(below.reduce((sum, m) => sum + (m.size ?? 0), 0))
    const row = table.getByRole('row').filter({ has: page.getByRole('link', { name: folder.path, exact: true }) })
    await expect(row.getByRole('cell').nth(1), folder.path).toHaveText(size)
    await expect(row.getByRole('cell').nth(2), folder.path).toHaveText(count(below.length))
    await expect(areas.getByRole('button', { name: `Open ${folder.path} (${size})`, exact: true })).toBeAttached()
  }

  const folder = folders[0]?.path ?? ''
  await page.setViewportSize(wideWindow)
  await openFolder(`${pendriveZip}/${folder}`)
  const inside = page.getByRole('table', { name: `Contents of ${folder}` })
  const photos = filesUnder(folder)
  await expect(inside.getByRole('row')).toHaveCount(photos.length + 1)
  for (const photo of photos) {
    const name = photo.path.slice(folder.length + 1)
    const row = inside.getByRole('row').filter({ has: page.getByRole('link', { name, exact: true }) })
    await expect(row.getByRole('cell').nth(1), name).toHaveText(bytes(photo.size ?? 0))
    // A member that follows an undecided archive leaves Decision blank.
    await expect(await cellUnder(inside, row, 'Decision'), name).toHaveText('')
    await expect(
      areas.getByRole('button', { name: `Details of ${name} (${bytes(photo.size ?? 0)})`, exact: true }),
    ).toBeAttached()
  }
  await page.setViewportSize(defaultWindow)

  const photo = photos[0]?.path ?? ''
  const name = photo.slice(folder.length + 1)
  const others = copiesOf(`${pendriveZip}!${photo}`).filter((p) => p !== `${pendriveZip}!${photo}`)
  await inside.getByRole('link', { name, exact: true }).click()
  const panel = page.getByRole('complementary', { name })
  await expect(panel.getByRole('navigation', { name: 'Location' })).toContainText(folder)
  await expect(panel).toContainText('Inside the archive fotos_2005_do_pendrive.zip')
  await expect(panel.getByRole('region', { name: 'Decision' })).toContainText('Decided with the archive: Undecided')
  await expect(panel.getByRole('group', { name: 'Set decision' })).toHaveCount(0)
  await expect(panel.getByRole('region', { name: 'Tags' })).toHaveCount(0)
  const copies = panel.getByRole('region', { name: 'Copies' })
  await expect(copies).toContainText(`${count(others.length)} other copies:`)
  const listed = copies.getByRole('list', { name: 'Other copies' }).getByRole('link')
  await expect(listed).toHaveCount(others.length)
  expect((await listed.allTextContents()).toSorted()).toEqual(others.toSorted())
})

test('R2.8: a photo inside the pendrive zip opens in the viewer', async () => {
  // The last photo: the browsing test previewed the first, which the
  // browser keeps in its cache.
  const photo = archiveListing(pendriveZip).findLast((m) => m.kind === 'file' && /\.jpe?g$/i.test(m.path))?.path ?? ''
  const name = photo.split('/').at(-1) ?? ''
  await openFolder(`${pendriveZip}/${parent(photo)}`)
  // The panel's preview may fetch the photo before the viewer does.
  const content = page.waitForResponse((r) => /^\/api\/entries\/m\d+\/content$/.test(new URL(r.url()).pathname))
  await page.getByRole('table', { name: /^Contents of / }).getByRole('link', { name, exact: true }).click()
  const response = await content
  expect([200, 206]).toContain(response.status())
  expect(response.headers()['content-type']).toBe('image/jpeg')
  expect(response.headers()['content-security-policy']).toMatch(/^sandbox/)
  await page.getByRole('complementary', { name }).getByRole('button', { name: 'Open' }).click()
  const viewer = page.getByRole('dialog', { name })
  await expect
    .poll(() => viewer.getByRole('img', { name }).evaluate((img: HTMLImageElement) => img.naturalWidth))
    .toBeGreaterThan(0)
  await closeViewer(viewer)
})

test('R2.7: a file with no other copy says so, with the checked share', async () => {
  // A file of Documentos whose content is in no duplicate group of the
  // ground truth has no other copy.
  const copied = new Set(corpus.duplicates.flatMap((d) => d.copies.map((c) => c.path)))
  const single = truth.find(
    (e) => parent(e.path) === 'Documentos' && e.sha256 !== undefined && (e.size ?? 0) > 0 && !copied.has(e.path),
  )
  if (single === undefined) {
    throw new Error('the ground truth has no file with no other copy in Documentos')
  }
  const name = single.path.slice('Documentos/'.length)
  const home: { coverage: Coverage } = await (await page.request.get('/api/home')).json()
  const share = formatPercent(home.coverage.checked.bytes / home.coverage.candidate.bytes, 'en')
  await openFolder('Documentos')
  await page.getByRole('table', { name: 'Contents of Documentos' }).getByRole('link', { name, exact: true }).click()
  const copies = page.getByRole('complementary', { name }).getByRole('region', { name: 'Copies' })
  await expect(copies.getByText(/^No other copy/)).toBeVisible()
  await expect(copies.getByText(`On all disks, ${share} of what could have a copy is checked.`)).toBeVisible()
})

test('R2.4: discarding one copy of curriculo.doc in the duplicates list leaves the other copies as they were', async () => {
  const tagged = 'Documentos/curriculo.doc'
  const discarded = 'Documentos/curriculo (1).doc'
  const group = copiesOf(tagged)
  expect(group).toContain(discarded)
  expect(group.every((p) => !p.includes('!'))).toBe(true)

  // A tag on one copy first.
  await openFolder(parent(tagged))
  await page.getByRole('table', { name: 'Contents of Documentos' }).getByRole('link', { name: 'curriculo.doc', exact: true }).click()
  const details = page.getByRole('complementary', { name: 'curriculo.doc' })
  await details.getByRole('textbox', { name: 'New tag' }).fill('documento')
  await details.getByRole('button', { name: 'Create and add' }).click()
  await expect(details.getByRole('list', { name: 'Own tags' })).toContainText('documento')
  const before = new Map<string, IntentState>()
  for (const path of group) {
    before.set(path, await intentOf(path))
  }

  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Opportunities' }).click()
  await page.getByRole('link', { name: en.opportunities.list.duplicates, exact: true }).click()
  const rows = page.getByRole('list', { name: `Rows of ${en.opportunities.list.duplicates}` })
  const row = rows.locator(':scope > li').filter({ hasText: `${count(group.length)} copies of curriculo.doc` })
  await row.getByRole('button', { name: 'Show copies' }).click()
  const copy = (path: string) =>
    row
      .getByRole('list', { name: 'Copies' })
      .locator(':scope > li')
      .filter({ has: page.getByRole('link', { name: path, exact: true }) })
  const discard = copy(discarded).getByRole('group', { name: `Decision for ${discarded}` }).getByRole('button', { name: 'Discard' })
  await discard.click()
  await expect(copy(discarded)).toContainText('Decision: Discard')
  await expect(discard).toHaveAttribute('aria-pressed', 'true')
  for (const path of group.filter((p) => p !== discarded)) {
    const decision = before.get(path)?.eff_decision ?? 'undecided'
    await expect(copy(path), path).toContainText(`Decision: ${en.home.decision[decision]}`)
  }

  // Through the API: only the discarded copy changed, and only its decision.
  for (const path of group) {
    const after = await intentOf(path)
    const was = before.get(path)
    expect(after, path).toEqual(path === discarded ? { ...was, decision: 'discard', eff_decision: 'discard' } : was)
  }
  expect(before.get(tagged)?.tags).toEqual(['documento'])
  // Through the panel: the tagged copy keeps its tag, and the discarded one
  // gained none.
  await copy(tagged).getByRole('link', { name: tagged, exact: true }).click()
  await expect(details.getByRole('list', { name: 'Own tags' }).getByRole('listitem')).toHaveText([/^documento/])
  await copy(discarded).getByRole('link', { name: discarded, exact: true }).click()
  const discardedPanel = page.getByRole('complementary', { name: 'curriculo (1).doc' })
  await expect(term(discardedPanel, 'Effective decision')).toContainText('Discard')
  await expect(discardedPanel.getByRole('list', { name: 'Own tags' })).toHaveCount(0)
})

test('R2.5: every card’s bytes equal the sum of its review list over all pages', async () => {
  const opportunities: {
    cards: {
      list: ListName
      bytes: number
      rows: number
      decided_bytes: number
      decided_rows: number
      basis: keyof typeof en.opportunities.basis
    }[]
  } = await (
    await page.request.get('/api/opportunities')
  ).json()
  expect(opportunities.cards.map((c) => c.list).toSorted()).toEqual(Object.keys(en.opportunities.list).toSorted())
  for (const card of opportunities.cards) {
    const rows = await allPages<{ id: string; bytes: number }>(`/api/opportunities/${card.list}`, { limit: '2' })
    expect(new Set(rows.items.map((r) => r.id)).size, card.list).toBe(rows.items.length)
    expect(rows.items.length, card.list).toBe(card.rows)
    expect(rows.items.reduce((sum, r) => sum + r.bytes, 0), card.list).toBe(card.bytes)
    // Its decided figures equal its decided list (r2b D13).
    const decided = await allPages<{ bytes: number }>(`/api/opportunities/${card.list}`, { limit: '2', decided: '1' })
    expect(decided.items.length, card.list).toBe(card.decided_rows)
    expect(decided.items.reduce((sum, r) => sum + r.bytes, 0), card.list).toBe(card.decided_bytes)
  }
  expect(opportunities.cards.find((c) => c.list === 'duplicates')?.bytes).toBeGreaterThan(0)

  for (const card of opportunities.cards) {
    const label = en.opportunities.list[card.list]
    // A card whose rows hold no bytes heads with its item count instead,
    // and one with no open row left says so.
    const countFirst = card.bytes === 0 && card.rows > 0
    const items = `${count(card.rows)} ${card.rows === 1 ? 'item' : 'items'}`
    const headline = card.rows === 0 ? 'Nothing left to review' : countFirst ? items : bytes(card.bytes)
    const rowsText = countFirst || card.rows === 0 ? null : `${items} to review`
    const decidedText =
      card.decided_rows === 0
        ? null
        : `${count(card.decided_rows)} decided${card.decided_bytes === 0 ? '' : ` (${bytes(card.decided_bytes)})`}`
    await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Opportunities' }).click()
    const tile = page
      .getByRole('list', { name: 'Opportunity cards' })
      .getByRole('listitem')
      .filter({ has: page.getByRole('link', { name: label, exact: true }) })
    await expect(tile, label).toContainText(headline)
    for (const text of [rowsText, decidedText]) {
      if (text !== null) {
        await expect(tile, label).toContainText(text)
      }
    }
    await tile.getByRole('link', { name: label, exact: true }).click()
    await expect(page.getByRole('heading', { name: label, level: 1 })).toBeVisible()
    const header = page.getByRole('main').getByText(en.opportunities.basis[card.basis]).locator('..')
    await expect(header, label).toContainText(headline)
    for (const text of [rowsText, decidedText]) {
      if (text !== null) {
        await expect(header, label).toContainText(text)
      }
    }
    const list = page.getByRole('list', { name: `Rows of ${label}` })
    if (card.rows === 0) {
      await expect(page.getByText('Nothing left to review in this list.')).toBeVisible()
      continue
    }
    const more = page.getByRole('button', { name: 'Load more' })
    while (await more.isVisible()) {
      await more.click()
    }
    await expect(list.locator(':scope > li'), label).toHaveCount(card.rows)
  }
})

test('review keys decide and move through the system junk list', async () => {
  const label = en.opportunities.list.system_junk
  const open = await allPages<{ entry: { path: string; name: string } }>('/api/opportunities/system_junk', {})
  const paths = open.items.map((r) => r.entry.path)
  expect(paths.length).toBeGreaterThanOrEqual(5)
  const [p0, p1, p2, p3] = paths as [string, string, string, string]

  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Opportunities' }).click()
  await page.getByRole('link', { name: label, exact: true }).click()
  const rows = page.getByRole('list', { name: `Rows of ${label}` })
  const row = (path: string) =>
    rows.locator(':scope > li').filter({ has: page.getByRole('link', { name: path, exact: true }) })
  await expect(rows.locator(':scope > li')).toHaveCount(paths.length)

  await page.keyboard.press('j')
  await expect(row(p0)).toHaveAttribute('aria-current', 'true')
  await expect(row(p0)).toBeFocused()
  await page.keyboard.press('ArrowDown')
  await expect(row(p1)).toHaveAttribute('aria-current', 'true')
  await page.keyboard.press('ArrowUp')
  await expect(row(p0)).toHaveAttribute('aria-current', 'true')

  // A decided row leaves the list, and the row that took its place is
  // selected, so the keys decide consecutive rows without moving.
  for (const [key, path, next] of [
    ['d', p0, p1],
    ['k', p1, p2],
    ['l', p2, p3],
  ] as const) {
    await page.keyboard.press(key)
    await expect(row(path)).toHaveCount(0)
    await expect(row(next)).toHaveAttribute('aria-current', 'true')
    await expect(row(next)).toBeFocused()
  }

  // Enter opens the selected row's details. Keys typed there are the
  // panel's: the row is not decided.
  await page.keyboard.press('Enter')
  const name = p3.split('/').at(-1) ?? ''
  const panel = page.getByRole('complementary', { name })
  await expect(panel).toBeVisible()
  const newTag = panel.getByRole('textbox', { name: 'New tag' })
  await newTag.click()
  await page.keyboard.type('kdl')
  await expect(newTag).toHaveValue('kdl')
  await page.keyboard.press('Escape')
  await expect(panel).toBeHidden()
  await expect(row(p3)).toHaveAttribute('aria-current', 'true')
  await page.keyboard.press('k')
  await expect(row(p3)).toHaveCount(0)

  for (const [path, decision] of [
    [p0, 'discard'],
    [p1, 'keep'],
    [p2, 'later'],
    [p3, 'keep'],
  ] as const) {
    expect((await intentOf(path)).decision, path).toBe(decision)
  }
  // The address holds the toggle, so the box turns on once the route does.
  const showDecided = page.getByRole('checkbox', { name: 'Show decided rows' })
  await showDecided.click()
  await expect(showDecided).toBeChecked()
  const decided = page.getByRole('list', { name: `Decided rows of ${label}` })
  for (const path of [p0, p1, p2, p3]) {
    await expect(decided.getByRole('link', { name: path, exact: true })).toBeVisible()
  }
})

test('r2b D11: Compare shows both paths of a pair and names the twin of an extra copy', async () => {
  // Documentos holds two copies of the curriculum, and the old My Documents
  // one; they share nothing else.
  const left = 'Documentos'
  const right = 'Backup_PC_2004/C/Documents and Settings/Joao/Meus documentos'
  const rightContents = new Set(filesBelow(right).map((e) => e.sha256))
  const shared = filesBelow(left)
    .filter((e) => rightContents.has(e.sha256))
    .map((e) => e.path)
  const copies = copiesOf(`${left}/curriculo.doc`)
  expect(shared.toSorted()).toEqual(copies.filter((p) => parent(p) === left).toSorted())
  const inLeft = shared.map((p) => p.slice(left.length + 1)).toSorted()
  const inRight = copies.filter((p) => parent(p) === right).map((p) => p.slice(right.length + 1))
  expect(inLeft).toEqual(['curriculo (1).doc', 'curriculo.doc'])
  expect(inRight).toEqual(['curriculo.doc'])
  // The copies pair in path order: the first left one with the right one,
  // at another path; the second left one is an extra copy of it.
  const [paired, extra] = inLeft
  const [twin] = inRight

  await page.goto(`/compare?left=${await entryId(left)}&right=${await entryId(right)}&bucket=identical`)
  expect(await compareGroup('Identical')).toEqual([paired, extra])
  const items = page.getByRole('list', { name: 'Files: Identical' }).locator(':scope > li')
  const pair = items.nth(0).locator(':scope > div > div')
  await expect(pair).toHaveCount(2)
  await expect(pair.nth(0)).toContainText('Left')
  await expect(pair.nth(0).getByText(paired ?? '', { exact: true })).toBeVisible()
  await expect(pair.nth(1)).toContainText('Right')
  await expect(pair.nth(1).getByText(twin ?? '', { exact: true })).toBeVisible()
  await expect(items.nth(0).getByText(/^Extra copy/)).toHaveCount(0)

  const lone = items.nth(1)
  await expect(lone.locator(':scope > div > div')).toHaveCount(1)
  await expect(lone.locator(':scope > div > div')).toContainText('Left')
  const note = lone.getByText(/^Extra copy, same as/)
  await expect(note).toHaveText(`Extra copy, same as ${twin} on the right`)
  // The twin's link opens the right side's copy.
  await note.getByRole('link', { name: twin, exact: true }).click()
  const panel = page.getByRole('complementary', { name: twin })
  await expect(panel.getByRole('navigation', { name: 'Location' })).toContainText('Meus documentos')
})

test('r2b D12: Similar folders lists the declared overlaps with their figures', async () => {
  const declared = corpus.relations.filter((r) => r.kind === 'overlap')
  expect(declared.length).toBeGreaterThan(0)
  const listed = await allPages<Overlap>('/api/relations', { kind: 'overlap' })
  const amount = (paths: TruthPath[]): Amount => ({
    files: paths.length,
    bytes: paths.reduce((sum, p) => sum + (truth.find((e) => e.path === p.path)?.size ?? 0), 0),
  })
  const found: Overlap[] = []
  for (const relation of declared) {
    const { a, b } = relation
    const item = listed.items.find(
      (i) => (i.a.path === a.path && i.other.path === b.path) || (i.a.path === b.path && i.other.path === a.path),
    )
    if (item === undefined) {
      throw new Error(`the similar folders lack ${a.path} and ${b.path}`)
    }
    // Side a is the ground truth's a: what is only in each side is the
    // ground truth's, and what a shares is the rest of a.
    const [here, there] =
      item.a.path === a.path ? [relation.a_only, relation.b_only] : [relation.b_only, relation.a_only]
    expect(item.only_here, a.path).toEqual(amount(here))
    expect(item.only_there, a.path).toEqual(amount(there))
    const aBytes = filesBelow(a.path).reduce((sum, e) => sum + (e.size ?? 0), 0)
    expect(item.matched_bytes, a.path).toBe(aBytes - amount(relation.a_only).bytes)
    found.push(item)
  }

  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Opportunities' }).click()
  await page.getByRole('link', { name: 'Similar folders', exact: true }).click()
  await expect(page).toHaveURL(/\/opportunities\/similar$/)
  await expect(page.getByRole('heading', { name: 'Similar folders', level: 1 })).toBeVisible()
  const rows = page.getByRole('list', { name: 'Similar folders' }).locator(':scope > li')
  const more = page.getByRole('button', { name: 'Load more' })
  await expect(rows.first()).toBeVisible()
  while (await more.isVisible()) {
    await more.click()
  }
  await expect(rows).toHaveCount(listed.items.length)
  const shown: Locator[] = []
  for (const item of found) {
    const row = rows
      .filter({ has: page.getByRole('link', { name: item.a.path, exact: true }) })
      .filter({ has: page.getByRole('link', { name: item.other.path, exact: true }) })
    await expect(row, item.a.path).toContainText(
      [
        `${bytes(item.matched_bytes)} in common`,
        `Only in ${item.a.path}: ${fileCount(item.only_here.files)} (${bytes(item.only_here.bytes)})`,
        `Only in ${item.other.path}: ${fileCount(item.only_there.files)} (${bytes(item.only_there.bytes)})`,
      ].join(' · '),
    )
    shown.push(row)
  }
  // Compare opens on the two sides.
  const [first] = found
  const [firstRow] = shown
  if (first === undefined || firstRow === undefined) {
    throw new Error('no declared overlap')
  }
  await firstRow.getByRole('link', { name: 'Compare', exact: true }).click()
  await expect(page).toHaveURL(new RegExp(`/compare\\?left=${first.a.id}&right=${first.other.id}`))
  const sides = page.getByRole('region', { name: 'Folders compared' }).locator(':scope > div')
  await expect(sides.nth(0).getByText(first.a.path, { exact: true })).toBeVisible()
  await expect(sides.nth(1).getByText(first.other.path, { exact: true })).toBeVisible()
})

test('r2b D7: a search without accents finds the accented name', async () => {
  const accented = 'Configurações locais'
  const query = 'configuracoes'
  // The names that read as the query once their accents are dropped.
  const matches = truth
    .filter((e) => (e.path.split('/').at(-1) ?? '').normalize('NFD').replace(/\p{M}/gu, '').toLowerCase().includes(query))
    .map((e) => e.path)
  expect(matches.filter((p) => p.endsWith(`/${accented}`)).length).toBeGreaterThan(0)

  const found = await allPages<{ path: string }>('/api/search', { name: query })
  expect(found.items.map((i) => i.path).toSorted()).toEqual(matches.toSorted())
  await search({ name: query })
  const results = page.getByRole('region', { name: 'Results' })
  await expect(results.getByRole('status').first()).toHaveText(
    `${count(matches.length)} ${matches.length === 1 ? 'result' : 'results'}`,
  )
  await expect(results.getByRole('link', { name: accented, exact: true }).first()).toBeVisible()
})

test('r2b 4.4: Home’s partial notice lists exactly the folders that could not be read', async () => {
  // The corpus's privado folder is mode 000, which root reads anyway.
  test.skip(process.getuid?.() === 0, 'running as root, which can read the mode-000 privado folder')
  const unreadable = truth.filter((e) => e.unreadable === true).map((e) => e.path)
  expect(unreadable.length).toBeGreaterThan(0)
  const source = await corpusSource()
  const found = await allPages<{ path: string }>('/api/search', { state: 'unreadable', source: source.id })
  expect(found.items.map((i) => i.path).toSorted()).toEqual(unreadable.toSorted())

  // Home showing one source links to what could not be read in it.
  await page.goto(`/?source=${source.id}`)
  await page
    .getByRole('status')
    .filter({ hasText: 'Some folders could not be read' })
    .getByRole('link', { name: 'See what could not be read' })
    .click()
  await expect(page).toHaveURL(new RegExp(`/search\\?state=unreadable&source=${source.id}$`))
  const results = page.getByRole('region', { name: 'Results' })
  await expect(results.getByRole('status').first()).toHaveText(
    `${count(unreadable.length)} ${unreadable.length === 1 ? 'result' : 'results'}`,
  )
  for (const path of unreadable) {
    const name = path.split('/').at(-1) ?? ''
    const row = results.getByRole('row').filter({ has: page.getByRole('link', { name, exact: true }) })
    await expect(row, path).toContainText('Could not be read')
  }
})

test('r2b M4: the Map keys walk the rows, open a folder or a file, and close the panel', async () => {
  await openFolder(programs)
  const table = page.getByRole('table', { name: 'Contents of Arquivos de programas' })
  const row = (index: number) => table.locator(`[role="row"][aria-rowindex="${index + 2}"]`)
  const names: string[] = []
  for (const index of [0, 1, 2]) {
    names.push((await row(index).getByRole('link').first().textContent()) ?? '')
  }
  const folderAddress = page.url()

  // The arrows select a row and open its details; the walk is one history
  // entry, which each further arrow replaces.
  await page.keyboard.press('ArrowDown')
  await expect(row(0)).toHaveAttribute('aria-selected', 'true')
  await expect(page.getByRole('complementary', { name: names[0] })).toBeVisible()
  await page.keyboard.press('ArrowDown')
  await page.keyboard.press('ArrowDown')
  await expect(row(2)).toHaveAttribute('aria-selected', 'true')
  await expect(row(2)).toBeFocused()
  await expect(page.getByRole('complementary', { name: names[2] })).toBeVisible()
  await page.goBack()
  await expect(page).toHaveURL(folderAddress)
  await expect(page.getByRole('complementary')).toHaveCount(0)

  // Enter opens a folder row.
  const third = truth.find((e) => e.path === `${programs}/${names[2]}`)
  expect(third?.kind).toBe('directory')
  for (let i = 0; i < 3; i++) {
    await page.keyboard.press('ArrowDown')
  }
  await expect(row(2)).toHaveAttribute('aria-selected', 'true')
  await page.keyboard.press('Enter')
  await expect(page.getByRole('table', { name: `Contents of ${names[2]}` })).toBeVisible()
  await expect(page.getByRole('complementary')).toHaveCount(0)

  // Enter opens a file in the viewer, and Escape closes the details.
  await openFolder('Midia')
  const midia = page.getByRole('table', { name: 'Contents of Midia' })
  await expect(midia.locator('[role="row"][aria-rowindex="2"]').getByRole('link').first()).toHaveText('foto.jpg')
  await page.keyboard.press('ArrowDown')
  const panel = page.getByRole('complementary', { name: 'foto.jpg' })
  await expect(panel).toBeVisible()
  await page.keyboard.press('Enter')
  const viewer = page.getByRole('dialog', { name: 'foto.jpg' })
  await expect
    .poll(() => viewer.getByRole('img', { name: 'foto.jpg' }).evaluate((img: HTMLImageElement) => img.naturalWidth))
    .toBe(320)
  await closeViewer(viewer)
  await page.keyboard.press('Escape')
  await expect(panel).toBeHidden()
  await expect(page).not.toHaveURL(/[?&]entry=/)
})

test('r2b D6: a daily rescan shows on the source card with its next scan, and turns off', async () => {
  // Twelve hours from now, so that it cannot come due during the run. The
  // browser's zone is UTC.
  const now = new Date()
  const hour = (now.getUTCHours() + 12) % 24
  const at = `${String(hour).padStart(2, '0')}:00`
  const next = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate(), hour))
  if (next <= now) {
    next.setUTCDate(next.getUTCDate() + 1)
  }
  const { id } = await corpusSource()
  const scheduleOf = async () => {
    const body: { sources: { id: string; schedule: unknown; next_scan_at: string | null }[] } = await (
      await page.request.get('/api/sources')
    ).json()
    const source = body.sources.find((s) => s.id === id)
    return { schedule: source?.schedule, next: source?.next_scan_at ?? null }
  }

  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Sources' }).click()
  const card = page.getByRole('article', { name: sourceLabel })
  await expect(term(card, 'Rescan')).toHaveText('Off')
  await card.getByRole('button', { name: 'Change schedule' }).click()
  let form = card.getByRole('form', { name: 'Rescan schedule' })
  await form.getByLabel('Rescan').selectOption({ label: 'Daily' })
  await form.getByLabel('Time').fill(at)
  await form.getByRole('button', { name: 'Save' }).click()
  await expect(form).toBeHidden()
  await expect(term(card, 'Rescan')).toHaveText(`Daily at ${at}`)
  const set = await scheduleOf()
  expect(set.schedule).toEqual({ every: 'day', at, zone: 'UTC' })
  expect(new Date(set.next ?? '').getTime()).toBe(next.getTime())
  const shown = await page.evaluate(
    (time) => new Intl.DateTimeFormat('en', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(time)),
    next.toISOString(),
  )
  await expect(term(card, 'Next scan')).toHaveText(shown)

  await card.getByRole('button', { name: 'Change schedule' }).click()
  form = card.getByRole('form', { name: 'Rescan schedule' })
  await form.getByLabel('Rescan').selectOption({ label: 'Off' })
  await form.getByRole('button', { name: 'Save' }).click()
  await expect(form).toBeHidden()
  await expect(term(card, 'Rescan')).toHaveText('Off')
  await expect(term(card, 'Next scan')).toHaveCount(0)
  expect(await scheduleOf()).toEqual({ schedule: null, next: null })
})

test('r2b D1–D5: a category and a group mark set in the panel change the folders above after the scan, and go back to the rules', async () => {
  const praia = parent(keptThumbs)
  const above = [parent(praia), parent(parent(praia))]
  const praiaId = await entryId(praia)
  const aboveIds: string[] = []
  for (const path of above) {
    aboveIds.push(await entryId(path))
  }
  const start = await detailOf(praiaId)
  expect(start.classification).toMatchObject({
    owner: { category: null, group: null },
    rules_category: 'personal_media',
    group: false,
  })
  // Praia's photos count as personal, its Thumbs.db as disposable.
  const own = byFamily(start.entry.composition)
  expect(own.disposable).toEqual({ bytes: truth.find((e) => e.path === keptThumbs)?.size, files: 1 })
  const before: Composition[] = []
  for (const id of aboveIds) {
    before.push(byFamily((await detailOf(id)).entry.composition))
  }
  const expectAbove = async (expected: (composition: Composition) => Composition) => {
    for (const [i, id] of aboveIds.entries()) {
      expect(byFamily((await detailOf(id)).entry.composition), above[i]).toEqual(expected(before[i] ?? {}))
    }
  }

  await openFolder(parent(praia))
  await page.getByRole('link', { name: 'Details of Praia', exact: true }).click()
  const panel = page.getByRole('complementary', { name: 'Praia' })
  const classification = panel.getByRole('region', { name: 'Classification' })
  const category = classification.getByRole('combobox', { name: 'Change category' })
  const group = classification.getByRole('group', { name: 'Review as one item' })
  const personal = en.entry.category.personal_media
  await expect(category.locator('option[value="rules"]')).toHaveText(`As the rules say (${personal})`)
  await expect(group.getByRole('button', { name: 'As the rules say (No)' })).toHaveAttribute('aria-pressed', 'true')

  // A cache folder is a group: the folders above count Praia whole under
  // the cache's family.
  await overrideAndScan(() => category.selectOption({ label: en.entry.category.cache }))
  await expect(classification.getByRole('status')).toHaveText(en.detail.override.afterScan)
  await expect(term(classification, 'Category')).toContainText(en.entry.category.cache)
  await expect(term(classification, 'Category')).toContainText('set by you')
  await expect(classification.getByText(`The rules say: ${personal}`)).toBeVisible()
  await expect(category.locator('option[value="rules"]')).toHaveText(`Back to the rules (${personal})`)
  const cached = await detailOf(praiaId)
  expect(cached.classification).toMatchObject({
    owner: { category: 'cache', group: null },
    rules_category: 'personal_media',
    group: true,
  })
  expect(cached.entry.family).toBe('disposable')
  await expectAbove((composition) => countedWhole(composition, own, 'disposable'))

  await overrideAndScan(() => category.selectOption('rules'))
  await expect(term(classification, 'Category')).not.toContainText('set by you')
  await expect(category.locator('option[value="rules"]')).toHaveText(`As the rules say (${personal})`)
  await expectAbove((composition) => composition)

  // Marked as one item, Praia counts whole as personal, its Thumbs.db too.
  await overrideAndScan(() => group.getByRole('button', { name: 'Yes', exact: true }).click())
  await expect(classification.getByText(en.detail.group)).toContainText('set by you')
  await expect(classification.getByText('The rules say: item by item')).toBeVisible()
  await expect(group.getByRole('button', { name: 'Yes', exact: true })).toHaveAttribute('aria-pressed', 'true')
  expect((await detailOf(praiaId)).classification).toMatchObject({ owner: { category: null, group: true }, group: true })
  await expectAbove((composition) => countedWhole(composition, own, 'personal'))
  // The folder above shows it.
  await panel.getByRole('navigation', { name: 'Location' }).getByRole('link', { name: '2006', exact: true }).click()
  const bars = page
    .getByRole('complementary', { name: '2006' })
    .getByRole('list', { name: 'Size by category' })
    .getByRole('listitem')
  const grouped = countedWhole(before[0] ?? {}, own, 'personal')
  await expect(bars).toHaveCount(Object.keys(grouped).length)
  for (const [family, label] of Object.entries(en.home.family)) {
    const amount = grouped[family]
    if (amount === undefined) {
      await expect(bars.filter({ hasText: label }), family).toHaveCount(0)
    } else {
      await expect(bars.filter({ hasText: label }), family).toContainText(`${bytes(amount.bytes)} · ${fileCount(amount.files)}`)
    }
  }

  await page.getByRole('link', { name: 'Details of Praia', exact: true }).click()
  await overrideAndScan(() => group.getByRole('button', { name: 'As the rules say (No)' }).click())
  await expect(classification.getByText('set by you')).toHaveCount(0)
  await expect(classification.getByText(en.detail.group)).toHaveCount(0)
  expect((await detailOf(praiaId)).classification).toMatchObject({ owner: { category: null, group: null }, group: false })
  await expectAbove((composition) => composition)
  // The scans asked for hashing and relations again; let them end.
  await awaitDuplicates(page.request, startHash)
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

// cellUnder returns row's cell in the column of table headed name, so a
// hidden column fails instead of reading another one.
async function cellUnder(table: Locator, row: Locator, name: string): Promise<Locator> {
  await expect(table.getByRole('columnheader', { name, exact: true })).toBeVisible()
  const index = (await table.getByRole('columnheader').allTextContents()).indexOf(name)
  expect(index, `the ${name} column`).toBeGreaterThanOrEqual(0)
  return row.getByRole('cell').nth(index)
}

function parent(path: string): string {
  const slash = path.lastIndexOf('/')
  return slash < 0 ? '' : path.slice(0, slash)
}

// filesBelow lists the ground-truth files below the folder at path.
function filesBelow(path: string): TruthEntry[] {
  return truth.filter((e) => e.size !== undefined && e.path.startsWith(`${path}/`))
}

// openFolder opens the Map at the folder at path ("" is the corpus source's
// top folder), following the folder links from the top. It starts from the
// top folder's address, rather than the Map link, so that it never acts on
// the folder path of the page it leaves.
async function openFolder(path: string) {
  await page.goto(`/map/${(await corpusSource()).root_entry_id}`)
  await expect(page.getByRole('navigation', { name: 'Folder path' }).getByRole('link')).toHaveCount(0)
  await expect(page.getByRole('table', { name: /^Contents of / })).toBeVisible()
  for (const name of path === '' ? [] : path.split('/')) {
    await page.getByRole('table', { name: /^Contents of / }).getByRole('link', { name, exact: true }).click()
    await expect(page.getByRole('table', { name: `Contents of ${name}` })).toBeVisible()
  }
}

// search runs a search from the Search screen's filters: by name, and with
// copies only the files that have another copy.
async function search({ name, copies }: { name?: string; copies?: boolean }) {
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Search' }).click()
  const filters = page.getByRole('form', { name: 'Filters' })
  await filters.getByRole('button', { name: 'Clear filters' }).click()
  if (name !== undefined) {
    await filters.getByRole('searchbox', { name: 'Name contains' }).fill(name)
  }
  if (copies === true) {
    await filters.locator('summary').filter({ hasText: /^Copies/ }).click()
    await filters.getByRole('checkbox', { name: 'Has another copy' }).check()
  }
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
// and returns the response status, error code, and body.
async function command<T = unknown>(name: string, body: unknown): Promise<{ status: number; code?: string; body: T }> {
  const session: { csrf_token: string } = await (await page.request.get('/api/session')).json()
  const resp = await page.request.post(`/api/commands/${name}`, {
    data: body,
    headers: {
      Origin: origin(),
      'X-CSRF-Token': session.csrf_token,
      'Idempotency-Key': `e2e-${name}-${Date.now()}-${Math.random()}`,
    },
  })
  const out: T & { error?: { code: string } } = await resp.json()
  return { status: resp.status(), code: out.error?.code, body: out }
}

// startHash starts a hashing job of the corpus source and returns its ID.
async function startHash(): Promise<string> {
  const started = await command<{ job_id: string }>('start-hash', { source_id: (await corpusSource()).id })
  expect(started.status).toBe(202)
  return started.body.job_id
}

// allPages reads every page of a cursor-paged list of the read API.
async function allPages<T>(path: string, params: Record<string, string>): Promise<{ items: T[]; pages: number }> {
  const items: T[] = []
  let pages = 0
  let cursor: string | null = null
  do {
    const query = new URLSearchParams(params)
    if (cursor !== null) {
      query.set('cursor', cursor)
    }
    const resp = await page.request.get(`${path}?${query}`)
    expect(resp.status(), `${path}?${query}`).toBe(200)
    const body: { items: T[]; next_cursor: string | null } = await resp.json()
    items.push(...body.items)
    cursor = body.next_cursor
    pages++
  } while (cursor !== null)
  return { items, pages }
}

// archiveListing returns the ground truth's members of the archive at path.
function archiveListing(path: string): TruthMember[] {
  const archive = corpus.members.find((a) => a.path === path)
  if (archive === undefined) {
    throw new Error(`the ground truth does not list ${path}`)
  }
  return archive.members
}

// copiesOf returns the copies of the ground truth's duplicate group that
// holds path (a member as "archive!member/path").
function copiesOf(path: string): string[] {
  const group = corpus.duplicates.find((d) => d.copies.some((c) => c.path === path))
  if (group === undefined) {
    throw new Error(`${path} is in no duplicate group of the ground truth`)
  }
  return group.copies.map((c) => c.path)
}

// IntentState is what the owner set on an entry, and its suggestion.
interface IntentState {
  decision: string | null
  eff_decision: Decision
  triage: string | null
  tags: string[]
}

// corpusSource reads the corpus source from the read API.
async function corpusSource(): Promise<{ id: string; root_entry_id: string }> {
  const sources: { sources: { id: string; label: string; root_entry_id: string | null }[] } = await (
    await page.request.get('/api/sources')
  ).json()
  const source = sources.sources.find((s) => s.label === sourceLabel)
  if (source === undefined || source.root_entry_id === null) {
    throw new Error(`${sourceLabel} is not a scanned source`)
  }
  return { id: source.id, root_entry_id: source.root_entry_id }
}

// entryId finds the entry at path in the corpus source, folder by folder,
// from the read API.
async function entryId(path: string): Promise<string> {
  let id = (await corpusSource()).root_entry_id
  for (const name of path.split('/')) {
    const children = await allPages<{ id: string; name: string }>(`/api/entries/${id}/children`, { sort: 'name' })
    id = children.items.find((c) => c.name === name)?.id ?? ''
    expect(id, `${path}: ${name}`).not.toBe('')
  }
  return id
}

// intentOf reads the decision, suggestion, and tags of the entry at path
// in the corpus source, from the read API.
async function intentOf(path: string): Promise<IntentState> {
  const id = await entryId(path)
  const detail: {
    entry: { decision: string | null; eff_decision: Decision; triage: string | null }
    intent: { tags: { name: string }[] }
  } = await (await page.request.get(`/api/entries/${id}`)).json()
  const { decision, eff_decision, triage } = detail.entry
  return { decision, eff_decision, triage, tags: detail.intent.tags.map((t) => t.name) }
}

// compareGroup opens a group of the Compare screen and returns the paths of
// its files.
async function compareGroup(label: string): Promise<string[]> {
  await page.getByRole('navigation', { name: 'Groups' }).getByRole('link', { name: new RegExp(`^${label}`) }).click()
  const list = page.getByRole('list', { name: `Files: ${label}` })
  await expect(list.locator(':scope > li').first()).toBeVisible()
  return list.locator(':scope > li > a').allTextContents()
}

// expectGroupFigures checks the files and bytes a Compare group shows.
async function expectGroupFigures(label: string, files: number, size: number) {
  await expect(
    page.getByRole('navigation', { name: 'Groups' }).getByRole('link', { name: new RegExp(`^${label}`) }),
  ).toHaveText(spaced(`${label} ${fileCount(files)} · ${bytes(size)}`))
}

// fileCount reads a number of files as the app does: "1 file", "2 files".
function fileCount(n: number): string {
  return `${count(n)} ${n === 1 ? 'file' : 'files'}`
}

// Amount is a number of files and their bytes.
interface Amount {
  files: number
  bytes: number
}

// Overlap is an item of GET /api/relations?kind=overlap: side a, the other
// side, and their figures.
interface Overlap {
  a: { id: string; path: string }
  other: { id: string; path: string }
  matched_bytes: number
  only_here: Amount
  only_there: Amount
}

// Composition is a folder's bytes and files by family.
type Composition = Record<string, Amount>

// ClassifiedEntry is what the override test reads of GET /api/entries/{id}.
interface ClassifiedEntry {
  entry: { family: string | null; composition: (Amount & { family: string })[] }
  classification: {
    owner: { category: string | null; group: boolean | null }
    rules_category: string | null
    group: boolean
  }
}

// detailOf reads the entry with ID id from the read API.
async function detailOf(id: string): Promise<ClassifiedEntry> {
  const resp = await page.request.get(`/api/entries/${id}`)
  expect(resp.status()).toBe(200)
  const detail: ClassifiedEntry = await resp.json()
  return detail
}

// byFamily keys a composition by family.
function byFamily(amounts: (Amount & { family: string })[]): Composition {
  return Object.fromEntries(amounts.map((a) => [a.family, { files: a.files, bytes: a.bytes }]))
}

// countedWhole returns the composition of a folder above group, once the
// group counts whole under family instead of by its own composition (a
// group outside the containers family, design D21).
function countedWhole(composition: Composition, group: Composition, family: string): Composition {
  const out: Composition = { ...composition }
  const whole: Amount = { files: 0, bytes: 0 }
  for (const [f, a] of Object.entries(group)) {
    const was = out[f] ?? { files: 0, bytes: 0 }
    out[f] = { files: was.files - a.files, bytes: was.bytes - a.bytes }
    whole.files += a.files
    whole.bytes += a.bytes
  }
  const was = out[family] ?? { files: 0, bytes: 0 }
  out[family] = { files: was.files + whole.files, bytes: was.bytes + whole.bytes }
  // A family holding nothing is left out.
  return Object.fromEntries(Object.entries(out).filter(([, a]) => a.files > 0 || a.bytes > 0))
}

// overrideAndScan does act, which sets or clears a category or a group mark
// in the detail panel, and waits for the scan its command started to end:
// only then do the folders above count the change (r2b design D3).
async function overrideAndScan(act: () => Promise<unknown>) {
  const sent = page.waitForResponse((r) => /^\/api\/commands\/set-(category|group)$/.test(new URL(r.url()).pathname))
  await act()
  const response = await sent
  expect(response.ok(), await response.text()).toBe(true)
  const result: { applied: number; scan: { job_id: string } | null } = await response.json()
  expect(result.applied).toBe(1)
  if (result.scan === null) {
    throw new Error('the override started no scan')
  }
  const job = result.scan.job_id
  await expect
    .poll(
      async () => {
        const status: { state: string } = await (await page.request.get(`/api/jobs/${job}`)).json()
        return status.state
      },
      { message: `scan ${job} ends`, timeout: 60_000 },
    )
    .toBe('succeeded')
}

// expectNoScriptRan checks that no fixture script set its flag in any frame
// of p.
async function expectNoScriptRan(p: Page) {
  for (const frame of p.frames()) {
    const ran = await frame.evaluate(() => '__precious_pwned' in window).catch(() => false)
    expect(ran, `a fixture script ran in ${frame.url()}`).toBe(false)
  }
}
