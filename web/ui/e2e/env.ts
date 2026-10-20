import { readFileSync } from 'node:fs'

// adminPassword is the administrator password global-setup.ts sets.
export const adminPassword = 'e2e correct horse battery staple'

// The bulk folder next to the corpus has bulkFolders folders, grouped in 40
// more, of bulkFiles files each: enough that its scan runs for seconds, so
// its progress can be watched.
export const bulkFolders = 2000
export const bulkFiles = 50

// setting returns what global-setup.ts recorded under name; test files read
// it when they load, after global setup.
function setting(name: string): string {
  const value = process.env[name]
  if (value === undefined) {
    throw new Error(`${name} is not set; run the suite with \`npx playwright test\``)
  }
  return value
}

// origin is the server global-setup.ts started.
export function origin(): string {
  return setting('PRECIOUS_E2E_ORIGIN')
}

// corpusPath is the corpus folder, canonical, as the server shows it.
export function corpusPath(): string {
  return setting('PRECIOUS_E2E_CORPUS')
}

// TruthEntry is one entry of the corpus's ground_truth.json (tools/gencorpus).
export interface TruthEntry {
  path: string
  kind: string
  size?: number
  sha256?: string
  unreadable?: boolean
  // date is set for every image and video.
  date?: TruthDate
}

// TruthDate is a media file's effective date for the zone UTC, without
// corrections, after the media job (r5 design D19). effective is the instant
// (RFC 3339, UTC; the start of the period when coarser than a second), null
// for source "none"; local is in the form of its precision ("YYYY",
// "YYYY-MM", "YYYY-MM-DD", or "YYYY-MM-DDTHH:MM:SS"); flags are the flag
// names in bit order.
export interface TruthDate {
  effective: string | null
  local: string
  precision: 'second' | 'day' | 'month' | 'year' | ''
  source: 'owner' | 'exif' | 'gps' | 'container' | 'file_name' | 'folder_name' | 'mtime' | 'none'
  refined: boolean
  flags: ('mtime_disagrees' | 'implausible' | 'camera_offset' | 'no_date_metadata')[]
}

// TruthCamera is a camera the media job's cameras pass lists: the photos it
// was given, and for the camera with a clock offset its suggested shift and
// event folders (by display path); shift_s is 0 and folders empty when it is
// ok.
export interface TruthCamera {
  key: string
  shift_s: number
  photos: number
  folders: string[]
}

// TruthPath names an entry, or an archive member as "archive!member/path".
export interface TruthPath {
  path: string
  path_b64: string
}

// TruthDuplicate is a duplicate group: a content with two or more copies.
export interface TruthDuplicate {
  sha256: string
  size: number
  copies: TruthPath[]
}

// TruthMember is a member of an archive; its path is relative to the archive.
export interface TruthMember extends TruthPath {
  kind: string
  size?: number
}

// TruthArchive is the complete listing of one archive file.
export interface TruthArchive extends TruthPath {
  format: string
  members: TruthMember[]
}

// TruthRelation is a declared relation: for same and inside, a is the
// archive or contained side; for overlap, the side with the larger matched
// share. a_only and b_only are the copies whose content is only on that side.
export interface TruthRelation {
  kind: 'same' | 'inside' | 'overlap'
  a: TruthPath
  b: TruthPath
  a_only: TruthPath[]
  b_only: TruthPath[]
}

// TruthRescue is a row of the rescue card: the owner's own file or folder
// inside its outermost program or disposable group, by display path.
export interface TruthRescue {
  path: string
  group: string
}

// GroundTruth is the corpus's ground_truth.json (internal/corpus): its
// entries, duplicate groups, archive listings, declared relations (a lower
// bound: relate may find more), the rescue card's rows, each in its order,
// and the cameras of the media dates (r5).
export interface GroundTruth {
  entries: TruthEntry[]
  duplicates: TruthDuplicate[]
  members: TruthArchive[]
  relations: TruthRelation[]
  rescue: TruthRescue[]
  cameras: TruthCamera[]
}

// groundTruth reads the ground truth of the corpus global-setup.ts wrote.
export function groundTruth(): GroundTruth {
  const file = setting('PRECIOUS_E2E_TRUTH')
  // tools/gencorpus writes this file from the same build; its shape is fixed.
  const truth: GroundTruth = JSON.parse(readFileSync(file, 'utf8'))
  return truth
}
