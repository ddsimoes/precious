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
  unreadable?: boolean
}

// groundTruth reads the ground truth of the corpus global-setup.ts wrote.
export function groundTruth(): TruthEntry[] {
  const file = setting('PRECIOUS_E2E_TRUTH')
  // tools/gencorpus writes this file from the same build; its shape is fixed.
  const truth: { entries: TruthEntry[] } = JSON.parse(readFileSync(file, 'utf8'))
  return truth.entries
}
