import type { Check, CheckFile, Quarantined } from '@/api/cleanup'
import type { Copy, Coverage, Relation } from '@/api/content'
import type { EntryDetail, EntryRow } from '@/api/entries'
import type { Home } from '@/api/home'
import type { JobEvent } from '@/api/jobs'
import type { Card, ReviewRow } from '@/api/opportunities'
import type { Action, Item } from '@/api/organize'
import type { Capabilities, PickerItem, Source } from '@/api/sources'

// API responses in the shapes of the design's Interfaces section.

export const ext4Capabilities: Capabilities = {
  known: true,
  read_only: false,
  case_sensitive: true,
  normalization_sensitive: true,
  stable_identity: true,
  local_time: false,
  hard_links: true,
  time_resolution_ns: 1,
  no_replace_rename: true,
}

export const portableCapabilities: Capabilities = {
  known: false,
  read_only: false,
  case_sensitive: false,
  normalization_sensitive: false,
  stable_identity: false,
  local_time: false,
  hard_links: false,
  time_resolution_ns: 2_000_000_000,
  no_replace_rename: false,
}

const GiB = 1024 ** 3

export function fotosSource(overrides: Partial<Source> = {}): Source {
  return {
    id: 'fotos',
    label: 'Fotos',
    state: 'online',
    state_reason: null,
    mount_point: '/mnt/tank/fotos',
    path: '/mnt/tank/fotos',
    rel_root: '',
    volume: { kind: 'zfs', id: 'tank/fotos', label: null, fs_type: 'zfs', strong: true },
    capabilities: ext4Capabilities,
    root_entry_id: '1',
    totals: { bytes: 120 * GiB, files: 410_000, dirs: 31_000 },
    last_scan_at: '2026-10-01T14:30:00Z',
    active_job: null,
    schedule: null,
    next_scan_at: null,
    schedule_skipped: null,
    writes: { enabled: false, unavailable: null },
    quarantine: { files: 0, bytes: 0, name_taken: false },
    ...overrides,
  }
}

export function usbSource(overrides: Partial<Source> = {}): Source {
  return {
    id: 'old-disk',
    label: 'Old disk',
    state: 'offline',
    state_reason: 'volume not mounted',
    mount_point: null,
    path: null,
    rel_root: 'Backups',
    volume: { kind: 'path', id: '/media/usb', label: 'USB', fs_type: 'vfat', strong: false },
    capabilities: portableCapabilities,
    root_entry_id: '900',
    totals: { bytes: 5 * GiB, files: 1_200, dirs: 80 },
    last_scan_at: null,
    active_job: null,
    schedule: null,
    next_scan_at: null,
    schedule_skipped: null,
    writes: { enabled: false, unavailable: 'no_replace_rename' },
    quarantine: { files: 0, bytes: 0, name_taken: false },
    ...overrides,
  }
}

export function pickerItem(path: string, overrides: Partial<PickerItem> = {}): PickerItem {
  return {
    handle: `h-${btoa(path)}`,
    name: path.split('/').at(-1) || path,
    path,
    volume_label: '',
    fs_type: 'ext4',
    is_source: false,
    ...overrides,
  }
}

export function homeResponse(overrides: Partial<Home> = {}): Home {
  return {
    totals: { bytes: 120 * GiB, files: 410_000, dirs: 31_000 },
    by_family: [
      { family: 'personal', bytes: 80 * GiB, files: 300_000 },
      { family: 'programs', bytes: 25 * GiB, files: 90_000 },
      { family: 'disposable', bytes: 5 * GiB, files: 15_000 },
      { family: 'containers', bytes: 10 * GiB, files: 5_000 },
    ],
    by_kind: [
      { kind: 'video', bytes: 60 * GiB, files: 2_000 },
      { kind: 'image', bytes: 40 * GiB, files: 200_000 },
      { kind: 'other', bytes: 20 * GiB, files: 208_000 },
    ],
    by_year: [
      { year: null, bytes: 5 * GiB, files: 1_000 },
      { year: 2024, bytes: 70 * GiB, files: 300_000 },
      { year: 2004, bytes: 45 * GiB, files: 109_000 },
    ],
    decisions: {
      undecided: { bytes: 50 * GiB, files: 300_000 },
      keep: { bytes: 25 * GiB, files: 60_000 },
      discard: { bytes: 40 * GiB, files: 45_000 },
      later: { bytes: 5 * GiB, files: 5_000 },
      quarantine: { bytes: 0, files: 0 },
    },
    partial: false,
    scans: [],
    coverage: coverage(),
    cards: [],
    hashing: [],
    ...overrides,
  }
}

// coverage is a CoverageJSON; by default 90 GiB could have a copy and 80 GiB
// of it was checked.
export function coverage(overrides: Partial<Coverage> = {}): Coverage {
  return {
    candidate: { bytes: 90 * GiB, files: 200_000 },
    checked: { bytes: 80 * GiB, files: 150_000 },
    unchecked: { bytes: 9 * GiB, files: 49_000 },
    unreadable: { bytes: 1 * GiB, files: 1_000 },
    ...overrides,
  }
}

