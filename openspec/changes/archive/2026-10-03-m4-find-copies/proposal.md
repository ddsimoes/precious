# Proposal

## Why

The archive this product exists for (a 1.17 TB, 1.3-million-file backup tree surveyed on 2026-10-03) holds most of its redundancy as whole folders copied, renamed, and rearranged: two photo trees of about 105 GB each sit entirely inside a third, under different folder layouts and lowercased file names. Matching by name finds 10% of that; matching by content finds all of it. After M3a nothing reads content, so none of it can be seen. This change is milestone **M4, selective comparison** (§14, §11.1, §5.5.6), re-scoped by ADR 0005 and ADR 0006. It makes **A10** pass and represents incomplete coverage honestly. Nothing on disk changes: removing copies arrives with M5.

## What Changes

- **Copy search (§11.1, §5.5.6, A10):**
  - The owner starts `find-copies` on a whole source or on chosen folders, including atomic units. One search per source runs at a time, as a cancellable bulk-class job that yields between work units.
  - The search lists every entry under its folders into a snapshot of its own. The snapshot creates no inventory node and never appears in the explorer, totals, or review queues (ADR 0005).
  - Files whose size occurs once are distinct without being read. Same-size files are read and hashed with SHA-256, large files first. Files of 16 MiB or more are first compared by three 64 KiB samples, which can only rule a match out.
  - Small same-size files are read only inside folder pairs that already look like copies.
  - Every read reopens the file without following links, checks that it is the same regular file the listing saw, and checks again after reading. A file that changed is `unstable`: it gets no digest and blocks every claim that would rest on it.
  - Digests are cached per file identity (device, inode, size, modification and change time), so a repeated search reads only what changed.
- **Folder results (ADR 0006):** a folder is `inside` another when every file under it has an identical file in the other, whatever the names and layout. Two folders that are inside each other are the `same`. Partial overlaps show their share. Results are ranked by bytes that a removal would free, one line per copy rather than per subfolder. Large files with copies that no folder result explains are listed too. Unlisted, unreadable, unstable, and unchecked entries are counted, and they prevent an `inside` claim.
- **Freshness (§5.5.7):** a result says `changed since checked` as soon as anything the catalog can observe under either folder changed. Wording never claims "the only copy on this disk".
- **Marking (§10.1):** `set-disposition` with `cleanup_candidate` accepts a copy result as evidence for a folder or file the result names. The evidence is stored with the mark and shown with it, and a stale result is refused with `stale_evidence`. Protection and inbox locks refuse the mark as before. A new **Marked for cleanup** review queue lists marked nodes with their evidence.
- **UI and API (§9.1, §9.3):** a Copies page per search, a Copies section in the inspector, a header link, and `GET /api/copy-searches`, `GET /api/copy-searches/{id}`, and `GET /api/copy-searches/{id}/results`.
- **Filesystem boundary (§5.4, §12.2, A11, A20):** regular-file content may be read, and only by a copy search. Nothing is written, and content is never stored, logged, or sent anywhere; only digests are kept.
- **BREAKING (internal):** migration `0006`. `fsaccess.Dir` gains `OpenFile`, and `EntryInfo` gains the change time.

Deferred, with owning milestone:
- Looking inside ZIP, TAR, and 7z archives, and comparing archives with folders → **M4b** (ADR 0006).
- Copy searches across several sources → **M4b**.
- Near-copies (re-encoded media, `.old` revisions, unfinished downloads) → **M6** (§11.4).
- Choosing keepers, byte-for-byte recheck at action time, quarantine of copies, and A14's unavailable keeper → **M5**.
- What a protection pin means when the protected folder is only a copy → **M5**.
- "Already have this?" for inbox arrivals, deferred to M4 by M3a → **M5**, with inbox organization.
- Watches on selected atomic boundaries, deferred to M4 by M3a → **M6**: deep changes inside a unit stay invisible to watches anyway.
- Searches that start on their own or on a schedule are not planned. A search runs only when the owner starts it.

## Capabilities

### New Capabilities
- `content-comparison`: copy searches, their snapshot, size grouping, sampled and full hashing, the digest cache, unstable files, folder and file results, coverage gaps, and freshness.

### Modified Capabilities
- `filesystem-boundary`: regular-file content may be read by copy searches only, through an identity-checked, no-follow, non-blocking open.
- `owner-intent`: a `cleanup_candidate` mark may carry a copy result as evidence, which is refused when stale.
- `review-queue`: a `marked` queue lists nodes marked `cleanup_candidate` with their evidence.
- `inventory-explorer`: the Copies pages, the copy-search read API, the inspector's Copies section, and copy searches in the read-only workflow.
- `job-runner`: copy searches are bulk-class jobs.
- `server-config`: `[copies]` settings are validated.

## Impact

- **New package:** `internal/compare` (copy-search job, snapshot, hashing, digest cache, folder relations, freshness, evidence), the package §8.2 names.
- **Changed packages:** `fsaccess` (file open and change time in the OS backend, `synthfs` with content, and the call recorder), `domain`, `config`, `commands`, `intent` (one outcome constant), `inventory` (marked queue, evidence reads), `web/explorer`, `web` templates and `app.js`, and `cmd/curator`.
- **Configuration:** a new `[copies]` section.
- **Database:** migration `0006`, additive.
- **Docs:** `docs/operator.md` gains a Finding copies section and an M3a to M4 upgrade note, and every statement that curator never reads file content is revised. ADR 0005 and ADR 0006.
- **Dependencies:** none new; SHA-256 comes from `crypto/sha256`.
