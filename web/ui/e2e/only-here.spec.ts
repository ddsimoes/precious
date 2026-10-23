import { devices, expect, test, type BrowserContext, type Locator, type Page } from '@playwright/test'

import { en } from '../src/i18n/en'
import { formatBytes, formatCount } from '../src/lib/format'
import { awaitDuplicates } from './duplicates'
import { adminPassword, copiesFixture, copiesPath, corpusPath, groundTruth, origin } from './env'

// The Only here mode of the Map (r5c design D7–D9, D13), in a browser,
// against the server the rest of the suite leaves behind: the corpus as it
// is after precious.spec.ts, and the copies folder global-setup.ts wrote
// (copiesFixture), added here as a second source and removed at the end.
//
// The copies are taken only from corpus files and folders that no test in
// precious.spec.ts moves, renames, quarantines, purges, or redates. That
// suite changes these, and nothing else:
// - renamed and back: Documentos/curriculo.doc (R3.1);
// - moved: Fotos - Copia/2006/Praia/DSC_editada.JPG into Fotos (R3.6), and
//   Fotos/2006/Praia/Thumbs.db into Documentos (the rescue);
// - moved and back: Downloads/Setup.exe and Downloads/Setup(1).exe (R3.4);
// - quarantined, then purged but for one: every discarded file, that is
//   every other Thumbs.db (R1.11), Documentos/curriculo (1).doc (R2.4,
//   restored by R4.3), Documentos/curriculo_final.doc, and the first row of
//   the system junk list (R4.1, R4.6);
// - redated: the Sony's photos in Viagens/2010-07 Bahia and Viagens/2010-12
//   Natal (R5.2), and Viagens/2008-03 Ouro Preto (R5.4, undone);
// - moved into new folders Fotos/{year}/{month}: all of Viagens and
//   celular_2011 (R5.5).
// Backup_PC_2004 is discarded but stays, blocked by a kept file (R4.1).
//
// Left as they are, and used here:
// - celular_backup_2009, whole: DCIM/100MEDIA is copied as a folder, and
//   WhatsApp/Media/WhatsApp Images/IMG-20090612-WA0001.jpg as a photo;
// - Fotos/2009/Viagem Chile/DSC04005.JPG (copied twice) and
//   Fotos/2007/Formatura/DSC03004.JPG (no other copy in the corpus);
// - Fotos/2004/Natal/DSC00104.JPG, whose other copies are in Fotos - Copia
//   and Backup_PC_2004;
// - Midia/videos.zip and Midia/video.mp4;
// - Projetos/site_antigo and Projetos/site_antigo_2006.tar.gz.
test.describe.configure({ mode: 'serial' })

const copiesLabel = 'Copies'
const corpus = groundTruth()
const bytes = (n: number) => formatBytes(n, 'en')
const sizeOf = (path: string) => {
  const size = corpus.entries.find((e) => e.path === path)?.size
  if (size === undefined) {
    throw new Error(`the ground truth has no file ${path}`)
  }
  return size
}
const { folder, photos, again, own } = copiesFixture
const ownBytes = new TextEncoder().encode(own.text).length
const folderFiles = corpus.entries.filter((e) => e.size !== undefined && e.path.startsWith(`${folder.from}/`))
const folderBytes = folderFiles.reduce((sum, e) => sum + (e.size ?? 0), 0)

interface Source {
  id: string
  label: string
  root: string
}

let context: BrowserContext
let page: Page
let problems: string[] = []
let copies: Source
let corpusSource: Source

