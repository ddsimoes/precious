# Proposal

## Why

R1 shows every folder's size and lets the owner find and decide anything, but it cannot say what is a copy of what. Duplicates are most of the redundancy on the owner's disk (ADR 0005-0007): photo trees copied three times, zips next to their unpacked folders, whole machines backed up twice. This change is milestone **R2** of `precious-spec-v0.3.md` (§14): background hashing over the index, duplicate groups and folder relations, Compare, Opportunities with their review lists, and Gems, so the owner can explore copies and valuables whenever they choose. Nothing on disk changes (I1).

## What Changes

- **Hashing (§6.4, §7).**
  - A background job hashes the files that could have a copy, without blocking anything else.
  - Coverage is always visible.
  - An unchanged file is never read twice.
- **Archives (§6.4, §11.12).**
  - zip and the tar family are read in memory, with safety budgets.
  - Their members are browsable read-only entries that take part in duplicates and relations, and the viewer opens them.
  - Only the whole archive can be decided.
- **Duplicates and relations (§6.4, §6.3).**
  - Duplicate groups, and the folder relations `same`, `inside`, and `overlap`.
  - Each folder's duplicated bytes, shown in the Map.
- **Screens (§11.1-§11.8):**
  - Home shows hashing coverage and the opportunity cards;
  - Opportunities, with review lists that support the keyboard and bulk decisions;
  - Compare;
  - Gems;
  - a "% duplicated" column and coloring in the Map;
  - a duplicate filter in Search;
  - copies and relations in the detail panel;
  - browsing inside archives.
- **Decisions stay the owner's (owner's decision, 2026-10-06).**
  - Duplicates are information. Every copy is decided with the existing controls, one by one or in bulk.
  - Deciding one copy changes nothing on the others.
  - The spec's "keeper" (§6.7, §11.6, R2.4) is removed, and the spec is updated to match.
- **Rescans** also compare change times, so a changed file loses its cached digest.

Acceptance scenarios: **R2.1–R2.8** (§14, with R2.4 as revised). Each has a spec scenario whose title starts with its ID, and its own test task.

Deferred, with owning milestone:
- **After R2, before R3 (a short step):** owner category overrides (§6.6), owner-marked groups (§6.5), and scheduled rescans (§7).
- **R3:** moving the files unique to one copy into another (§11.6), and Gems' move action (§11.7).
- **R6:** probabilities in cards and review lists (§11.4, §11.5).
- **R7:** version families in Gems; nested archives and formats beyond the standard library.
- **R8:** native hashing and archive paths on macOS and Windows, which keep the portable backend until then.

## Capabilities

### New Capabilities

- `archive-contents`: reading zip and tar-family archives in memory with budgets, and their members as read-only entries.
- `duplicates`: duplicate groups, folder relations, folder duplication figures, and comparing two folders or archives.
- `review-lists`: opportunity cards and their review lists, and Gems.

### Modified Capabilities

- `file-index`: content hashing, coverage, and digest caching; rescans compare change times and invalidate digests.
- `inventory-explorer`: Home, Map, Search, and the detail panel gain duplicates; new Opportunities, review list, Compare, and Gems screens.
- `file-viewer`: archive members open in the viewer without extraction.
- `owner-intent`: a copy is decided like any entry, and archive members cannot be decided.
- `state-store`: the R2 migration upgrades an R1 database in place.
- `server-config`: hashing, archive, and duplicates settings.

## Impact

- **Restored from git tag `curator-m4b`, then adapted:**
  - `internal/compare/unpack`, which becomes `internal/archive`;
  - the hashing core of `internal/compare`, which becomes `internal/content`;
  - the relation engine, which becomes `internal/relations`.
- **Not restored:** the snapshot listing, searches, freshness, evidence, and reader code.
- **New packages:** `content` (hash jobs), `relations` (relate job and Compare), and `review` (cards, review lists, Gems).
- **Changed packages:**
  - `index`: change times, invalidation, and an after-scan hook;
  - `search`: the duplicate filter and new row fields;
  - `decisions`: selections from a list;
  - `web/api`;
  - `viewer`;
  - `config`;
  - `corpus`: tar and gzip fixtures, plus ground truth for duplicates, relations, and gems.
- **Database:** migration `0002_content.sql`, with no rescan needed.
- **Front end:** new screens and Playwright flows.
- **Docs:** `docs/operator.md` gains hashing, archives, Compare, Opportunities, and Gems.
- **Dependencies:** none new; the standard library covers zip, tar, gzip, and bzip2.