export function card(list: Card['list'], bytes: number, rows: number, overrides: Partial<Card> = {}): Card {
  return {
    list,
    bytes,
    rows,
    decided_rows: 0,
    decided_bytes: 0,
    basis: list === 'duplicates' || list === 'unpacked_archives' ? 'content' : 'rules',
    ...overrides,
  }
}

// copyOf is a CopyJSON of row, which follows its folder.
export function copyOf(row: EntryRow, overrides: Partial<Copy> = {}): Copy {
  return {
    ref: row.id,
    source_id: row.source_id,
    path: row.path,
    path_b64: row.path_b64,
    archive_id: row.archive_id,
    hard_link: false,
    offline: false,
    decision: null,
    eff_decision: row.eff_decision,
    ...overrides,
  }
}

// relationTo is a RelationJSON whose other side is other.
export function relationTo(other: EntryRow, overrides: Partial<Relation> = {}): Relation {
  return {
    id: '7',
    kind: 'same',
    self: 'a',
    other,
    matched_bytes: other.total_bytes,
    redundant_bytes: other.total_bytes,
    only_here: { files: 0, bytes: 0 },
    only_there: { files: 0, bytes: 0 },
    ...overrides,
  }
}

// reviewRow is a RowJSON of an entry row; its summary repeats the entry's
// category, files, and bytes, with the years 2003 to 2004.
export function reviewRow(id: string, entry: EntryRow | null, overrides: Partial<ReviewRow> = {}): ReviewRow {
  return {
    id,
    bytes: entry?.total_bytes ?? 0,
    files: entry?.total_files ?? 0,
    entry,
    relation: null,
    copies: null,
    group: null,
    summary: {
      category: entry?.category ?? null,
      years: [2003, 2004],
      files: entry?.total_files ?? 0,
      bytes: entry?.total_bytes ?? 0,
      signals: [],
    },
    ...overrides,
  }
}

export function scanEvent(overrides: Partial<JobEvent> = {}): JobEvent {
  return {
    job_id: '42',
    kind: 'scan',
    source_id: 'fotos',
    state: 'running',
    cancel_requested: false,
    progress: { phase: 1, dirs: 10, files: 100, bytes: 1024 },
    attempts: 1,
    ...overrides,
  }
}

// entryRow is an EntryRow; by default the file Downloads/setup.exe of fotos.
export function entryRow(overrides: Partial<EntryRow> = {}): EntryRow {
  return {
    id: '12',
    source_id: 'fotos',
    name: 'setup.exe',
    name_b64: btoa('setup.exe'),
    path: 'Downloads/setup.exe',
    path_b64: btoa('Downloads/setup.exe'),
    kind: 'file',
    file_kind: 'installer',
    main_kind: null,
    category: 'installer_download',
    family: 'programs',
    triage: 'discard',
    group: false,
    veto: false,
    size: 3 * 1024 ** 2,
    total_bytes: 3 * 1024 ** 2,
    total_files: 1,
    mtime: '2004-12-24T10:00:00Z',
    newest: '2004-12-24T10:00:00Z',
    oldest: '2004-12-24T10:00:00Z',
    state: 'present',
    partial: false,
    mount_boundary: false,
    decision: null,
    eff_decision: 'undecided',
    tag_ids: [],
    content_state: null,
    copies: null,
    candidate_bytes: null,
    checked_bytes: null,
    duplicated_bytes: null,
    archive_state: null,
    archive_id: null,
    ...overrides,
  }
}

// folderRow is an EntryRow of a folder.
export function folderRow(id: string, name: string, overrides: Partial<EntryRow> = {}): EntryRow {
  return entryRow({
    id,
    name,
    name_b64: btoa(name),
    path: name,
    path_b64: btoa(name),
    kind: 'directory',
    file_kind: null,
    main_kind: 'image',
    category: 'personal_media',
    family: 'personal',
    triage: 'keep',
    size: 0,
    total_bytes: 10 * GiB,
    total_files: 1_000,
    oldest: '2001-01-01T00:00:00Z',
    ...overrides,
  })
}

// entryDetail is a GET /api/entries/{id} response for row, at the top of
// fotos unless ancestors are given.
export function entryDetail(row: EntryRow, overrides: Partial<EntryDetail> = {}): EntryDetail {
  return {
    entry: row,
    ancestors: [{ id: '1', name: '', name_b64: '', only_child: false }],
    classification: {
      category: row.category,
      family: row.family,
      traits: [],
      triage: row.triage,
      group: row.group,
      veto: row.veto,
      rules: [],
      indicators: [],
      owner: { category: null, group: null },
      rules_category: row.category,
      rules_group: row.group,
    },
    intent: { decision: row.decision, eff_decision: row.eff_decision, from: null, tags: [] },
    stats: null,
    content: null,
    relations: [],
    archive: null,
    coverage: coverage(),
    only_folder: null,
    archive_note: null,
    in_quarantine: null,
    ...overrides,
  }
}