test.beforeAll(async ({ browser }) => {
  // Dates and sizes read the same on every machine.
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
      problems.push(`console error: ${message.text()} (${message.location().url})`)
    }
  })
  page.on('pageerror', (error) => problems.push(`page error: ${error.message}`))

  await page.goto('/login')
  await page.getByLabel('Password').fill(adminPassword)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('heading', { name: 'Home', level: 1 })).toBeVisible()

  // Add the copies folder through the picker's handles and scan it.
  const roots: { roots: { handle: string }[] } = await (await page.request.get('/api/picker')).json()
  let handle: string | undefined
  for (const root of roots.roots) {
    const listing: { children: { handle: string; path: string }[] } = await (
      await page.request.get(`/api/picker?handle=${encodeURIComponent(root.handle)}`)
    ).json()
    handle = listing.children.find((c) => c.path === copiesPath())?.handle ?? handle
  }
  if (handle === undefined) {
    throw new Error('the picker does not list the copies folder')
  }
  const added = await command<{ source: { id: string } }>('add-source', { handle, label: copiesLabel })
  expect(added.status).toBe(201)
  const id = added.body.source.id
  expect((await command('start-scan', { source_id: id })).status).toBe(202)
  await expect
    .poll(async () => (await sourceAt(copiesPath()))?.scanned, { message: 'the copies are scanned', timeout: 60_000 })
    .toBe(true)
  // Hashing follows the scan, and the relate pass places what it found.
  await awaitDuplicates(page.request, async () => {
    const started = await command<{ job_id: string }>('start-hash', { source_id: id })
    expect(started.status).toBe(202)
    return started.body.job_id
  })
  copies = await scannedSource(copiesPath())
  corpusSource = await scannedSource(corpusPath())
  for (const root of [copies.root, corpusSource.root]) {
    await expect
      .poll(
        async () => {
          const detail: { places_updating?: boolean; entry: { place?: string | null } } = await (
            await page.request.get(`/api/entries/${root}?places=1`)
          ).json()
          return detail.places_updating === false && detail.entry.place !== 'not_computed'
        },
        { message: 'the places are computed', timeout: 60_000 },
      )
      .toBe(true)
  }
})

test.afterEach(async () => {
  const seen = problems
  problems = []
  expect(seen).toEqual([])
})

test.afterAll(async () => {
  if (copies !== undefined) {
    expect((await command('remove-source', { source_id: copies.id })).status).toBe(200)
  }
  await context.close()
})

test('emphasis follows the place: a folder copied elsewhere is dimmed, the folder with an only copy stands out', async () => {
  await openMap(copies.root)
  const table = page.getByRole('table', { name: `Contents of ${copiesLabel}` })
  await expect(table.getByRole('columnheader', { name: 'Has copies' })).toBeVisible()
  await turnOn(page.getByRole('switch', { name: en.map.places.toggle }))
  await expect(page).toHaveURL(/[?&]places=1(&|$)/)
  await expect(table.getByRole('columnheader', { name: 'Has copies' })).toHaveCount(0)

  const copied = rowOf(table, folder.to)
  await expect(await placeCell(table, copied)).toHaveText(en.map.places.folder.other_source)
  await expect(copied).toHaveAttribute('data-emphasis', 'muted')
  const mine = rowOf(table, own.folder)
  await expect(await placeCell(table, mine)).toHaveText(fill(en.map.places.folder.only_copy, { bytes: bytes(ownBytes) }))
  await expect(mine).toHaveAttribute('data-emphasis', 'strong')
  // The open folder's totals say the same in bytes.
  const copiedBytes = folderBytes + photos.from.reduce((sum, path) => sum + sizeOf(path), 0) + sizeOf(again.from)
  const totals = page.getByRole('group', { name: en.map.places.totals })
  await expect(totals).toContainText(fill(en.map.places.amount.only_copy, { bytes: bytes(ownBytes) }))
  await expect(totals).toContainText(fill(en.map.places.amount.other_source, { bytes: bytes(copiedBytes) }))
})

test('the mode stays on through drilling and a reload', async () => {
  await openMap(copies.root)
  await turnOn(page.getByRole('switch', { name: en.map.places.toggle }))
  await page.getByRole('table', { name: `Contents of ${copiesLabel}` }).getByRole('link', { name: photos.to, exact: true }).click()
  const table = page.getByRole('table', { name: `Contents of ${photos.to}` })
  await expect(table).toBeVisible()
  for (const reload of [false, true]) {
    if (reload) {
      await page.reload()
    }
    await expect(page).toHaveURL(/[?&]places=1(&|$)/)
    await expect(page.getByRole('switch', { name: en.map.places.toggle })).toBeChecked()
    // Each photo has its original on the corpus; the one copied twice names
    // it, the copy on another source first, and counts the other.
    for (const path of photos.from) {
      const row = rowOf(table, basename(path))
      await expect(await placeCell(table, row), path).toHaveText(en.map.places.file.other_source)
      const named = fill(en.map.places.copy.atOn, { path, source: corpusSource.label })
      await expect(row.getByRole('link', { name: named, exact: true }), path).toBeVisible()
      if (path === again.from) {
        await expect(row, path).toContainText(fill(en.map.places.copy.more, { formatted: '1' }))
      } else {
        await expect(row, path).not.toContainText(/\+\d+ more/)
      }
    }
  }
})

