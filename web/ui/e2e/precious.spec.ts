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

  await page.getByRole('table', { name: /^Contents of / }).getByRole('link', { name: backup, exact: true }).click()
  const inside = page.getByRole('table', { name: `Contents of ${backup}` })
  const drive = inside.getByRole('row').filter({ has: page.getByRole('link', { name: 'C', exact: true }) })
  await expect(drive.getByRole('cell').last()).toHaveText('Discard ↑')

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
  await openFolder(`${pendriveZip}/${folder}`)
  const inside = page.getByRole('table', { name: `Contents of ${folder}` })
  const photos = filesUnder(folder)
  await expect(inside.getByRole('row')).toHaveCount(photos.length + 1)
  for (const photo of photos) {
    const name = photo.path.slice(folder.length + 1)
    const row = inside.getByRole('row').filter({ has: page.getByRole('link', { name, exact: true }) })
    await expect(row.getByRole('cell').nth(1), name).toHaveText(bytes(photo.size ?? 0))
    // A member that follows an undecided archive leaves Decision blank.
    await expect(row.getByRole('cell').last(), name).toHaveText('')
    await expect(
      areas.getByRole('button', { name: `Details of ${name} (${bytes(photo.size ?? 0)})`, exact: true }),
    ).toBeAttached()
  }

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
  const gem = corpus.gems.unique.find((g) => parent(g.path) === 'Documentos' && g.copies === 1)
  if (gem === undefined) {
    throw new Error('the ground truth has no unique file in Documentos')
  }
  const name = gem.path.slice('Documentos/'.length)
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
    cards: { list: ListName; bytes: number; rows: number; basis: keyof typeof en.opportunities.basis }[]
  } = await (
    await page.request.get('/api/opportunities')
  ).json()
  expect(opportunities.cards.map((c) => c.list).toSorted()).toEqual(Object.keys(en.opportunities.list).toSorted())
  for (const card of opportunities.cards) {
    const rows = await allPages<{ id: string; bytes: number }>(`/api/opportunities/${card.list}`, { limit: '2' })
    expect(new Set(rows.items.map((r) => r.id)).size, card.list).toBe(rows.items.length)
    expect(rows.items.length, card.list).toBe(card.rows)
    expect(rows.items.reduce((sum, r) => sum + r.bytes, 0), card.list).toBe(card.bytes)
  }
  expect(opportunities.cards.find((c) => c.list === 'duplicates')?.bytes).toBeGreaterThan(0)

  for (const card of opportunities.cards) {
    const label = en.opportunities.list[card.list]
    // A card whose rows hold no bytes heads with its item count instead.
    const countFirst = card.bytes === 0 && card.rows > 0
    const items = `${count(card.rows)} ${card.rows === 1 ? 'item' : 'items'}`
    const headline = countFirst ? items : bytes(card.bytes)
    const rowsText = countFirst ? null : `${items} to review`
    await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Opportunities' }).click()
    const tile = page
      .getByRole('list', { name: 'Opportunity cards' })
      .getByRole('listitem')
      .filter({ has: page.getByRole('link', { name: label, exact: true }) })
    await expect(tile, label).toContainText(headline)
    if (rowsText !== null) {
      await expect(tile, label).toContainText(rowsText)
    }
    await tile.getByRole('link', { name: label, exact: true }).click()
    await expect(page.getByRole('heading', { name: label, level: 1 })).toBeVisible()
    const header = page.getByRole('main').getByText(en.opportunities.basis[card.basis]).locator('..')
    await expect(header, label).toContainText(headline)
    if (rowsText !== null) {
      await expect(header, label).toContainText(rowsText)
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

test('R2.6: Gems lists the ground truth’s unique personal files, the rescued spreadsheet, and the edited photo', async () => {
  const unique = await allPages<{ entry: { path: string } }>('/api/gems', { section: 'unique', limit: '5' })
  expect(unique.items.map((g) => g.entry.path)).toEqual(corpus.gems.unique.map((g) => g.path))
  const rescue = await allPages<{ entry: { path: string } }>('/api/gems', { section: 'rescue', limit: '5' })
  expect(rescue.items.map((g) => g.entry.path)).toEqual(corpus.gems.rescue.map((g) => g.path))
  const onlyInCopy = await allPages<{ entry: { path: string } }>('/api/gems', { section: 'only_in_copy', limit: '5' })
  expect(onlyInCopy.items.map((g) => g.entry.path)).toEqual(
    expect.arrayContaining(corpus.gems.only_in_copy.map((g) => g.path)),
  )
  const spreadsheet = corpus.gems.rescue.find((g) => g.path.endsWith('/Meu orcamento casamento.xls'))
  const edited = corpus.gems.only_in_copy.find((g) => g.path.endsWith('/DSC_editada.JPG'))
  if (spreadsheet?.group === undefined || edited === undefined) {
    throw new Error('the ground truth lacks the rescued spreadsheet or the edited photo')
  }
  const home: { coverage: Coverage } = await (await page.request.get('/api/home')).json()
  const claim = `On all disks, ${formatPercent(home.coverage.checked.bytes / home.coverage.candidate.bytes, 'en')} of what could have a copy is checked.`

  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Gems' }).click()
  const sections = Object.values(en.gems.section).map((title) => page.getByRole('region', { name: title }))
  for (const section of sections) {
    await expect(section.getByText(claim)).toBeVisible()
  }
  const [first, second, third] = sections
  const firstList = first?.getByRole('list', { name: en.gems.section.unique }) ?? page.locator('none')
  const more = first?.getByRole('button', { name: 'Load more' }) ?? page.locator('none')
  await expect(firstList.locator(':scope > li').first()).toBeVisible()
  while (await more.isVisible()) {
    await more.click()
  }
  await expect(firstList.locator(':scope > li > div:first-child > a')).toHaveText(
    corpus.gems.unique.map((g) => g.path),
  )
  const rescued = second?.getByRole('listitem').filter({ has: page.getByRole('link', { name: spreadsheet.path, exact: true }) })
  await expect(rescued ?? page.locator('none')).toContainText(`Inside ${spreadsheet.group.path}`)
  await expect(third?.getByRole('link', { name: edited.path, exact: true }) ?? page.locator('none')).toBeVisible()
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

// openFolder opens the Map at the folder at path ("" is the corpus source's
// top folder), following the folder links from the top. It starts from the
// top folder's address, rather than the Map link, so that it never acts on
// the folder path of the page it leaves.
async function openFolder(path: string) {
  const sources: { sources: { label: string; root_entry_id: string }[] } = await (
    await page.request.get('/api/sources')
  ).json()
  const root = sources.sources.find((s) => s.label === sourceLabel)?.root_entry_id ?? ''
  expect(root, sourceLabel).not.toBe('')
  await page.goto(`/map/${root}`)
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
  const sources: { sources: { id: string; label: string }[] } = await (await page.request.get('/api/sources')).json()
  const source = sources.sources.find((s) => s.label === sourceLabel)
  const started = await command<{ job_id: string }>('start-hash', { source_id: source?.id })
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

// intentOf reads the decision, suggestion, and tags of the entry at path
// in the corpus source, from the read API.
async function intentOf(path: string): Promise<IntentState> {
  const sources: { sources: { label: string; root_entry_id: string }[] } = await (
    await page.request.get('/api/sources')
  ).json()
  let id = sources.sources.find((s) => s.label === sourceLabel)?.root_entry_id ?? ''
  for (const name of path.split('/')) {
    const children = await allPages<{ id: string; name: string }>(`/api/entries/${id}/children`, { sort: 'name' })
    id = children.items.find((c) => c.name === name)?.id ?? ''
    expect(id, `${path}: ${name}`).not.toBe('')
  }
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
  ).toHaveText(spaced(`${label} ${count(files)} ${files === 1 ? 'file' : 'files'} · ${bytes(size)}`))
}

// expectNoScriptRan checks that no fixture script set its flag in any frame
// of p.
async function expectNoScriptRan(p: Page) {
  for (const frame of p.frames()) {
    const ran = await frame.evaluate(() => '__precious_pwned' in window).catch(() => false)
    expect(ran, `a fixture script ran in ${frame.url()}`).toBe(false)
  }
}
