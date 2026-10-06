import type { EntryDetail, EntryRow } from '@/api/entries'
import type { Home } from '@/api/home'
import type { JobEvent } from '@/api/jobs'
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
      { year: 2024, bytes: 70 * GiB, files: 300_000 },
      { year: 2004, bytes: 50 * GiB, files: 110_000 },
    ],
    decisions: {
      undecided: { bytes: 50 * GiB, files: 300_000 },
      keep: { bytes: 25 * GiB, files: 60_000 },
      discard: { bytes: 40 * GiB, files: 45_000 },
      later: { bytes: 5 * GiB, files: 5_000 },
    },
    partial: false,
    scans: [],
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
    ancestors: [{ id: '1', name: '', name_b64: '' }],
    classification: {
      category: row.category,
      family: row.family,
      traits: [],
      triage: row.triage,
      group: row.group,
      veto: row.veto,
      rules: [],
      indicators: [],
    },
    intent: { decision: row.decision, eff_decision: row.eff_decision, from: null, tags: [] },
    stats: null,
    ...overrides,
  }
}