test('inside an archive, members show their places and the totals their unpacked sizes', async () => {
  const zip = 'Midia/videos.zip'
  const video = 'Midia/video.mp4'
  const members = corpus.members.find((a) => a.path === zip)?.members ?? []
  const memberSize = (path: string) => members.find((m) => m.path === path)?.size ?? Number.NaN
  const readme = memberSize('ferias/leia-me.txt')
  const film = memberSize('ferias/video.mp4')
  // The film inside is the loose Midia/video.mp4; the note is in no other
  // place.
  expect(corpus.duplicates.find((d) => d.copies.some((c) => c.path === video))?.copies.map((c) => c.path)).toEqual([
    video,
    `${zip}!ferias/video.mp4`,
  ])
  expect(corpus.duplicates.some((d) => d.copies.some((c) => c.path === `${zip}!ferias/leia-me.txt`))).toBe(false)

  await openMap(await entryAt(corpusSource, 'Midia'), 'places=1&color=places')
  await page.getByRole('table', { name: 'Contents of Midia' }).getByRole('link', { name: 'videos.zip', exact: true }).click()
  const table = page.getByRole('table', { name: 'Contents of videos.zip' })
  await expect(table).toBeVisible()
  const unpacked = [
    fill(en.map.places.amount.only_copy, { bytes: bytes(readme) }),
    fill(en.map.places.amount.this_source, { bytes: bytes(film) }),
  ]
  const totals = page.getByRole('group', { name: en.map.places.totalsArchive })
  for (const amount of unpacked) {
    await expect(totals).toContainText(amount)
  }
  // The member folder shows its bytes by place, in words and as a bar.
  const ferias = rowOf(table, 'ferias')
  const cell = await placeCell(table, ferias)
  await expect(cell).toContainText(fill(en.map.places.folder.only_copy, { bytes: bytes(readme) }))
  // Accessible names read a no-break space as a space.
  const bar = new RegExp(
    unpacked.map((amount) => amount.replace(/[.*+?^${}()|[\]\\]/g, '\\$&').replace(/\s/g, '\\s')).join('.*'),
  )
  await expect(cell.getByRole('img', { name: bar })).toBeVisible()

  await table.getByRole('link', { name: 'ferias', exact: true }).click()
  const inside = page.getByRole('table', { name: 'Contents of ferias' })
  const filmRow = rowOf(inside, 'video.mp4')
  await expect(await placeCell(inside, filmRow)).toHaveText(fill(en.map.places.file.this_source, { formatted: '2' }))
  await expect(filmRow.getByRole('link', { name: fill(en.map.places.copy.at, { path: video }), exact: true })).toBeVisible()
})

test('hiding what is safe elsewhere lists only the folder with an only copy, and drilling keeps it hidden', async () => {
  await openMap(copies.root, 'places=1&color=places')
  const table = page.getByRole('table', { name: `Contents of ${copiesLabel}` })
  await expect(table.getByRole('row')).toHaveCount(5)
  await turnOn(page.getByRole('checkbox', { name: en.map.places.hide }))
  await expect(page).toHaveURL(/[?&]hide=other_source(&|$)/)
  await expect(table.getByRole('row')).toHaveCount(2)
  await expect(rowOf(table, own.folder)).toBeVisible()

  await table.getByRole('link', { name: own.folder, exact: true }).click()
  const inside = page.getByRole('table', { name: `Contents of ${own.folder}` })
  await expect(inside).toBeVisible()
  await expect(page).toHaveURL(/[?&]hide=other_source(&|$)/)
  await expect(page.getByRole('checkbox', { name: en.map.places.hide })).toBeChecked()
  await expect(await placeCell(inside, rowOf(inside, own.name))).toHaveText(en.map.places.file.only_copy)
})

