# Proposal

## Why

Archives hold 356 GB, 30% of the owner's surveyed 1.17 TB backup tree. That includes a 105 GB `.tar.gz` machine backup next to its unpacked copy, and three snapshots of one machine. M4 compares archives only as opaque files, so it cannot say that an archive is already unpacked somewhere, or that one snapshot is inside another. This change is milestone **M4b** (ADR 0006): read-only archive inspection (§11.3), archives compared with folders by their unpacked content, and copy searches across sources. No A-ID belongs to M4b. ADR 0006 leaves its acceptance scenarios to this proposal, and they are listed below. Nothing on disk changes.

## What Changes

- **Archives are opened by every copy search (§11.3, ADR 0007).** zip, tar, tar.gz, tar.bz2, and single-file gzip and bzip2 are opened. A file is opened when its name says it is one of these formats and its first bytes confirm it. 7z, rar, xz, and zstd archives are counted as "not opened". An archive inside an archive is compared as a whole file but never opened. jar, docx, and other formats that use zip inside are not archives here.
- **Content, never metadata (§11.3).** Members are compared by the SHA-256 of their unpacked bytes. Names, sizes, and CRC values never establish a match, and a CRC that disagrees with the unpacked bytes makes the archive `corrupt`.
- **Safety (§11.3, §5.4).** Unpacking happens in memory only; nothing is ever written to disk. Each archive has budgets for entries, unpacked bytes, expansion ratio, and time. An archive is `rejected` when a member path leaves the archive or two members collide, and is reported `encrypted`, `corrupt`, `unsupported`, or `partial` when it cannot be opened completely. Such an archive is compared as a plain file, so no claim rests on its contents.
- **Archives relate as folders.** An opened archive can be `inside` a folder or another archive, hold one, or be the `same` as one. When an archive and a folder are the same, the archive is the copy (owner's choice). Removing a whole archive frees its packed size. A folder inside an archive frees nothing and cannot be marked.
- **Reading cost.** A zip's member list is read from its directory, and its members are read only when their size occurs elsewhere, like M4's files. A tar-based or gzip/bzip2 archive is read once from start to end, hashing every member in that pass. An unchanged archive is never opened again: its member list and digests are cached by file identity, like M4's digests.
- **Archive contents in the inspector (owner's choice).** The inspector of an archive file lists its folders and files as the last search found them, with the time, and says when the archive changed since.
- **Copy searches across sources (ADR 0006).** One search may cover several sources, or folders from several sources. A job may now move from one source's device to another at a work-unit boundary, holding one device at a time.
- **BREAKING (internal):** migration `0007`. The `find-copies` request body changes shape, and `jobs.Runtime` gains `UseSource`.

Acceptance scenarios (ADR 0006), each with its own test task. Spec scenarios whose titles start with these IDs define them:
- **M4b-1** A `.tar.gz` and its unpacked folder are reported the `same`, with the archive as the copy and its packed size freeable.
- **M4b-2** An older snapshot archive is reported inside a newer one, and a zip of some photos is reported inside the photo folder.
- **M4b-3** A zip member with the name, size, and CRC-32 of a file but different bytes is not a match.
- **M4b-4** A zip bomb, a path that leaves the archive, a duplicate member, an encrypted zip, a truncated `.tar.gz`, and an unsupported compression method each end in their state, with no claim on their contents.
- **M4b-5** A repeated search does not reopen an unchanged archive.
- **M4b-6** The inspector of an archive lists its contents and says when the archive changed since.
- **M4b-7** A folder on one source is reported inside a folder on another, and inbox work on either device is served while the search runs.
- **M4b-8** A restart during an archive's read resumes, reusing every archive finished before it.

Deferred, with owning milestone:
- Archives inside archives, and 7z, rar, xz, and zstd archives, are never opened → **M6** (§11.3 "nesting budgets"; ADR 0007).
- Path-aware equality ("same tree") and revisions of one archive → **M6** (§11.2, §11.4).
- Unpacking an archive or keeping one side of a copy → **M5**, through plans.

## Capabilities

### New Capabilities

None. Archive handling extends `content-comparison`.

### Modified Capabilities
- `content-comparison`: archives are opened under budgets, compared by unpacked content, cached by file identity, and related as folders. A search may cover several sources.
- `filesystem-boundary`: archives are unpacked in memory only, and member listings of supported archives are kept.
- `inventory-explorer`: the inspector lists archive contents. Copies pages and the read API show archive sides and sources, and start searches across sources.
- `owner-intent`: a side inside an archive cannot be marked; a whole archive can.
- `job-runner`: a job moves between devices one at a time.
- `server-config`: archive budgets are validated.

## Impact

- **Changed packages:** `compare` (new archive phase in `archive.go`, cache, relate, results, reader, handler), `jobs` (`Runtime.UseSource`), `config`, `commands` (`find-copies` body), `web/explorer`, the `web` templates and `app.js`, and `cmd/curator`.
- **Database:** migration `0007`, additive: archive cache tables, search sources, and result columns.
- **Configuration:** new `[copies]` archive keys.
- **Docs:** `docs/operator.md` (Finding copies: archives, budgets, cross-source searches; M4 to M4b upgrade note) and ADR 0007.
- **Dependencies:** none new: `archive/zip`, `archive/tar`, `compress/gzip`, `compress/bzip2`, and `crypto/sha256`.