// action is an Action of the history; by default a planned single move of
// one 3 MiB file on fotos, with nothing in conflict.
export function action(overrides: Partial<Action> = {}, counts: Partial<Action['counts']> = {}): Action {
  return {
    id: '77',
    kind: 'move',
    source_id: 'fotos',
    state: 'planned',
    created_at: '2026-10-07T10:00:00Z',
    expires_at: '2026-10-07T11:00:00Z',
    started_at: null,
    finished_at: null,
    destination: null,
    job_id: null,
    undo_of: null,
    bulk: false,
    counts: {
      planned: 0,
      refused: 0,
      conflict: 0,
      blocked: 0,
      intent: 0,
      done: 0,
      not_permitted: 0,
      offline: 0,
      changed: 0,
      failed: 0,
      no_safe_rename: 0,
      not_empty: 0,
      manual_recovery: 0,
      not_attempted: 0,
      resolved: 0,
      ...counts,
    },
    bytes: 3 * 1024 ** 2,
    files: 1,
    kept_lost: 0,
    reversed: 0,
    undo: { possible: false, reason: 'not_done' },
    ground: null,
    list: null,
    check_id: null,
    deleted_files: 0,
    deleted_bytes: 0,
    freed_bytes: 0,
    ...overrides,
  }
}

// actionItem is an Item renaming from into to, planned.
export function actionItem(id: string, from: string, to: string, overrides: Partial<Item> = {}): Item {
  return {
    id,
    seq: Number(id),
    op: 'rename',
    entry: null,
    from: { path: from, path_b64: btoa(from) },
    to: { path: to, path_b64: btoa(to) },
    state: 'planned',
    reason: null,
    decision_after: null,
    detail: null,
    found: null,
    reversed: false,
    bytes: 1024,
    files: 1,
    ...overrides,
  }
}

// entryCounts is an Action's entries: every state at 0 but those given.
export function entryCounts(counts: Partial<Action['counts']> = {}): Action['counts'] {
  return action({}, counts).counts
}

// quarantined is a Quarantined item of fotos: by default the file
// Downloads/setup.exe, moved to quarantine by plan 50, never checked.
export function quarantined(id: string, original: string | null, overrides: Partial<Quarantined> = {}): Quarantined {
  const name = (original ?? `found-${id}`).split('/').at(-1) ?? id
  const path = `.precious-quarantine/50/${id}/${name}`
  return {
    entry: entryRow({ id, name, name_b64: btoa(name), path, path_b64: btoa(path) }),
    original: original === null ? null : { path: original, path_b64: btoa(original) },
    plan_id: original === null ? null : '50',
    quarantined_at: original === null ? null : '2026-10-08T09:00:00Z',
    bytes: 3 * 1024 ** 2,
    files: 1,
    check: null,
    ...overrides,
  }
}

const noAmount = { files: 0, bytes: 0 }

// check is a Check of fotos: by default a ready check of two items, with
// one unique photo, one unique junk file, and one file with a copy, of
// which the photo and the junk still need their confirmations.
export function check(overrides: Partial<Check> = {}): Check {
  return {
    id: '9',
    source_id: 'fotos',
    state: 'ready',
    job_id: '31',
    created_at: '2026-10-08T10:00:00Z',
    finished_at: '2026-10-08T10:05:00Z',
    stale_reason: null,
    items: 2,
    counts: {
      verdict: {
        safe: { files: 1, bytes: 1024 },
        copy_offline: noAmount,
        unique: { files: 2, bytes: 3 * 1024 ** 2 },
        unreadable: noAmount,
        opaque_archive: noAmount,
        no_content: { files: 1, bytes: 0 },
      },
      class: {
        possibly_valuable: { files: 1, bytes: 2 * 1024 ** 2 },
        likely_junk: { files: 1, bytes: 1024 ** 2 },
        uncertain: noAmount,
      },
    },
    confirmed: noAmount,
    unconfirmed: { files: 2, bytes: 3 * 1024 ** 2 },
    junk_confirmed: false,
    allowed: false,
    ...overrides,
  }
}

// checkFile is a CheckFile of item 61 of check 9; by default a unique file.
export function checkFile(id: string, path: string, overrides: Partial<CheckFile> = {}): CheckFile {
  return {
    id,
    item: quarantined('61', 'Fotos/2004').entry,
    entry_id: `e${id}`,
    path,
    path_b64: btoa(path),
    member: null,
    kind: 'file',
    size: 1024 ** 2,
    verdict: 'unique',
    class: 'uncertain',
    copy: null,
    confirmed: false,
    ...overrides,
  }
}