test('sorting by what is only on this source puts the folder with an only copy first, and last ascending', async () => {
  await openMap(copies.root, 'places=1&color=places')
  const table = page.getByRole('table', { name: `Contents of ${copiesLabel}` })
  const header = table.getByRole('columnheader', { name: new RegExp(`^${en.map.places.column}`) })
  await header.getByRole('button').click()
  await expect(page).toHaveURL(/[?&]sort=only_here(&|$)/)
  await expect(header).toHaveAttribute('aria-sort', 'descending')
  const rows = table.getByRole('row')
  await expect(rows).toHaveCount(5)
  await expect(rows.nth(1).getByRole('link', { name: own.folder, exact: true })).toBeVisible()

  await header.getByRole('button').click()
  await expect(header).toHaveAttribute('aria-sort', 'ascending')
  await expect(rows.nth(4).getByRole('link', { name: own.folder, exact: true })).toBeVisible()
  await expect(rows.nth(1).getByRole('link', { name: own.folder, exact: true })).toHaveCount(0)
})

test('color by where copies are: the treemap still shows both folders with hide on, and the legend names each place', async () => {
  await openMap(copies.root)
  await turnOn(page.getByRole('switch', { name: en.map.places.toggle }))
  await expect(page.getByLabel(en.map.color)).toHaveValue('places')
  await turnOn(page.getByRole('checkbox', { name: en.map.places.hide }))
  await expect(page.getByRole('table', { name: `Contents of ${copiesLabel}` }).getByRole('row')).toHaveCount(2)

  // Each area says its place, in the legend's words for its color.
  const areas = page.getByRole('list', { name: en.map.treemapAreas })
  const area = (name: string, size: number, place: string) =>
    areas.getByRole('button', { name: `${fill(en.map.openFolder, { name })} (${bytes(size)}, ${place})`, exact: true })
  await expect(area(own.folder, ownBytes, en.map.place.only_copy)).toBeVisible()
  await expect(area(folder.to, folderBytes, en.map.place.other_source)).toBeVisible()
  await expect(page.getByRole('list', { name: en.map.legend }).getByRole('listitem')).toHaveText(Object.values(en.map.place))
})

test('a copy on the same source: the row names one of the other two and says there is one more', async () => {
  const photo = 'Fotos/2004/Natal/DSC00104.JPG'
  const others = (corpus.duplicates.find((d) => d.copies.some((c) => c.path === photo))?.copies ?? [])
    .map((c) => c.path)
    .filter((p) => p !== photo)
  // Two loose copies in other folders of the corpus, none anywhere else.
  expect(others).toHaveLength(2)
  expect(others.some((p) => p.includes('!'))).toBe(false)

  await openMap(await entryAt(corpusSource, 'Fotos/2004/Natal'), 'places=1&color=places')
  const table = page.getByRole('table', { name: 'Contents of Natal' })
  const row = rowOf(table, 'DSC00104.JPG')
  await expect(await placeCell(table, row)).toHaveText(fill(en.map.places.file.this_source, { formatted: '3' }))
  await expect(row).toContainText(fill(en.map.places.copy.more, { formatted: '1' }))
  const note = row.getByRole('link', { name: /^Also at / })
  const noteText = (await note.textContent()) ?? ''
  const named = others.find((p) => noteText === fill(en.map.places.copy.at, { path: p }))
  expect(named, 'the row names one of the other copies').toBeDefined()

  // The link opens the copy it names.
  await note.click()
  const id = await entryAt(corpusSource, named ?? '')
  await expect(page).toHaveURL(new RegExp(`[?&]entry=${id}(&|$)`))
  const panel = page.getByRole('complementary', { name: 'DSC00104.JPG' })
  await expect(panel.getByRole('navigation', { name: en.detail.location })).toContainText(
    (named ?? '').split('/').slice(0, -1).at(-1) ?? '',
  )
})

test('a copy inside an archive: the row says it is also inside the tar.gz, naming the member', async () => {
  const file = 'Projetos/site_antigo/contato.php'
  const archive = 'Projetos/site_antigo_2006.tar.gz'
  expect(corpus.duplicates.find((d) => d.copies.some((c) => c.path === file))?.copies.map((c) => c.path)).toEqual([
    file,
    `${archive}!contato.php`,
  ])

  await openMap(await entryAt(corpusSource, 'Projetos/site_antigo'), 'places=1&color=places')
  const table = page.getByRole('table', { name: 'Contents of site_antigo' })
  const row = rowOf(table, 'contato.php')
  await expect(await placeCell(table, row)).toHaveText(fill(en.map.places.file.this_source, { formatted: '2' }))
  const note = row.getByRole('link', {
    name: fill(en.map.places.copy.inside, { archive, member: 'contato.php' }),
    exact: true,
  })
  await expect(note).toBeVisible()
  await expect(row).not.toContainText(/\+\d+ more/)
  await note.click()
  // The link opens the member: the panel names the archive it is in.
  const panel = page.getByRole('complementary', { name: 'contato.php' })
  await expect(panel.getByRole('navigation', { name: en.detail.location })).toContainText(archive)
  await expect(panel).toContainText(fill(en.detail.member.replaceAll(/<\/?archiveLink>/g, ''), { path: basename(archive) }))
})

test('a folder copied whole names the folder it equals, and its Compare link opens Compare on the two', async () => {
  await openMap(copies.root, 'places=1&color=places')
  const table = page.getByRole('table', { name: `Contents of ${copiesLabel}` })
  const row = rowOf(table, folder.to)
  await expect(
    row.getByRole('link', {
      name: fill(en.map.places.covered.sameOn, { path: folder.from, source: corpusSource.label }),
      exact: true,
    }),
  ).toBeVisible()
  const left = await entryAt(copies, folder.to)
  const right = await entryAt(corpusSource, folder.from)
  await row
    .getByRole('link', { name: fill(en.map.places.covered.compareLabel, { name: folder.to, path: folder.from }), exact: true })
    .click()
  await expect(page).toHaveURL(new RegExp(`/compare\\?left=${left}&right=${right}(&|$)`))
  const sides = page.getByRole('region', { name: en.compare.sides }).locator(':scope > div')
  await expect(sides.nth(0)).toContainText(`${folder.to}${copiesLabel}`)
  await expect(sides.nth(1)).toContainText(`${folder.from}${corpusSource.label}`)
  // Every file of the one is identical to the file at the same path in the
  // other.
  await page.getByRole('navigation', { name: en.compare.groups }).getByRole('link', { name: /^Identical/ }).click()
  await expect(page.getByRole('list', { name: 'Files: Identical' }).locator(':scope > li > a')).toHaveText(
    folderFiles.map((e) => basename(e.path)).toSorted(),
  )
})

test('a folder’s panel lists the files and bytes of each place and of the disposable files, and the checked share', async () => {
  // The corpus's top folder holds only copies, copies only on its source,
  // the originals of every copy on the copies source, and system files.
  const originals = [...folderFiles.map((e) => e.path), ...photos.from]
  const originalBytes = originals.reduce((sum, path) => sum + sizeOf(path), 0)

  await openMap(corpusSource.root, 'places=1&color=places')
  await page.getByRole('link', { name: 'Details of celular_backup_2009', exact: true }).click()
  await page
    .getByRole('complementary', { name: 'celular_backup_2009' })
    .getByRole('navigation', { name: en.detail.location })
    .getByRole('link', { name: corpusSource.label, exact: true })
    .click()
  const section = page.getByRole('complementary').getByRole('region', { name: en.detail.places.title })
  const bars = section.getByRole('list', { name: en.detail.places.title }).getByRole('listitem')
  const bar = (label: string) => bars.filter({ has: page.getByText(label, { exact: true }) })
  for (const label of [en.detail.places.only_copy, en.detail.places.this_source, en.detail.places.disposable]) {
    await expect(bar(label), label).toContainText(/[\d.,]+\s(B|KiB|MiB|GiB) · [\d,]+ files?/)
  }
  await expect(bar(en.detail.places.other_source)).toContainText(
    `${bytes(originalBytes)} · ${formatCount(originals.length, 'en')} files`,
  )
  await expect(section).toContainText(en.detail.places.disposableHelp)
  await expect(section).toContainText(fill(en.coverage.claim, { percent: '100%' }))
})

test('a file’s panel says it has a copy on another source, above the copy’s path', async () => {
  const photo = 'Fotos/2007/Formatura/DSC03004.JPG'
  expect(corpus.duplicates.some((d) => d.copies.some((c) => c.path === photo))).toBe(false)

  await openMap(await entryAt(corpusSource, 'Fotos/2007/Formatura'), 'places=1&color=places')
  await page.getByRole('table', { name: 'Contents of Formatura' }).getByRole('link', { name: 'DSC03004.JPG', exact: true }).click()
  const copiesSection = page
    .getByRole('complementary', { name: 'DSC03004.JPG' })
    .getByRole('region', { name: en.detail.copies, exact: true })
  const sentence = copiesSection.getByText(en.detail.places.file.other_source, { exact: true })
  const copy = copiesSection
    .getByRole('list', { name: en.detail.copiesList })
    .getByRole('link', { name: `${photos.to}/${basename(photo)}`, exact: true })
  await expect(sentence).toBeVisible()
  await expect(copy).toBeVisible()
  const [above, below] = [await sentence.boundingBox(), await copy.boundingBox()]
  expect(above?.y ?? Number.NaN).toBeLessThan(below?.y ?? Number.NaN)
})

// openMap opens the Map at folder with the view in query, and waits for its
// table to list a row.
async function openMap(folderId: string, query = '') {
  await page.goto(`/map/${folderId}${query === '' ? '' : `?${query}`}`)
  await expect(page.getByRole('table', { name: /^Contents of / }).getByRole('row').nth(1)).toBeVisible()
}

// turnOn clicks a box the address holds, and waits until the address has
// turned it on: the box shows the address's state, not its own.
async function turnOn(box: Locator) {
  await box.click()
  await expect(box).toBeChecked()
}

// rowOf is the row of table whose name link is name.
function rowOf(table: Locator, name: string): Locator {
  return table.getByRole('row').filter({ has: page.getByRole('link', { name, exact: true }) })
}

// placeCell is row's cell in the Where copies are column of table, so that a
// hidden column fails instead of reading another one.
async function placeCell(table: Locator, row: Locator): Promise<Locator> {
  await expect(table.getByRole('columnheader', { name: new RegExp(`^${en.map.places.column}`) })).toBeVisible()
  const headers = await table.getByRole('columnheader').allTextContents()
  const index = headers.findIndex((h) => h.startsWith(en.map.places.column))
  expect(index, 'the Where copies are column').toBeGreaterThanOrEqual(0)
  return row.getByRole('cell').nth(index)
}

function basename(path: string): string {
  return path.split('/').at(-1) ?? ''
}

// fill puts values into a catalog template's {{name}} placeholders.
function fill(template: string, values: Record<string, string>): string {
  return template.replaceAll(/\{\{(\w+)\}\}/g, (_, name: string) => values[name] ?? `{{${name}}}`)
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
      'Idempotency-Key': `e2e-${name}-${Date.now()}-${Math.random()}`,
    },
  })
  return { status: resp.status(), body: (await resp.json()) as T }
}

interface SourceJSON {
  id: string
  label: string
  path: string | null
  root_entry_id: string | null
  last_scan_at: string | null
  active_job: unknown
}

// sourceAt reads the source at path from the read API, and whether a scan
// of it has ended.
async function sourceAt(path: string): Promise<(SourceJSON & { scanned: boolean }) | undefined> {
  const listed: { sources: SourceJSON[] } = await (await page.request.get('/api/sources')).json()
  const source = listed.sources.find((s) => s.path === path)
  return source === undefined ? undefined : { ...source, scanned: source.last_scan_at !== null && source.active_job === null }
}

async function scannedSource(path: string): Promise<Source> {
  const source = await sourceAt(path)
  if (source === undefined || source.root_entry_id === null) {
    throw new Error(`${path} is not a scanned source`)
  }
  return { id: source.id, label: source.label, root: source.root_entry_id }
}

// entryAt finds the entry at path inside source, folder by folder.
async function entryAt(source: Source, path: string): Promise<string> {
  let id = source.root
  for (const name of path.split('/')) {
    const body: { items: { id: string; name: string }[] } = await (
      await page.request.get(`/api/entries/${id}/children?sort=name&order=asc&limit=1000`)
    ).json()
    id = body.items.find((i) => i.name === name)?.id ?? ''
    expect(id, `${path}: ${name}`).not.toBe('')
  }
  return id
}
